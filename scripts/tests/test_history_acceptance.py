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
        return [json.dumps({'Output': 'HISTORY_EVIDENCE ' + json.dumps({'kind': kind, 'data': {'backend': backend}})})
                for kind in ('cap_costs', 'artifact_cap', 'artifact_lifecycle') for backend in ('json', 'mongo')]

    def test_requires_every_backend_and_phase_once(self):
        rows = self.fixture()
        rows.append(json.dumps({'Output': 'HISTORY_EVIDENCE ' + json.dumps({'kind':'legacy_refusal','data':{'backend':'mongo'}})}))
        self.assertEqual(len(history.measurements('\n'.join(rows))), 7)
        for bad in (rows[:-1], rows + [rows[0]], []):
            with self.assertRaises(ValueError):
                history.measurements('\n'.join(bad))

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
