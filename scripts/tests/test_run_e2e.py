import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "run-e2e.sh"


class RunE2ETests(unittest.TestCase):
    def run_wrapper(self, ci=False, sha="a" * 40, backend="json", run_code=0):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "scripts").mkdir()
            (root / "demo/e2e").mkdir(parents=True)
            shutil.copyfile(SCRIPT, root / "scripts/run-e2e.sh")
            binary = root / "bin"
            binary.mkdir()
            log = root / "docker.jsonl"
            docker = binary / "docker"
            docker.write_text("#!/usr/bin/env python3\n"
                              "import json, os, sys\n"
                              "with open(os.environ['DOCKER_LOG'], 'a') as f: f.write(json.dumps(sys.argv[1:])+'\\n')\n"
                              "if sys.argv[1] == 'create': print('test-helper')\n"
                              "if sys.argv[1] == 'run': sys.exit(int(os.environ['DOCKER_RUN_CODE']))\n")
            docker.chmod(0o755)
            env = dict(os.environ, PATH=str(binary) + os.pathsep + os.environ["PATH"],
                       CI="1" if ci else "", KVMC_EXPECTED_SHA=sha,
                       KVMC_EXPECTED_BACKEND=backend, KVMC_DEMO_URL="https://localhost:8290",
                       DOCKER_LOG=str(log), DOCKER_RUN_CODE=str(run_code))
            result = subprocess.run(["bash", str(root / "scripts/run-e2e.sh"), "specs/auth.spec.ts"],
                                    env=env, capture_output=True, text=True)
            calls = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
            return result, calls

    def test_revision_and_backend_reach_both_docker_paths(self):
        for ci in (False, True):
            with self.subTest(ci=ci):
                result, calls = self.run_wrapper(ci=ci)
                self.assertEqual(result.returncode, 0, result.stderr)
                runs = [args for args in calls if args[0] == "run"]
                self.assertEqual(len(runs), 1)
                self.assertIn("KVMC_EXPECTED_SHA=" + "a" * 40, runs[0])
                self.assertIn("KVMC_EXPECTED_BACKEND=json", runs[0])
                self.assertIn("KVMC_DEMO_URL=https://host.docker.internal:8290", runs[0])
                self.assertIn("specs/auth.spec.ts", runs[0])

    def test_browser_failure_is_not_hidden_by_ci_cleanup(self):
        for ci in (False, True):
            result, _ = self.run_wrapper(ci=ci, run_code=17)
            self.assertEqual(result.returncode, 17)

    def test_bad_revision_fails_before_docker(self):
        result, calls = self.run_wrapper(sha="main")
        self.assertEqual(result.returncode, 2)
        self.assertEqual(calls, [])

    def test_bad_backend_fails_before_docker(self):
        result, calls = self.run_wrapper(backend="production")
        self.assertEqual(result.returncode, 2)
        self.assertEqual(calls, [])


if __name__ == "__main__":
    unittest.main()
