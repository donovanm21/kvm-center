#!/usr/bin/env python3
"""Bounded Linux native-artifact acceptance; synthetic data, no release/host access.

Private fixtures, archives, generated credentials and raw subprocess logs stay
in a temporary directory and are deleted. Only sanitized measurements survive.
"""
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import signal
import subprocess
import sys
import tempfile
from datetime import datetime, timezone

from validate import ROOT, source_identity, local_test_url, go_test_evidence, missing_release_inputs

PRIOR = 'b8bdcbc5bbd72f4799f5ec14d80d8805601cd12c'


def measurements(text):
    result = []
    # test2json may split a long output line across multiple events. Keep
    # streams separate so interleaved tests cannot splice evidence together.
    streams = {}
    for line in text.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        key = (event.get('Package', ''), event.get('Test', ''))
        streams[key] = streams.get(key, '') + event.get('Output', '')
    for output in streams.values():
        for line in output.splitlines():
            if 'HISTORY_EVIDENCE ' in line:
                result.append(json.loads(line.split('HISTORY_EVIDENCE ', 1)[1]))
    expected = {(kind, backend) for kind in ('cap_costs', 'artifact_cap', 'artifact_lifecycle') for backend in ('json', 'mongo')} | {('legacy_refusal', 'mongo'), ('deployed_legacy_refusal', 'mongo'), ('entity_opaque_lifecycle', 'mongo'), ('overlay_v1_archive_restore', 'mongo'), ('overlay_v2_archive_restore', 'mongo'), ('overlay_v3_archive_restore', 'mongo'), ('guest_report_lifecycle', 'mongo'), ('alarm_namespace_refusal', 'mongo'), ('affinity_namespace_refusal', 'mongo'), ('unknown_namespace_refusal', 'mongo'), ('cached_namespace_backup_refusal', 'mongo')}
    keys = [(r['kind'], r['data']['backend']) for r in result]
    if set(keys) != expected or len(keys) != len(expected):
        raise ValueError('Missing or duplicate history acceptance measurements')
    return result


