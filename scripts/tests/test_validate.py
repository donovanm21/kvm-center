"""Regression tests for false-green validation and release packaging."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / "validate.py"
spec = importlib.util.spec_from_file_location("validation", SCRIPT)
validation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(validation)


class EvidenceTests(unittest.TestCase):
    def test_go_zero_tests_and_skip_only_fail(self):
        for text in ("", '{"Action":"pass","Package":"x"}', '{"Action":"skip","Test":"TestMongoIntegrationX"}'):
            with self.subTest(text=text), self.assertRaises(ValueError):
                validation.go_test_evidence(text, "TestMongoIntegration", True)

    def test_go_skips_and_package_failures_are_not_integration_success(self):
        passed = '{"Action":"pass","Test":"TestMongoIntegrationX"}\n'
        self.assertEqual(validation.go_test_evidence(passed, "TestMongoIntegration", True)["passed"], 1)
        for suffix in ('{"Action":"skip","Test":"TestMongoIntegrationY"}', '{"Action":"fail","Package":"x"}'):
            with self.subTest(suffix=suffix), self.assertRaises(ValueError):
                validation.go_test_evidence(passed + suffix, "TestMongoIntegration", True)

    def test_browser_empty_failed_skipped_flaky_or_global_error_fail(self):
        for data in ({}, {"stats": {"expected": 0}}, {"stats": {"expected": 1, "unexpected": 1}},
                     {"stats": {"expected": 1, "skipped": 1}}, {"stats": {"expected": 1, "flaky": 1}},
                     {"stats": {"expected": 1}, "errors": ["server failed"]}):
            with self.subTest(data=data), self.assertRaises(ValueError):
                validation.browser_evidence(json.dumps(data))
        self.assertEqual(validation.browser_evidence('{"stats":{"expected":2}}')["expected"], 2)

    def test_production_urls_are_rejected(self):
        for value in ("mongodb://production:27017", "mongodb://user:pass@localhost", "mongodb://localhost.evil", "mongodb://localhost,remote", "mongodb+srv://localhost", "mongodb://localhost:27017,remote:27017",
                      "mongodb://localhost/?proxyHost=remote", "mongodb://localhost/?replicaSet=shared",
                      "mongodb://localhost/?directConnection=false", "mongodb://localhost:0",
                      "mongodb://localhost:70000", "mongodb://localhost/production", "mongodb://["):
            self.assertFalse(validation.local_test_url(value, ("mongodb",)), value)
        self.assertTrue(validation.local_test_url("mongodb://127.0.0.1:27017", ("mongodb",)))

    def test_all_packaging_inputs_required(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.assertEqual(set(validation.missing_release_inputs(root)), set(validation.RELEASE_INPUTS))
            for name in validation.RELEASE_INPUTS:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("fixture")
            self.assertEqual(validation.missing_release_inputs(root), [])
            (root / validation.RELEASE_INPUTS[0]).write_text("")
            self.assertEqual(validation.missing_release_inputs(root), [validation.RELEASE_INPUTS[0]])

    def test_missing_executable_and_failure_never_pass(self):
        with tempfile.TemporaryDirectory() as tmp:
            runner = validation.Runner("source", Path(tmp) / "evidence", 2)
            runner.command("vet", ["/nonexistent/kvm-validation-tool"])
            self.assertEqual(runner.report["gates"]["vet"]["status"], "blocked")
            runner.command("unit", ["bash", "-c", "exit 7"])
            self.assertEqual(runner.report["gates"]["unit"]["status"], "failed")
            self.assertFalse(runner.report["selected_gates_passed"])
            self.assertFalse(runner.report["release_gates_passed"])

    def test_timeout_fails_and_evidence_cannot_be_overwritten(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "evidence"
            runner = validation.Runner("source", out, 1)
            runner.command("vet", ["bash", "-c", "sleep 10"])
            self.assertEqual(runner.report["gates"]["vet"]["status"], "failed")
            with self.assertRaises(FileExistsError):
                validation.Runner("source", out)

    def test_binaries_cannot_reuse_stale_webui(self):
        with tempfile.TemporaryDirectory() as tmp:
            runner = validation.Runner("source", Path(tmp) / "evidence")
            with patch.object(runner, "command") as command:
                runner.run_gate("binaries")
                command.assert_not_called()
            self.assertEqual(runner.report["gates"]["binaries"]["status"], "blocked")

    def test_release_is_blocked_without_real_lab_evidence(self):
        with tempfile.TemporaryDirectory() as tmp:
            runner = validation.Runner("release", Path(tmp) / "evidence")
            runner.run_gate("hypervisor")
            self.assertEqual(runner.report["gates"]["hypervisor"]["status"], "blocked")
            self.assertFalse(runner.report["release_gates_passed"])

    def test_aggregate_success_requires_finished_source_verification(self):
        with tempfile.TemporaryDirectory() as tmp:
            runner = validation.Runner("release", Path(tmp) / "evidence")
            for gate in runner.report["gates"].values():
                gate["status"] = "passed"
            for fields, expected in (({}, False), ({"source_unchanged": True}, False),
                                     ({"finished_at": "fixture-finished"}, True),
                                     ({"source_unchanged": False}, False)):
                runner.report.update(fields)
                runner.save()
                with self.subTest(fields=fields):
                    self.assertEqual(runner.report["selected_gates_passed"], expected)
                    self.assertEqual(runner.report["release_gates_passed"], expected)

    def test_binaries_stamp_recorded_revision_despite_ambient_sha(self):
        with tempfile.TemporaryDirectory() as tmp:
            runner = validation.Runner("source", Path(tmp) / "evidence")
            runner.report["gates"]["webui"]["status"] = "passed"
            with patch.dict(os.environ, {"GIT_SHA": "stale-ambient-revision"}), patch.object(runner, "command") as command:
                runner.run_gate("binaries")
            self.assertEqual(command.call_args.kwargs.get("env", {}).get("GIT_SHA"), runner.report["source"]["revision"])

    def test_dirty_browser_candidate_is_blocked_before_execution(self):
        with tempfile.TemporaryDirectory() as tmp:
            runner = validation.Runner("browser", Path(tmp) / "evidence")
            runner.report["source"]["dirty"] = True
            with patch.dict(os.environ, {"KVMC_DISPOSABLE_DEMO": "1", "KVMC_DEMO_URL": "https://127.0.0.1:18290"}), \
                    patch.object(Path, "is_file", return_value=True), patch.object(runner, "command") as command:
                runner.run_gate("browser")
                self.assertEqual(command.call_count, 0)
            self.assertEqual(runner.report["gates"]["browser"]["status"], "blocked")
            self.assertIn("clean", runner.report["gates"]["browser"]["reason"])


class PackagingTests(unittest.TestCase):
    def test_release_matrix_versions_and_spa_precondition(self):
        # Execute the real builder with a recording fake compiler, not regex
        # assertions that could pass while the invocation is broken.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "scripts").mkdir()
            (root / "server").mkdir()
            (root / "server/VERSION").write_text("v1.2.3")
            build = root / "scripts/build-release.sh"
            build.write_text((SCRIPT.parent / "build-release.sh").read_text())
            env = dict(os.environ, VERSION="v1.2.3", GIT_SHA="fixture")
            missing = subprocess.run(["bash", str(build)], env=env, capture_output=True, text=True)
            self.assertNotEqual(missing.returncode, 0)
            self.assertIn("Production SPA missing", missing.stderr)
            (root / "server/staticv4/dist").mkdir(parents=True)
            (root / "server/staticv4/dist/index.html").write_text("fixture SPA")
            bindir = root / "bin"
            bindir.mkdir()
            fake = bindir / "go"
            fake.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
with open(os.environ["CALL_LOG"], "a") as out:
    out.write(json.dumps({"args":sys.argv[1:],"os":os.environ["GOOS"],"arch":os.environ["GOARCH"]}) + "\\n")
pathlib.Path(sys.argv[sys.argv.index("-o")+1]).write_text("fixture binary")
''')
            fake.chmod(0o755)
            calls = root / "calls.jsonl"
            env.update(PATH=str(bindir) + os.pathsep + env["PATH"], CALL_LOG=str(calls))
            result = subprocess.run(["bash", str(build)], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            rows = [json.loads(line) for line in calls.read_text().splitlines()]
            self.assertEqual(len(rows), 12)
            for row in rows:
                pkg = row["args"][-1]
                variable = {"./server": "versionRaw", "./kvm-tools": "versionRaw", "./agent": "Version", "./cmd/kvmctl": "cliVersion"}[pkg]
                self.assertTrue(any(f"main.{variable}=v1.2.3" in arg for arg in row["args"]))
                self.assertTrue(any("main.GitSHA=fixture" in arg for arg in row["args"]))
                self.assertIn("-mod=readonly", row["args"])
            self.assertEqual(len((root / "dist/SHA256SUMS").read_text().splitlines()), 12)

class HypervisorHarnessTests(unittest.TestCase):
    def test_help_and_list_do_not_require_credentials(self):
        env = dict(os.environ)
        env.pop("KVMC_TOKEN", None)
        with tempfile.TemporaryDirectory() as tmp:
            env["KVMC_E2E_OUTDIR"] = tmp
            for option in ("--help", "--list"):
                result = subprocess.run(["bash", str(SCRIPT.parent / "e2e-hypervisor.sh"), option], env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("M2", result.stdout)

    def test_list_does_not_overwrite_existing_lab_evidence(self):
        with tempfile.TemporaryDirectory() as tmp:
            log = Path(tmp) / "run.log"
            log.write_text("preserved lab evidence\n")
            env = dict(os.environ, KVMC_E2E_OUTDIR=tmp)
            env.pop("KVMC_TOKEN", None)
            result = subprocess.run(["bash", str(SCRIPT.parent / "e2e-hypervisor.sh"), "--list"], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(log.read_text(), "preserved lab evidence\n")

    def test_missing_option_value_fails_without_network(self):
        for option in ("--scenario", "--server", "--host", "--peer"):
            result = subprocess.run(["bash", str(SCRIPT.parent / "e2e-hypervisor.sh"), option], capture_output=True, text=True)
            self.assertEqual(result.returncode, 2)
            self.assertIn("requires a value", result.stderr)


class BrowserFixtureTests(unittest.TestCase):
    def test_mongo_normalizer_preserves_driver_query_separator(self):
        fixture = (SCRIPT.parent / "run-browser-fixture.sh").read_text()
        normalizer = fixture.split("<<'PYMONGO'\n", 1)[1].split("\nPYMONGO", 1)[0]
        for raw in ("mongodb://127.0.0.1:27017", "mongodb://localhost/",
                    "mongodb://[::1]:27017/?directConnection=true"):
            with self.subTest(raw=raw):
                result = subprocess.run([sys.executable, "-", raw], input=normalizer,
                                        cwd=SCRIPT.parent.parent, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertTrue(result.stdout.strip().endswith("/?directConnection=true"))

    def test_dirty_candidate_stops_before_fixture_tools(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "scripts").mkdir()
            fixture = root / "scripts/run-browser-fixture.sh"
            fixture.write_text((SCRIPT.parent / fixture.name).read_text())
            bindir = root / "bin"
            bindir.mkdir()
            git = bindir / "git"
            git.write_text("#!/bin/sh\nprintf ' M tracked.go\\n'\n")
            git.chmod(0o755)
            called = root / "fixture-tool-called"
            python = bindir / "python3"
            python.write_text('#!/bin/sh\nprintf called > "$CALL_LOG"\nexit 77\n')
            python.chmod(0o755)
            env = dict(os.environ, PATH=str(bindir) + os.pathsep + os.environ["PATH"],
                       KVMC_TEST_BACKEND="json", CALL_LOG=str(called))
            result = subprocess.run(["bash", str(fixture)], env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("clean", result.stderr)
            self.assertFalse(called.exists(), "fixture tool ran before candidate identity was checked")

    def test_candidate_drift_during_build_stops_before_seeding(self):
        for drift in ("revision", "dirty"):
            with self.subTest(drift=drift), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                scripts = root / "scripts"
                scripts.mkdir()
                fixture = scripts / "run-browser-fixture.sh"
                fixture.write_text((SCRIPT.parent / fixture.name).read_text())
                webui = scripts / "build-webui.sh"
                webui.write_text("#!/bin/sh\nexit 0\n")
                webui.chmod(0o755)
                bindir = root / "bin"
                bindir.mkdir()
                git = bindir / "git"
                git.write_text(f"#!{sys.executable}\n" + '''import os, pathlib, sys
kind = sys.argv[1]
state = pathlib.Path(os.environ["GIT_COUNTER_DIR"]) / kind
count = int(state.read_text()) + 1 if state.exists() else 1
state.write_text(str(count))
if kind == "rev-parse":
    print(("b" if os.environ["TEST_DRIFT"] == "revision" and count > 1 else "a") * 40)
elif kind == "status" and os.environ["TEST_DRIFT"] == "dirty" and count > 1:
    print(" M tracked.go")
''')
                git.chmod(0o755)
                python = bindir / "python3"
                python.write_text("#!/bin/sh\nprintf 'fixture\\n'\n")
                python.chmod(0o755)
                go = bindir / "go"
                go.write_text(f"#!{sys.executable}\n" + '''import json, os, pathlib, sys
with open(os.environ["BUILD_LOG"], "a") as log:
    log.write(json.dumps(sys.argv[1:]) + "\\n")
out = pathlib.Path(sys.argv[sys.argv.index("-o") + 1])
out.write_text('#!/bin/sh\\nprintf called >> "$SEED_LOG"\\nexit 99\\n')
out.chmod(0o755)
''')
                go.chmod(0o755)
                counters = root / "git-counters"
                counters.mkdir()
                seeded = root / "seed-called"
                builds = root / "builds.jsonl"
                env = dict(os.environ, PATH=str(bindir) + os.pathsep + os.environ["PATH"],
                           TMPDIR=tmp, KVMC_TEST_BACKEND="json", GIT_COUNTER_DIR=str(counters),
                           TEST_DRIFT=drift, BUILD_LOG=str(builds), SEED_LOG=str(seeded))
                result = subprocess.run(["bash", str(fixture)], env=env, capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("changed", result.stderr)
                self.assertFalse(seeded.exists(), "fixture seeded after the candidate changed")
                rows = [json.loads(line) for line in builds.read_text().splitlines()]
                self.assertIn("-ldflags=-X main.GitSHA=" + "a" * 40, rows[-1])


class PublicationTests(unittest.TestCase):
    def test_latest_exact_candidate_must_succeed(self):
        spec = importlib.util.spec_from_file_location("ci_success", SCRIPT.parent / "assert-ci-success.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        good = {"headSha": "abc", "status": "completed", "conclusion": "success"}
        module.require_success([good], "abc")
        for rows in ([], [dict(good, headSha="old")], [dict(good, status="queued")],
                     [dict(good, conclusion="cancelled")], [dict(good, conclusion="skipped")],
                     [dict(good, conclusion="failure"), good]):
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                module.require_success(rows, "abc")

class CIWaitTests(unittest.TestCase):
    def run_gate(self, result, max_wait="1"):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            gh = root / "gh"
            gh.write_text("#!/bin/sh\nprintf '%s\\n' \"$FAKE_CI_RESULT\"\n")
            gh.chmod(0o755)
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"],
                       GITHUB_SHA="abc", MAX_WAIT_SECONDS=max_wait, POLL_SECONDS="1",
                       FAKE_CI_RESULT=json.dumps(result))
            return subprocess.run(["bash", str(SCRIPT.parent / "wait-github-ci.sh")],
                                  env=env, capture_output=True, text=True, timeout=5)

    def test_pending_times_out_and_failed_is_not_retried_as_success(self):
        pending = self.run_gate([])
        self.assertNotEqual(pending.returncode, 0)
        self.assertIn("did not pass within", pending.stderr)
        failed = self.run_gate([{"headSha": "abc", "status": "completed", "conclusion": "failure"}])
        self.assertNotEqual(failed.returncode, 0)
        self.assertNotIn("did not pass within", failed.stderr)

    def test_exact_success_passes_without_wait(self):
        result = self.run_gate([{"headSha": "abc", "status": "completed", "conclusion": "success"}])
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
