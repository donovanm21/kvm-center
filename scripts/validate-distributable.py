#!/usr/bin/env python3
"""Build, never publish, the production image on an isolated hosted runner."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import signal
import subprocess
import sys
import tempfile
import uuid
from validate import ROOT, source_identity, local_test_url, go_test_evidence
spec = importlib.util.spec_from_file_location('history_runner', ROOT / 'scripts/validate-history.py')
history = importlib.util.module_from_spec(spec)
spec.loader.exec_module(history)


def cleanup_owned(runtime, volume, container, image, runtime_owned, created, built):
    cleanup = True
    for argv, owned in [(['docker','rm','-f',runtime],runtime_owned), (['docker','volume','rm',volume],runtime_owned),
                        (['docker','rm','-f',container],created), (['docker','image','rm','-f',image],built)]:
        if owned:
            try:
                result = subprocess.run(argv, capture_output=True, timeout=30)
                # Absence is expected after normal Go cleanup or an incomplete build.
                if result.returncode and not any(msg in result.stderr.decode().lower() for msg in ('no such image', 'no such container', 'no such volume')):
                    cleanup = False
            except Exception:
                cleanup = False
    return cleanup


def main():
    def interrupted(signum, _frame):
        raise KeyboardInterrupt(f"interrupted by signal {signum}")
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    output = ROOT / '.validation/distributable/report.json'
    output.parent.mkdir(parents=True, exist_ok=False)
    source = source_identity()
    report = dict(schema_version=1, source=source, status='running', release_ready=False,
                  scope='Actual linux/amd64 Dockerfile.server image; synthetic common legacy Mongo shape; no host or deployment access',
                  remaining_gates=['v5.61.58 feature/schema divergence: alarms, affinity, legacy history and extended VM fields',
                                   'Actual deployed build lineage and offline backend-native recovery',
                                   'ARM64 runtime, Windows/macOS tools and installer acceptance',
                                   'Native/UI lifecycle acceptance and outstanding release security acceptance'])
    image = 'kvmc-validation:' + uuid.uuid4().hex
    container = 'kvmc-validation-extract-' + uuid.uuid4().hex
    runtime = 'kvmc-validation-runtime-' + uuid.uuid4().hex
    volume = runtime + '-data'
    built = created = runtime_owned = False
    try:
        if platform.system() != 'Linux' or platform.machine() not in ('x86_64', 'amd64') or source['dirty']:
            raise ValueError('requires clean committed source on disposable Linux amd64')
        if not local_test_url(os.environ.get('KVMC_TEST_MONGO_URI', ''), ('mongodb',)):
            raise ValueError('requires disposable loopback Mongo')
        with tempfile.TemporaryDirectory(prefix='kvmc-package-') as tmp:
            tmp = Path(tmp)
            # Archive tracked source only: never transmit local .git, state or credentials to Docker.
            archive = subprocess.check_output(['git', 'archive', 'HEAD'], cwd=ROOT, timeout=30)
            context = tmp / 'context'; context.mkdir()
            archive_path = tmp / 'source.tar'; archive_path.write_bytes(archive)
            history.run(['tar', '-xf', str(archive_path), '-C', str(context)], tmp / 'archive.log')
            built = True
            history.run(['docker', 'build', '--platform=linux/amd64', '--build-arg', 'GIT_SHA='+source['revision'],
                         '-f', 'Dockerfile.server', '-t', image, '.'], tmp / 'build.log', cwd=context, timeout=1200)
            report['image_id'] = subprocess.check_output(['docker', 'image', 'inspect', image, '--format', '{{.Id}}'], text=True, timeout=30).strip()
            created = True
            history.run(['docker', 'create', '--name', container, image], tmp / 'create.log')
            history.run(['docker', 'cp', container+':/app', str(tmp / 'app')], tmp / 'copy.log')
            version = (ROOT / 'server/VERSION').read_text().strip()
            names = ['kvm-center-agent-amd64', 'kvm-center-agent-arm64', 'kvm-tools-linux-amd64',
                     'kvm-tools-linux-arm64', 'kvm-tools-windows-amd64.exe', 'kvmctl-linux-amd64',
                     'kvmctl-linux-arm64', 'kvmctl-darwin-amd64', 'kvmctl-darwin-arm64', 'kvmctl-windows-amd64.exe']
            paths = [tmp / 'app/kvm-center-server'] + [tmp / 'app/dist' / n for n in names]
            report['artifacts'] = []
            for p in paths:
                metadata = subprocess.check_output(['go', 'version', '-m', str(p)], text=True, timeout=30)
                # Go intentionally omits -ldflags build metadata with -trimpath.
                # Check embedded stamp payloads; execute Linux version commands below.
                raw = p.read_bytes()
                if source['revision'].encode() not in raw or version.encode() not in raw:
                    raise ValueError('embedded artifact stamp mismatch: '+p.name)
                target_os = 'windows' if 'windows' in p.name else ('darwin' if 'darwin' in p.name else 'linux')
                target_arch = 'arm64' if 'arm64' in p.name else 'amd64'
                if 'GOOS='+target_os not in metadata or 'GOARCH='+target_arch not in metadata:
                    raise ValueError('artifact target metadata mismatch: '+p.name)
                report['artifacts'].append(dict(name=p.name, sha256=hashlib.sha256(raw).hexdigest(),
                                               embedded_stamps=True, target_metadata=[line.strip() for line in metadata.splitlines() if 'GOOS=' in line or 'GOARCH=' in line]))
            report['linux_version_commands'] = []
            for name, flag in [('kvm-center-agent-amd64','-version'), ('kvm-tools-linux-amd64','-version'), ('kvmctl-linux-amd64','version')]:
                # Version-only exits precede configuration access, workers and native actions.
                text = subprocess.check_output([str(tmp / 'app/dist' / name), flag], stderr=subprocess.STDOUT, text=True, timeout=10)
                if source['revision'] not in text or version not in text:
                    raise ValueError('runtime tool stamp mismatch: '+name)
                report['linux_version_commands'].append(name)
            report['version'] = version
            runtime_owned = True
            env = dict(os.environ, KVMC_PACKAGE_CONTAINER=runtime, KVMC_PACKAGE_VOLUME=volume, GOMAXPROCS='2', GOFLAGS='-p=2', KVMC_PACKAGE_IMAGE=image,
                       KVMC_PACKAGE_SHA=source['revision'], KVMC_PACKAGE_VERSION=version)
            log = tmp / 'acceptance.jsonl'
            try:
                history.run(['go', 'test', '-mod=readonly', '-count=1', '-tags=integration,historyacceptance,packagingacceptance',
                             '-run=^TestDistributable', '-timeout=3m', '-json', './server'], log, env, timeout=200)
            finally:
                events = [json.loads(line) for line in log.read_text().splitlines() if line.startswith('{')] if log.exists() else []
                report['test_outcomes'] = [{k:e[k] for k in ('Test','Action','Elapsed') if k in e} for e in events if e.get('Test') and e.get('Action') in ('pass','fail','skip')]
                report['assertion_diagnostics'] = [e['Output'].strip() for e in events if 'distributable_acceptance_test.go:' in e.get('Output','') and 'PACKAGE_EVIDENCE ' not in e.get('Output','')]
                report['runtime'] = [json.loads(e['Output'].split('PACKAGE_EVIDENCE ',1)[1]) for e in events if 'PACKAGE_EVIDENCE ' in e.get('Output','')]
            report['tests'] = go_test_evidence(log.read_text(), 'TestDistributable', True)
        if source_identity() != source:
            raise ValueError('source changed during validation')
        report['source_unchanged'] = True
        report['status'] = 'passed'
    except (Exception, KeyboardInterrupt) as exc:
        report.update(status='failed', reason=str(exc))
    finally:
        cleanup = cleanup_owned(runtime, volume, container, image, runtime_owned, created, built)
        report['owned_resource_cleanup'] = cleanup
        if not cleanup: report['status']='failed'
        output.write_text(json.dumps(report,indent=2)+'\n')
    print(json.dumps(report,indent=2))
    return 0 if report['status']=='passed' else 1

if __name__ == '__main__': sys.exit(main())
