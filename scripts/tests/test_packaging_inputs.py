"""Offline packaging/script tests. All service and network commands are mocks."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class PackagingInputTests(unittest.TestCase):
    def stub(self, root, name, body):
        path = root / name
        path.write_text('#!/usr/bin/env python3\n' + body)
        path.chmod(0o755)

    def test_required_sources_and_docker_stamps(self):
        for name in ('update-agent.sh', 'install-kvmctl.sh', 'install-kvmctl.ps1', 'libvirt-hooks-qemu.sh'):
            self.assertTrue((ROOT / 'scripts' / name).stat().st_size)
        text = (ROOT / 'Dockerfile.server').read_text()
        self.assertIn('RUN npm ci ', text)
        self.assertIn('npm run build && test -s dist/index.html', text)
        self.assertIn('test -s server/staticv4/dist/index.html', text)
        self.assertEqual(text.count('ARG GIT_SHA=dev'), 2)
        self.assertIn('main.versionRaw=$VERSION -X main.GitSHA=$GIT_SHA', text)
        self.assertIn('-ldflags="$GUEST_LDF" -o /dist/kvm-tools', text)
        self.assertNotIn('go mod tidy', text)
        self.assertNotIn('go build -ldflags=', text)

    def test_docker_build_commands_stamp_actual_symbols(self):
        text=(ROOT/'Dockerfile.server').read_text()
        blocks=[]
        lines=iter(text.splitlines())
        for line in lines:
            if not line.startswith('RUN '): continue
            command=line[4:]
            while command.rstrip().endswith('\\'):
                following=next(lines)
                if following.lstrip().startswith('#'): continue
                command=command.rstrip()[:-1]+' '+following.strip()
            if 'go build ' in command: blocks.append(command)
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            self.stub(root,'go', "import os,sys,json\nwith open(os.environ['CALLS'],'a') as f: f.write(json.dumps({'args':sys.argv[1:],'os':os.environ.get('GOOS'),'arch':os.environ.get('GOARCH')})+'\\n')\n")
            env=dict(os.environ,PATH=str(root)+os.pathsep+os.environ['PATH'],CALLS=str(root/'calls'),GIT_SHA='a'*40,TARGETOS='linux',TARGETARCH='amd64')
            mocks='cat() { echo v1.2.3; }; mkdir() { :; }; cp() { :; }; ln() { :; }; chmod() { :; }; tar() { :; }; test() { :; }; '
            for block in blocks:
                result=subprocess.run(['bash','-c',mocks+block],env=env,capture_output=True,text=True)
                self.assertEqual(result.returncode,0,result.stderr)
            calls=[json.loads(line) for line in (root/'calls').read_text().splitlines()]
            self.assertEqual(len(calls),12)
            symbols={'./server':'versionRaw','./agent':'Version','./kvm-tools':'versionRaw','./cmd/kvmctl':'cliVersion'}
            for call in calls:
                package=call['args'][-1]
                if package not in symbols: continue
                flags=next(a for a in call['args'] if a.startswith('-ldflags='))
                self.assertIn('main.'+symbols[package]+'=v1.2.3',flags)
                self.assertIn('main.GitSHA='+'a'*40,flags)

    def test_hook_event_status_encoding_and_stdout(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.stub(root, 'curl', "import os,sys,json\nopen(os.environ['CALLS'],'a').write(json.dumps(sys.argv[1:])+'\\n')\nprint(os.environ['STATUS'],end='')\nsys.exit(int(os.environ.get('CURL_EXIT','0')))\n")
            self.stub(root, 'logger', 'pass\n')
            env = dict(os.environ, PATH=str(root)+os.pathsep+os.environ['PATH'], CALLS=str(root/'calls'))
            for op, sub, status, code, want in [('prepare','begin','403',0,0),('start','begin','403',0,0),('migrate','begin','200',0,0),('migrate','begin','403',0,1),('migrate','begin','500',0,0),('migrate','begin','000',7,0)]:
                (root/'calls').write_text('')
                result = subprocess.run(['bash', str(ROOT/'scripts/libvirt-hooks-qemu.sh'), 'vm & one', op, sub], env=dict(env, STATUS=status, CURL_EXIT=str(code)), capture_output=True, text=True)
                self.assertEqual(result.returncode, want, result.stderr)
                self.assertEqual(result.stdout, '')
                calls = (root/'calls').read_text().splitlines()
                self.assertEqual(len(calls), int(op=='migrate'))
                if calls:
                    args = json.loads(calls[0]); self.assertIn('vm=vm & one', args); self.assertIn('--data-urlencode', args)

    def test_cli_staging_and_missing_argument(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp); bindir=root/'bin';bindir.mkdir(); target=root/'install';target.mkdir()
            self.stub(bindir,'uname', "import sys\nprint('Linux' if sys.argv[1]=='-s' else 'x86_64')\n")
            self.stub(bindir,'curl', "import os,sys,pathlib\np=pathlib.Path(sys.argv[sys.argv.index('-o')+1]);p.write_text('#!/bin/sh\\nexit '+os.environ.get('BINARY_EXIT','0')+'\\n')\n")
            env=dict(os.environ,PATH=str(bindir)+os.pathsep+os.environ['PATH'])
            script=str(ROOT/'scripts/install-kvmctl.sh')
            args=['bash',script,'--portal','https://fixture.invalid/downloads','--install-dir',str(target),'--force']
            (target/'kvmctl').write_text('old')
            result=subprocess.run(args,env=dict(env,BINARY_EXIT='1'),capture_output=True,text=True)
            self.assertNotEqual(result.returncode,0,result.stdout+result.stderr);self.assertEqual((target/'kvmctl').read_text(),'old')
            result=subprocess.run(args,env=env,capture_output=True,text=True)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual(subprocess.run(['bash',script,'--portal'],capture_output=True).returncode,1)

    def test_agent_update_success_and_rollback_are_scoped(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);bindir=root/'bin';bindir.mkdir();install=root/'install';install.mkdir()
            self.stub(bindir,'id', "print('0')\n")
            self.stub(bindir,'uname', "print('x86_64')\n")
            self.stub(bindir,'curl', "import sys,pathlib\nif '-o' in sys.argv: pathlib.Path(sys.argv[sys.argv.index('-o')+1]).write_text('#!/bin/sh\\n# candidate\\nexit 0\\n')\n")
            self.stub(bindir,'systemctl', "import os,sys,pathlib\np=pathlib.Path(os.environ['CALLS']);s=p.read_text() if p.exists() else '';p.write_text(s+' '.join(sys.argv[1:])+'\\n')\nif os.environ.get('FAIL_START')=='1' and sys.argv[1]=='start': sys.exit(1)\n")
            self.stub(bindir,'restorecon','pass\n')
            env=dict(os.environ,PATH=str(bindir)+os.pathsep+os.environ['PATH'],CALLS=str(root/'calls'))
            args=['bash',str(ROOT/'scripts/update-agent.sh'),'--from','https://fixture.invalid/downloads','--install-dir',str(install)]
            binary=install/'kvm-center-agent';binary.write_text('old')
            result=subprocess.run(args,env=dict(env,FAIL_START='1'),capture_output=True,text=True)
            self.assertNotEqual(result.returncode,0,result.stdout+result.stderr);self.assertEqual(binary.read_text(),'old')
            result=subprocess.run(args,env=env,capture_output=True,text=True)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual((install/'kvm-center-agent.previous').read_text(),'old')
            for line in (root/'calls').read_text().splitlines(): self.assertTrue(line.endswith('kvm-center-agent'),line)

    def test_agent_interruption_restores_previous_binary(self):
        for phase in ('stop','rename'):
            with self.subTest(phase=phase), tempfile.TemporaryDirectory() as tmp:
                root=Path(tmp);bindir=root/'bin';bindir.mkdir();install=root/'install';install.mkdir()
                self.stub(bindir,'id', "print('0')\n")
                self.stub(bindir,'uname', "print('x86_64')\n")
                self.stub(bindir,'curl', "import sys,pathlib\nif '-o' in sys.argv: pathlib.Path(sys.argv[sys.argv.index('-o')+1]).write_text('#!/bin/sh\\nexit 0\\n')\n")
                self.stub(bindir,'systemctl', "import os,sys,signal,pathlib\np=pathlib.Path(os.environ['CALLS']);s=p.read_text() if p.exists() else '';p.write_text(s+' '.join(sys.argv[1:])+'\\n')\nif os.environ['PHASE']=='stop' and sys.argv[1]=='stop' and not s: os.kill(os.getppid(),signal.SIGTERM)\n")
                self.stub(bindir,'mv', "import os,sys,signal,shutil\nsrc,dst=sys.argv[-2:];shutil.move(src,dst)\nif os.environ['PHASE']=='rename' and '/.agent-update.' in src: os.kill(os.getppid(),signal.SIGTERM)\n")
                env=dict(os.environ,PATH=str(bindir)+os.pathsep+os.environ['PATH'],CALLS=str(root/'calls'),PHASE=phase)
                binary=install/'kvm-center-agent';binary.write_text('old')
                result=subprocess.run(['bash',str(ROOT/'scripts/update-agent.sh'),'--from','https://fixture.invalid/downloads','--install-dir',str(install)],env=env,capture_output=True,text=True,timeout=10)
                self.assertNotEqual(result.returncode,0,result.stdout+result.stderr)
                self.assertEqual(binary.read_text(),'old')
                self.assertIn('start kvm-center-agent',(root/'calls').read_text())

    def test_fresh_build_wrapper_orders_steps_and_stops_on_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);scripts=root/'scripts';scripts.mkdir()
            (scripts/'build-package.sh').write_bytes((ROOT/'scripts/build-package.sh').read_bytes())
            (scripts/'build-webui.sh').write_text('echo webui >> "$CALLS"; exit "${FAIL:-0}"\n')
            (scripts/'build-release.sh').write_text('echo release >> "$CALLS"\n')
            env=dict(os.environ,CALLS=str(root/'calls'))
            self.assertEqual(subprocess.run(['bash',str(scripts/'build-package.sh')],env=env).returncode,0)
            self.assertEqual((root/'calls').read_text(),'webui\nrelease\n')
            (root/'calls').write_text('')
            self.assertNotEqual(subprocess.run(['bash',str(scripts/'build-package.sh')],env=dict(env,FAIL='1')).returncode,0)
            self.assertEqual((root/'calls').read_text(),'webui\n')

    def test_existing_hook_is_preserved_and_windows_matrix_is_honest(self):
        text=(ROOT/'scripts/install.sh').read_text().split('# Never overwrite an operator',1)[1].split('# Firewalld:',1)[0]
        self.assertIn('if [[ -e /etc/libvirt/hooks/qemu || -L /etc/libvirt/hooks/qemu ]]',text)
        self.assertNotIn('systemctl',text)
        ps=(ROOT/'scripts/install-kvmctl.ps1').read_text()
        self.assertIn("'ARM64' { Die",ps)
        self.assertIn('finally {',ps)
        self.assertIn('& $temp version',ps)
        self.assertLess(ps.index('& $temp version'),ps.index('Move-Item -Force'))

if __name__=='__main__': unittest.main()
