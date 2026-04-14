#!/bin/bash
# /etc/libvirt/hooks/qemu
#
# kvm-center migration-grant enforcement hook (v5.11.1+).
#
# libvirtd calls this script around VM lifecycle operations. We only
# care about the destination-side "prepare" phase of a migration —
# that's the moment libvirtd decides whether to accept an incoming
# migration. If the local kvm-center agent doesn't report an active
# grant for this VM, the migration is refused (exit != 0).
#
# Source-side "begin" is not enforced here — by definition the
# kvm-center server authorised the source because the operator used
# the control plane. Out-of-band `virsh migrate` from a shell on the
# source IS enforced, but on the destination end instead, which is
# where the protection actually matters (you can't accept a VM you
# weren't told to accept).
#
# If the local agent is unreachable we fail open with a log line so
# planned migrations aren't gated on the agent's health — grants
# remain on the cryptographic path, not a liveness path.
#
# Arguments libvirt passes:
#   $1 = guest name
#   $2 = operation  ("prepare" | "start" | "release" | "migrate" | …)
#   $3 = sub-op     ("begin" | "end" | "-" …)
#   $4 = extra arg
set -u

VM="${1:-}"
OP="${2:-}"
SUBOP="${3:-}"

if [[ "$OP" != "prepare" ]]; then
    exit 0
fi
# Only the destination side of a migration. libvirt runs "prepare"
# for normal starts too — we want to gate only when this is an
# incoming migration. The "begin" sub-op is how libvirt phrases
# migrate-prepare; a plain local start uses "start" as the op.
if [[ "$SUBOP" != "begin" ]]; then
    exit 0
fi

AGENT_URL="http://127.0.0.1:8081/grant-active?vm=${VM}"
RESP=$(curl -fsS --max-time 3 "$AGENT_URL" 2>&1) || {
    # Agent unreachable or 4xx — distinguish.
    CODE=$(curl -fso /dev/null -s -w '%{http_code}' --max-time 3 "$AGENT_URL" || echo 000)
    case "$CODE" in
        000|5*) logger -t kvm-center-hook "agent unreachable (HTTP $CODE) — allowing migration of $VM by default" ; exit 0 ;;
        403)    logger -t kvm-center-hook "REJECT migration of $VM: no active grant" >&2
                echo "kvm-center: no active migration grant for $VM" >&2
                exit 1 ;;
        *)      logger -t kvm-center-hook "agent HTTP $CODE for $VM — allowing"
                exit 0 ;;
    esac
}
logger -t kvm-center-hook "migration of $VM authorised via grant ($RESP)"
exit 0
