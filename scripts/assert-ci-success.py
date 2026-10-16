#!/usr/bin/env python3
"""Reject publication unless the latest push CI run passed for this exact SHA.

Input is `gh run list --workflow test.yml --event push --commit SHA
--json headSha,status,conclusion`. No matching run, queued, cancelled, skipped,
or a newer failed rerun must never be treated as success.
"""
import json
import sys


class PendingCI(ValueError):
    """CI may still start or finish within the caller's bounded wait."""


def require_success(runs, revision):
    if not isinstance(runs, list) or not runs:
        raise PendingCI("no push validation run for candidate yet")
    latest = runs[0]  # gh run list returns newest runs first
    if latest.get("headSha") != revision:
        raise ValueError("CI revision does not match publication candidate")
    if latest.get("status") in ("queued", "in_progress", "waiting", "requested", "pending"):
        raise PendingCI("candidate CI is still running")
    if latest.get("status") != "completed" or latest.get("conclusion") != "success":
        raise ValueError("latest candidate CI did not complete successfully; no publication permitted")


if __name__ == "__main__":
    try:
        if len(sys.argv) != 2:
            raise ValueError("usage: assert-ci-success.py <exact candidate SHA>")
        require_success(json.load(sys.stdin), sys.argv[1])
    except PendingCI as exc:
        print(f"Waiting for CI: {exc}", file=sys.stderr)
        sys.exit(2)
    except (ValueError, TypeError, AttributeError) as exc:
        print(f"Publication blocked: {exc}", file=sys.stderr)
        sys.exit(1)
    print("Exact-candidate automated CI passed; this does not establish real-hypervisor acceptance.")
