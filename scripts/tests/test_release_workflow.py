"""Offline executable checks for the existing tag-release build commands."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]


def build_block():
    workflow = (ROOT / '.github/workflows/release.yml').read_text()
    step = workflow.split('      - name: Cross-compile binaries\n', 1)[1]
    block = step.split('        run: |\n', 1)[1].split('\n      - name:', 1)[0]
    return textwrap.dedent(block)


class ReleaseWorkflowTests(unittest.TestCase):
    def test_tag_build_stamps_the_symbol_each_binary_actually_reads(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            bindir = root / 'bin'
            bindir.mkdir()
            fake = bindir / 'go'
            fake.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
with open(os.environ['CALL_LOG'], 'a') as out:
    out.write(json.dumps({'args':sys.argv[1:],'os':os.environ['GOOS'],'arch':os.environ['GOARCH']}) + '\\n')
pathlib.Path(sys.argv[sys.argv.index('-o') + 1]).write_text('fixture binary')
''')
            fake.chmod(0o755)
            log = root / 'calls.jsonl'
            env = dict(os.environ, PATH=str(bindir) + os.pathsep + os.environ['PATH'],
                       CALL_LOG=str(log), GITHUB_REF_NAME='v1.2.3')
            result = subprocess.run(['bash', '-c', build_block()], cwd=root, env=env,
                                    capture_output=True, text=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stderr)
            calls = [json.loads(line) for line in log.read_text().splitlines()]
            self.assertEqual(len(calls), 10)  # existing published matrix is unchanged
            expected = {'./server': 'versionRaw', './agent': 'Version',
                        './kvm-tools': 'versionRaw', './cmd/kvmctl': 'cliVersion'}
            for call in calls:
                package = call['args'][-1]
                flags = [a for a in call['args'] if a.startswith('-ldflags=')]
                with self.subTest(package=package, os=call['os'], arch=call['arch']):
                    self.assertEqual(flags, [f'-ldflags=-s -w -X main.{expected[package]}=v1.2.3'])
            self.assertEqual(len((root / 'dist/SHA256SUMS').read_text().splitlines()), 10)


if __name__ == '__main__':
    unittest.main()
