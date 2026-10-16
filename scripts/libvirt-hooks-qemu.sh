#!/usr/bin/env bash
# Incoming migration only: prepare/begin also fires for ordinary VM starts.
# Keep stdout empty: libvirt treats migration-hook output as replacement XML.
# This compatibility hook denies an explicit missing grant (403). Transport
# failures and 5xx retain the legacy fail-open availability policy; this hook
# alone is not an authorization boundary. Native libvirt acceptance is required.
set -u
[[ "${2:-}" == migrate && "${3:-}" == begin ]] || exit 0
VM="${1:-}"
[[ -n "$VM" ]] || exit 1
if CODE=$(curl -sS --connect-timeout 1 --max-time 3 -o /dev/null -w '%{http_code}' \
    --get --data-urlencode "vm=$VM" http://127.0.0.1:8081/grant-active); then
    case "$CODE" in
        200) exit 0 ;;
        5??) logger -t kvm-center-hook "agent unavailable; legacy fail-open migration policy"; exit 0 ;;
        *) logger -t kvm-center-hook "migration rejected: agent HTTP $CODE"; exit 1 ;;
    esac
fi
logger -t kvm-center-hook "agent transport unavailable; legacy fail-open migration policy"
exit 0
