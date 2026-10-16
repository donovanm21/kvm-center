#!/usr/bin/env python3
"""Evidence-producing validation. Exit zero means only the selected gates passed.

This runner never contacts a hypervisor or a production service. Real-hypervisor
acceptance remains an explicit, separately authorized release blocker.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
from urllib.parse import urlparse

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ("harness", "format", "modules", "vet", "unit", "webui", "binaries")
GATES = SOURCE + ("mongo", "browser", "package-inputs", "container", "govulncheck", "npm-audit", "gitleaks", "hypervisor")
PROFILES = {"source": SOURCE, "mongo": ("mongo",), "browser": ("browser",), "packaging": ("package-inputs",), "release": GATES}
RELEASE_INPUTS = ("scripts/install.sh", "scripts/update-agent.sh", "scripts/install-kvmctl.sh",
                  "scripts/install-kvmctl.ps1", "scripts/libvirt-hooks-qemu.sh",
                  "kvm-tools/install.ps1", "skill/install.sh")


def go_test_evidence(text, prefix=None, forbid_skips=False):
    events = []
    for line in text.splitlines():
        try:
            events.append(json.loads(line))
        except (ValueError, TypeError):
            continue  # toolchain-download notices are not JSON test events
    tests = [e for e in events if e.get("Test") and (not prefix or e["Test"].startswith(prefix))]
    passed = {e["Test"] for e in tests if e.get("Action") == "pass"}
    skipped = {e["Test"] for e in tests if e.get("Action") == "skip"}
    failures = [e for e in events if e.get("Action") == "fail"]
    if not passed or failures or (forbid_skips and skipped):
        raise ValueError(f"test evidence incomplete: passed={len(passed)} skipped={len(skipped)} failures={len(failures)}")
    return {"passed": len(passed), "skipped": sorted(skipped)}


def browser_evidence(text):
    data = json.loads(text)
    stats = data.get("stats", {})
    expected = stats.get("expected", 0)
    # Retries/flaky/skip cannot silently become a release success.
    if not isinstance(expected, int) or expected < 1 or any(stats.get(k, 0) for k in ("unexpected", "skipped", "flaky")) or data.get("errors"):
        raise ValueError("browser evidence requires at least one pass and zero failed, skipped, flaky or global-error tests")
    return stats


def missing_release_inputs(root=ROOT):
    return [name for name in RELEASE_INPUTS if not (root / name).is_file() or not (root / name).stat().st_size]


def local_test_url(value, schemes):
    try:
        u = urlparse(value)
        port = u.port  # reject malformed/multiple-host ports rather than ignoring them
        return (u.scheme in schemes and u.hostname in ("localhost", "127.0.0.1", "::1")
                and u.username is None and u.password is None and "," not in u.netloc
                and (port is None or 1 <= port <= 65535) and u.path in ("", "/")
                and not u.fragment and u.query in (("", "directConnection=true") if u.scheme == "mongodb" else ("",)))
    except ValueError:
        return False


def source_identity():
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    status = subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT, text=True)
    # Identify actual tracked + new source bytes, including an uncommitted fix.
    files = subprocess.check_output(["git", "ls-files", "-co", "--exclude-standard", "-z"], cwd=ROOT).split(b"\0")
    digest = hashlib.sha256()
    for name in sorted(set(filter(None, files))):
        path = ROOT / os.fsdecode(name)
        digest.update(name + b"\0")
        digest.update(path.read_bytes() if path.is_file() else b"<deleted>")
    return {"revision": revision, "dirty": bool(status.strip()), "source_sha256": digest.hexdigest()}


class Runner:
    def __init__(self, profile, output, timeout=1800):
        self.profile, self.output, self.timeout = profile, output, timeout
        self.output.mkdir(parents=True, exist_ok=False)  # never overwrite evidence
        self.report = {"schema_version": 1, "started_at": datetime.now(timezone.utc).isoformat(),
                       "profile": profile, "source": source_identity(),
                       "scope": "Executed assertions only; see docs/validation.md for feature coverage and missing acceptance.",
                       "gates": {name: {"status": "not_run", "reason": "outside selected profile"} for name in GATES}}
        self.save()

    def save(self):
        selected = [self.report["gates"][name]["status"] for name in PROFILES[self.profile]]
        # A crash after the last command must not leave an aggregate pass before
        # the final source fingerprint has been checked and the run is complete.
        complete = bool(self.report.get("finished_at")) and self.report.get("source_unchanged") is True
        self.report["selected_gates_passed"] = complete and bool(selected) and all(s == "passed" for s in selected)
        self.report["release_gates_passed"] = complete and all(g["status"] == "passed" for g in self.report["gates"].values())
        tmp = self.output / "report.json.tmp"
        tmp.write_text(json.dumps(self.report, indent=2) + "\n")
        tmp.replace(self.output / "report.json")

    def command(self, name, argv, cwd=ROOT, env=None, validator=None):
        path = self.output / f"{name}.log"
        gate = self.report["gates"][name]
        gate.update(status="running", log=path.name)
        gate.pop("reason", None)
        self.save()
        print(f"→ {name}", flush=True)
        started = time.monotonic()
        try:
            with path.open("w") as log:
                # Separate process group lets timeout kill compiler/test children too.
                process = subprocess.Popen(argv, cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
                try:
                    code = process.wait(timeout=self.timeout)
                except (subprocess.TimeoutExpired, KeyboardInterrupt):
                    os.killpg(process.pid, signal.SIGTERM)
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.wait()
                    raise
            gate["exit_code"] = code
            if code:
                gate.update(status="failed", reason=f"command exited {code}; see {path.name}")
            else:
                if validator:
                    gate["evidence"] = validator(path.read_text(errors="replace"))
                gate["status"] = "passed"
        except FileNotFoundError as exc:
            gate.update(status="blocked", reason=f"required executable unavailable: {exc.filename}")
        except subprocess.TimeoutExpired:
            gate.update(status="failed", reason=f"exceeded {self.timeout}s timeout; process group stopped")
        except (ValueError, OSError) as exc:
            gate.update(status="failed", reason=str(exc))
        except KeyboardInterrupt:
            gate.update(status="failed", reason="interrupted; process group stopped")
            raise
        finally:
            gate["seconds"] = round(time.monotonic() - started, 2)
            self.save()
            print(f"  {name}: {gate['status']}", flush=True)

    def blocked(self, name, reason):
        self.report["gates"][name] = {"status": "blocked", "reason": reason}
        self.save()
        print(f"  {name}: blocked ({reason})", flush=True)

    def run_gate(self, name):
        if name == "harness":
            self.command(name, [sys.executable, "-m", "unittest", "discover", "-s", "scripts/tests", "-v"])
        elif name == "format":
            self.command(name, ["bash", "-c", 'set -euo pipefail; changed=$(git ls-files -co --exclude-standard "*.go" | sort -u | xargs gofmt -l); test -z "$changed" || { printf "%s\\n" "$changed"; exit 1; }'])
        elif name == "modules":
            self.command(name, ["bash", "-c", "set -euo pipefail; go mod tidy -diff; go mod verify"])
        elif name == "vet":
            self.command(name, ["go", "vet", "-mod=readonly", "./..."])
        elif name == "unit":
            self.command(name, ["go", "test", "-mod=readonly", "-race", "-count=1", "-timeout=10m", "-json", "./..."], validator=go_test_evidence)
        elif name == "webui":
            self.command(name, ["bash", "scripts/build-webui.sh"])
        elif name == "binaries":
            if self.report["gates"]["webui"]["status"] != "passed":
                self.blocked(name, "requires successful fresh webui build; stale embedded assets are not accepted")
            else:
                env = dict(os.environ, GIT_SHA=self.report["source"]["revision"])
                self.command(name, ["bash", "scripts/build-release.sh", str(self.output / "binaries")], env=env)
        elif name == "mongo":
            uri = os.environ.get("KVMC_TEST_MONGO_URI", "")
            if not local_test_url(uri, ("mongodb",)):
                self.blocked(name, "set KVMC_TEST_MONGO_URI to a disposable, unauthenticated loopback MongoDB; no production URI accepted")
            else:
                self.command(name, ["go", "test", "-mod=readonly", "-race", "-count=1", "-tags=integration", "-run=^TestMongoIntegration", "-timeout=5m", "-json", "./server"],
                             validator=lambda text: go_test_evidence(text, "TestMongoIntegration", True))
        elif name == "browser":
            url = os.environ.get("KVMC_DEMO_URL", "")
            if self.report["source"]["dirty"]:
                self.blocked(name, "requires a clean committed candidate; the server Git SHA cannot identify uncommitted source changes")
            elif not local_test_url(url, ("https", "http")) or os.environ.get("KVMC_DISPOSABLE_DEMO") != "1":
                self.blocked(name, "requires KVMC_DISPOSABLE_DEMO=1 and a loopback KVMC_DEMO_URL for a seeded disposable demo")
            elif not (ROOT / "demo/e2e/node_modules/@playwright/test/cli.js").is_file():
                self.blocked(name, "run npm ci --prefix demo/e2e and install Playwright chromium + webkit browsers first")
            else:
                backend = os.environ.get("KVMC_TEST_BACKEND", "mongo")
                if backend not in ("mongo", "json"):
                    self.blocked(name, "KVMC_TEST_BACKEND must be mongo or json")
                    return
                self.report["gates"][name]["backend"] = backend
                self.report["gates"][name]["inventory"] = "synthetic"
                env = dict(os.environ, CI="1", KVMC_EXPECTED_BACKEND=backend, KVMC_EXPECTED_SHA=self.report["source"]["revision"], PLAYWRIGHT_JSON_OUTPUT_FILE=str(self.output / "browser-results.json"))
                def check_browser(_):
                    return browser_evidence((self.output / "browser-results.json").read_text())
                self.command(name, ["node", "node_modules/@playwright/test/cli.js", "test", "--retries=0", "--reporter=json"], cwd=ROOT / "demo/e2e", env=env, validator=check_browser)
                # Preserve partial counts on a failed run without converting it
                # to a pass (e.g. Chromium passes, WebKit cannot launch).
                result_file = self.output / "browser-results.json"
                if result_file.is_file():
                    try:
                        self.report["gates"][name]["test_counts"] = json.loads(result_file.read_text()).get("stats", {})
                    except (ValueError, OSError):
                        pass  # command/validator already recorded failure
                    self.save()
        elif name == "package-inputs":
            missing = missing_release_inputs()
            if missing:
                self.blocked(name, "missing required tracked release sources: " + ", ".join(missing))
            else:
                self.command(name, ["git", "ls-files", "--error-unmatch", *RELEASE_INPUTS])
        elif name == "container":
            if self.report["gates"]["package-inputs"]["status"] != "passed":
                self.blocked(name, "release inputs incomplete; refusing to omit advertised downloads")
            else:
                # Local build only; never push or deploy. Only native image build,
                # not multi-arch runtime behavior, is established by this gate.
                self.command(name, ["docker", "build", "--build-arg", f"GIT_SHA={self.report['source']['revision']}", "-f", "Dockerfile.server", "-t", "kvm-center-validation:local", "."])
        elif name == "govulncheck":
            self.command(name, ["govulncheck", "./..."])
        elif name == "npm-audit":
            self.command(name, ["npm", "audit", "--audit-level=critical", "--omit=dev"], cwd=ROOT / "webui-react")
        elif name == "gitleaks":
            self.command(name, ["gitleaks", "detect", "--source", ".", "--no-banner", "--redact"])
        elif name == "hypervisor":
            self.blocked(name, "requires separately authorized disposable multi-host lab acceptance; synthetic agents/unit tests are not libvirt evidence. See docs/validation.md")

    def run(self):
        for name in PROFILES[self.profile]:
            self.run_gate(name)
        after = source_identity()
        self.report["source_unchanged"] = after == self.report["source"]
        if not self.report["source_unchanged"]:
            self.report["invalid_reason"] = "Source changed during validation; rerun against a frozen candidate."
        self.report["finished_at"] = datetime.now(timezone.utc).isoformat()
        self.save()
        print(f"Evidence: {self.output / 'report.json'}")
        return 0 if self.report["selected_gates_passed"] and self.report["source_unchanged"] else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", choices=PROFILES, default="source")
    parser.add_argument("--output", type=Path, help="new evidence directory (must not exist)")
    parser.add_argument("--timeout", type=int, default=1800, help="per-gate timeout in seconds")
    args = parser.parse_args()
    if args.timeout < 1:
        parser.error("--timeout must be positive")
    output = (args.output or ROOT / ".validation" / (datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S") + f"-{os.getpid()}")).resolve()
    try:
        return Runner(args.profile, output, args.timeout).run()
    except (OSError, subprocess.CalledProcessError) as exc:
        print(f"Validation blocked: {exc}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    sys.exit(main())
