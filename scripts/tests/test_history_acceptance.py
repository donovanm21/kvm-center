"""Offline checks for evidence completeness and owned-process cleanup."""
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location('history_acceptance', SCRIPTS / 'validate-history.py')
history = importlib.util.module_from_spec(spec)
spec.loader.exec_module(history)


class HistoryEvidenceTests(unittest.TestCase):
    def fixture(self):
        return [json.dumps({'Output': 'HISTORY_EVIDENCE ' + json.dumps({'kind': kind, 'data': {'backend': backend}}) + '\n'})
                for kind in ('cap_costs', 'artifact_cap', 'artifact_lifecycle') for backend in ('json', 'mongo')] + [json.dumps({'Output': 'HISTORY_EVIDENCE ' + json.dumps({'kind': 'legacy_history_reader', 'data': {'backend': 'offline'}}) + '\n'})] + [json.dumps({'Output': 'HISTORY_EVIDENCE ' + json.dumps({'kind': extra, 'data': {'backend': 'mongo'}}) + '\n'}) for extra in ('active_legacy_history_lifecycle', 'deployed_legacy_refusal', 'entity_opaque_lifecycle', 'overlay_v1_archive_restore', 'overlay_v2_archive_restore', 'overlay_v3_archive_restore', 'overlay_v4_archive_restore', 'overlay_v5_archive_restore', 'overlay_v6_archive_restore', 'overlay_v7_archive_restore', 'overlay_v8_archive_restore', 'remaining_alarm_metrics_lifecycle', 'resource_alarm_lifecycle', 'session_lifecycle', 'token_lifecycle', 'audit_lifecycle', 'guest_report_lifecycle', 'alarm_namespace_refusal', 'affinity_namespace_refusal', 'unknown_namespace_refusal', 'cached_namespace_backup_refusal')]

    def test_requires_every_backend_and_phase_once(self):
        rows = self.fixture()
        rows.append(json.dumps({'Output': 'HISTORY_EVIDENCE ' + json.dumps({'kind':'legacy_refusal','data':{'backend':'mongo'}}) + '\n'}))
        self.assertEqual(len(history.measurements('\n'.join(rows))), 29)
        for bad in (rows[:-1], rows + [rows[0]], []):
            with self.assertRaises(ValueError):
                history.measurements('\n'.join(bad))

    def test_fragmented_interleaved_evidence(self):
        rows = self.fixture()
        payload = 'HISTORY_EVIDENCE ' + json.dumps({
            'kind': 'legacy_refusal', 'data': {'backend': 'mongo',
            'checksums': {str(i): 'a' * 64 for i in range(60)}}}) + '\n'
        chunks = [payload[i:i + 511] for i in range(0, len(payload), 511)]
        for chunk in chunks:
            rows.append(json.dumps({'Package': 'server', 'Test': 'legacy', 'Output': chunk}))
            rows.append(json.dumps({'Package': 'server', 'Test': 'other', 'Output': 'noise\n'}))
        result = history.measurements('\n'.join(rows))
        self.assertEqual(len(result), 29)
        self.assertEqual(len(result[-1]['data']['checksums']), 60)
        with self.assertRaises(ValueError):
            history.measurements('\n'.join(rows[:-2]))

    def test_assertion_diagnostics_redacts_arbitrary_output(self):
        secret = 'PRIVATE-DO-NOT-EXPORT'
        rows = [json.dumps({'Test': 'TestHistoryAcceptanceCachedBackupNamespaceRefusal',
                 'Output': '    namespace_gate_acceptance_test.go:51: ' + secret}),
                json.dumps({'Test': 'TestHistoryAcceptanceResourceAlarmLifecycle',
                 'Output': '    alarm_preservation_acceptance_test.go:80: HISTORY_FAILURE request_status_mismatch\n'}),
                json.dumps({'Test': secret, 'Output': 'history_acceptance_test.go:99: ' + secret}),
                json.dumps({'Output': secret}), 'not json', '[]']
        result = history.assertion_diagnostics('\n'.join(rows))
        self.assertEqual(result, [
            {'test':'TestHistoryAcceptanceCachedBackupNamespaceRefusal', 'file':'namespace_gate_acceptance_test.go', 'line':51},
            {'test':'TestHistoryAcceptanceResourceAlarmLifecycle', 'file':'alarm_preservation_acceptance_test.go', 'line':80, 'code':'request_status_mismatch'}])
        self.assertNotIn(secret, json.dumps(result))

    @unittest.skipUnless(sys.platform == 'linux', 'owned Linux process-group regression runs in hosted CI')
    def test_nonzero_leader_cleans_owned_descendant(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            pidfile = tmp / 'child.pid'
            code = "import subprocess, pathlib, sys; p=subprocess.Popen([sys.executable,'-c','import time; time.sleep(60)']); pathlib.Path(sys.argv[1]).write_text(str(p.pid)); sys.exit(2)"
            with self.assertRaises(RuntimeError):
                history.run([sys.executable, '-c', code, str(pidfile)], tmp / 'log', timeout=5)
            # A killed orphan can briefly remain a zombie awaiting the runner's
            # init; it must not remain a live process or retain open fixtures.
            pid = int(pidfile.read_text())
            subprocess.run(['bash', '-c', f'for i in {{1..30}}; do test ! -e /proc/{pid}/stat && exit 0; test "$(cut -d " " -f3 /proc/{pid}/stat)" = Z && exit 0; sleep .1; done; exit 1'], check=True, timeout=5)


if __name__ == '__main__':
    unittest.main()