def run(argv, log, env=None, cwd=ROOT, timeout=600):
    with log.open('w') as out:
        p = subprocess.Popen(argv, cwd=cwd, env=env, stdout=out, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            code = p.wait(timeout=timeout)
        finally:
            # A test timeout may exit its leader without running Go cleanup.
            # Signal this owned session even when its leader already exited.
            try:
                os.killpg(p.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                p.wait(timeout=5)
            except subprocess.TimeoutExpired:
                pass
            finally:
                try:
                    os.killpg(p.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                p.wait()
    if code:
        raise RuntimeError(f'{Path(argv[0]).name} exited {code}; private logs omitted from public evidence')


def main():
    def interrupted(signum, _frame):
        raise KeyboardInterrupt(f'interrupted by signal {signum}')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    output = ROOT / '.validation/history/report.json'
    output.parent.mkdir(parents=True, exist_ok=False)
    report = {'schema_version': 1, 'source': source_identity(), 'started_at': datetime.now(timezone.utc).isoformat(),
              'status': 'running', 'profile': 'history-native-artifact', 'release_ready': False,
              'platform': platform.platform(), 'cpu_count': os.cpu_count(),
              'prior_revision': PRIOR, 'scope': 'Linux amd64 native release-script artifact; synthetic JSON and loopback Mongo; no history UI acceptance',
              'ceilings': {'save_max_seconds': 5, 'api_max_seconds': 2, 'heap_after_gc_bytes': 256 << 20,
                           'rss_bytes': 768 << 20, 'late_heap_growth_bytes': 32 << 20, 'fd_growth': 8, 'goroutine_growth': 8,
                           'cycles_per_backend': 20},
              'remaining_gates': ['Long-duration and concurrent fleet soak; disk-full/backend outage acceptance',
                                  'Production-image acceptance is reported separately by distributable-acceptance',
                                  'Deployed v5.61.58 schema compatibility requires explicit offline conversion',
                                  'ARM64 runtime; actual deployment lineage and backend-native recovery',
                                  'Outstanding release security acceptance'],
              'missing_container_inputs': missing_release_inputs()}
    def save():
        output.write_text(json.dumps(report, indent=2) + '\n')
    save()
    try:
        if platform.system() != 'Linux' or platform.machine() not in ('x86_64', 'amd64'):
            raise ValueError('requires disposable Linux amd64 runner')
        if report['source']['dirty']:
            raise ValueError('requires clean committed source')
        if not local_test_url(os.environ.get('KVMC_TEST_MONGO_URI', ''), ('mongodb',)):
            raise ValueError('requires disposable unauthenticated loopback Mongo')
        with tempfile.TemporaryDirectory(prefix='kvmc-history-') as temp:
            temp = Path(temp)
            env = dict(os.environ, GOMAXPROCS='2', GOFLAGS='-p=2', GIT_SHA=report['source']['revision'])
            run(['bash', 'scripts/build-webui.sh'], temp / 'webui.log', env)
            run(['bash', 'scripts/build-release.sh', str(temp / 'artifacts')], temp / 'build.log', env)
            binary = temp / 'artifacts/kvm-center-server-linux-amd64'
            report['binary_sha256'] = hashlib.sha256(binary.read_bytes()).hexdigest()
            report['toolchain'] = subprocess.check_output(['go', 'version'], text=True).strip()
            report['release_script_matrix'] = (temp / 'artifacts/SHA256SUMS').read_text().splitlines()
            prior = temp / 'prior'
            run(['git', 'worktree', 'add', '--detach', str(prior), PRIOR], temp / 'worktree.log')
            try:
                # Rollback must use the pinned revision's own frontend too.
                run(['bash', 'scripts/build-webui.sh'], temp / 'prior-webui.log', env, prior)
                version = (prior / 'server/VERSION').read_text().strip()
                old = temp / 'prior-server'
                run(['go', 'build', '-mod=readonly', '-buildvcs=false', '-trimpath',
                     f'-ldflags=-s -w -X main.versionRaw={version} -X main.GitSHA={PRIOR}',
                     '-o', str(old), './server'], temp / 'old-build.log', dict(env, CGO_ENABLED='0'), prior)
                report['prior_binary_sha256'] = hashlib.sha256(old.read_bytes()).hexdigest()
                testenv = dict(env, KVMC_HISTORY_BINARY=str(binary), KVMC_HISTORY_OLD_BINARY=str(old), KVMC_HISTORY_SHA=report['source']['revision'])
                log = temp / 'acceptance.jsonl'
                try:
                    run(['go', 'test', '-mod=readonly', '-count=1', '-tags=integration,historyacceptance',
                         '-run=^TestHistoryAcceptance', '-timeout=8m', '-json', './server'], log, testenv, timeout=500)
                finally:
                    # Preserve only test identities/outcomes and explicit measurements.
                    if log.exists():
                        events = []
                        for line in log.read_text().splitlines():
                            try: item = json.loads(line)
                            except ValueError: continue
                            if item.get('Test') and item.get('Action') in ('pass', 'fail', 'skip'):
                                events.append({k: item[k] for k in ('Test', 'Action', 'Elapsed') if k in item})
                        report['test_outcomes'] = events
                        report['assertion_diagnostics'] = [json.loads(line).get('Output', '').strip() for line in log.read_text().splitlines()
                            if line.startswith('{') and re.search(r'history_acceptance_test.go:\d+:', json.loads(line).get('Output', ''))
                            and 'HISTORY_EVIDENCE ' not in json.loads(line).get('Output', '')]
                report['tests'] = go_test_evidence(log.read_text(), 'TestHistoryAcceptance', True)
                report['measurements'] = measurements(log.read_text())
            finally:
                run(['git', 'worktree', 'remove', '--force', str(prior)], temp / 'remove.log')
            report['fixtures_removed'] = True
        report['source_unchanged'] = source_identity() == report['source']
        if not report['source_unchanged']:
            raise ValueError('source changed during validation')
        report['status'] = 'passed'
    except (Exception, KeyboardInterrupt) as exc:
        report.update(status='failed', reason=str(exc))
    finally:
        report['finished_at'] = datetime.now(timezone.utc).isoformat()
        save()
    print(json.dumps(report, indent=2))
    return 0 if report['status'] == 'passed' else 1


if __name__ == '__main__':
    sys.exit(main())
