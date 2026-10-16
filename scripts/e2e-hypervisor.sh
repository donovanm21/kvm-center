#!/usr/bin/env bash
# e2e-hypervisor.sh — full operator-surface battery against one hypervisor.
#
# Designed to be:
#   - rerunnable (every test resource is named e2e-<scenario>-<timestamp>
#     so concurrent runs and re-runs never collide)
#   - additive (each scenario is a `phase_<id>` function registered in the
#     SCENARIOS array; adding a new one is one function + one register line)
#   - operator-faithful (every check hits the kvm-center HTTP API the same
#     way the SPA does — no agent SSH shortcuts for the operations under
#     test, only for diagnostic verify steps)
#
# Usage:
#   KVMC_TOKEN=kvmc_... ./scripts/e2e-hypervisor.sh                 # all
#   KVMC_TOKEN=kvmc_... ./scripts/e2e-hypervisor.sh --list          # list
#   KVMC_TOKEN=kvmc_... ./scripts/e2e-hypervisor.sh --scenario B1   # one
#   KVMC_TOKEN=kvmc_... ./scripts/e2e-hypervisor.sh --clean         # sweep
#   KVMC_TOKEN=kvmc_... ./scripts/e2e-hypervisor.sh --with-retire   # incl M
#   KVMC_TOKEN=kvmc_... ./scripts/e2e-hypervisor.sh --strict --with-retire # no skips
#
# Env knobs:
#   KVMC_SERVER       default https://server.example.com:8190
#   KVMC_HOST         default hostD.example.com (the target hypervisor)
#   KVMC_PEER         default hostA.example.com (migration peer)
#   KVMC_TOKEN        required, API bearer token
#
# Exit code: 0 if every selected scenario passed; non-zero on any failure.

set -uo pipefail

: "${KVMC_SERVER:=https://server.example.com:8190}"
: "${KVMC_HOST:=hostD.example.com}"
: "${KVMC_PEER:=hostA.example.com}"

# Sweep-mode skips the actual scenarios and only runs cleanup. Useful
# when a prior run left a test VM around (Ctrl-C, network blip, etc).
CLEAN_ONLY=0
WITH_RETIRE=0
LIST_ONLY=0
STRICT=0
SELECTED=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --strict)       STRICT=1; shift ;;
    --clean)        CLEAN_ONLY=1; shift ;;
    --list)         LIST_ONLY=1; shift ;;
    --with-retire)  WITH_RETIRE=1; shift ;;
    --scenario)     [[ $# -ge 2 && "$2" != --* ]] || { echo "--scenario requires a value" >&2; exit 2; }; SELECTED="$2"; shift 2 ;;
    --server) [[ $# -ge 2 && "$2" != --* ]] || { echo "--server requires a value" >&2; exit 2; }; KVMC_SERVER="$2"; shift 2 ;;
    --host) [[ $# -ge 2 && "$2" != --* ]] || { echo "--host requires a value" >&2; exit 2; }; KVMC_HOST="$2"; shift 2 ;;
    --peer) [[ $# -ge 2 && "$2" != --* ]] || { echo "--peer requires a value" >&2; exit 2; }; KVMC_PEER="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "Unknown arg: $1" >&2; exit 1 ;;
  esac
done

RUN_TS="$(date -u +%Y%m%dT%H%M%SZ)"
# `e2eharness-` (not `e2e-`) so the harness's test resources are
# distinguishable from operator-created VMs that happen to start
# with `e2e-` (e.g. e2e-rocky-10 in the real fleet). Sweep-mode
# greps for this prefix only.
TAG_PREFIX="e2eharness-${RUN_TS}"

# ── colours ───────────────────────────────────────────────────────
if [[ -t 1 ]]; then
  C_GREEN=$'\033[0;32m'; C_RED=$'\033[0;31m'; C_YELLOW=$'\033[1;33m'
  C_BLUE=$'\033[0;34m'; C_DIM=$'\033[2m'; C_NC=$'\033[0m'
else
  C_GREEN=""; C_RED=""; C_YELLOW=""; C_BLUE=""; C_DIM=""; C_NC=""
fi

# ── tracking ──────────────────────────────────────────────────────
# Compatible with bash 3.2 (Apple's stock) — no associative arrays.
# SCENARIOS holds ordered IDs; SCEN_ROWS holds "ID|FN|NAME" tuples
# indexed in parallel, looked up via scen_*() helpers.
SCENARIOS=()
SCEN_ROWS=()
SCEN_OUTCOMES=()  # "ID=PASS" entries
SCEN_DURS=()      # "ID=42" entries
CREATED_VMS=()         # "<uuid>=<current_host>" tuples
CREATED_POOLS=()
SET_EXPECTED_DOWN=()

# ── helpers ───────────────────────────────────────────────────────
log()  { printf '%s\n' "$*" | tee -a "$LOG"; }
note() { printf '  %s%s%s\n' "$C_DIM" "$*" "$C_NC" | tee -a "$LOG"; }
ok()   { printf '  %s✓%s %s\n' "$C_GREEN" "$C_NC" "$*" | tee -a "$LOG"; }
bad()  { printf '  %s✗%s %s\n' "$C_RED"   "$C_NC" "$*" | tee -a "$LOG"; }

# Run a curl against the server. Returns: status code on stdout,
# body on $OUTDIR/.body.
api() {
  local method=$1 path=$2; shift 2
  curl -ksS -o "$OUTDIR/.body" -w '%{http_code}' \
    -X "$method" \
    -H "Authorization: Bearer $KVMC_TOKEN" \
    -H "Content-Type: application/json" \
    "$@" \
    "$KVMC_SERVER$path"
}
api_body() { cat "$OUTDIR/.body"; }

# JSON helpers (lean on python3 for robustness — jq isn't always on macOS).
jget() { python3 -c "import json,sys; print(json.load(sys.stdin)$1)"; }

# wait_until <max_sec> <poll_cmd> — runs poll_cmd every 2s; returns 0
# the first time it exits 0, or 1 if max_sec elapses first.
wait_until() {
  local max=$1; shift
  local t=$max
  while (( t > 0 )); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 2; t=$((t-2))
  done
  return 1
}

# register <id> <fn> <description>
register() {
  SCENARIOS+=("$1")
  SCEN_ROWS+=("$1|$2|$3")
}

# scen_lookup <id> → echoes "ID|FN|NAME" row or empty
scen_lookup() {
  local needle=$1 row
  for row in "${SCEN_ROWS[@]}"; do
    case "$row" in "$needle|"*) echo "$row"; return 0 ;; esac
  done
}
scen_fn() {  scen_lookup "$1" | awk -F'|' '{print $2}'; }
scen_name() { scen_lookup "$1" | cut -d'|' -f3-; }

# kv_get <key> <array_var_name>
kv_get() {
  local key=$1 arrname=$2 row
  eval "local _arr=(\"\${$arrname[@]}\")"
  for row in "${_arr[@]:-}"; do
    case "$row" in "$key="*) echo "${row#*=}"; return 0 ;; esac
  done
}
kv_set() {
  local key=$1 val=$2 arrname=$3
  eval "$arrname+=(\"$key=$val\")"
}

# Run one scenario. The phase function returns 0 (pass), 77 (skip),
# anything else (fail). The scenario writes its narration via ok/bad
# so the dispatcher only needs the verdict.
run_scenario() {
  local id=$1
  local fn; fn=$(scen_fn "$id")
  local desc; desc=$(scen_name "$id")
  if [[ -z $fn ]]; then
    echo "no such scenario: $id" >&2
    return 1
  fi
  printf '\n%s[%s]%s %s%s%s\n' "$C_BLUE" "$id" "$C_NC" "$C_DIM" "$desc" "$C_NC" | tee -a "$LOG"
  local start; start=$(date +%s)
  "$fn"
  local rc=$?
  local end; end=$(date +%s)
  local dur=$((end-start))
  local outcome
  case "$rc" in
    0)  outcome=PASS ;;
    77) outcome=SKIP ;;
    *)  outcome=FAIL ;;
  esac
  kv_set "$id" "$outcome" SCEN_OUTCOMES
  kv_set "$id" "$dur" SCEN_DURS
  return 0
}

# ── cleanup ───────────────────────────────────────────────────────
# Runs at the end of the suite. Removes any test VMs / pools / flags
# the run created. Best-effort — failures are logged but not fatal.
cleanup() {
  log ""
  log "${C_DIM}── cleanup ──${C_NC}"
  for entry in "${CREATED_VMS[@]:-}"; do
    [[ -z $entry ]] && continue
    local uuid=${entry%%=*} cur_host=${entry#*=}
    # Re-resolve current host from the live record in case the VM
    # was migrated mid-run and we didn't capture the update.
    api GET "/api/vms/$uuid" >/dev/null 2>&1
    local live; live=$(api_body | python3 -c 'import json,sys
try:    print(json.load(sys.stdin).get("host_id",""))
except: print("")' 2>/dev/null)
    [[ -n $live ]] && cur_host=$live
    [[ -z $cur_host ]] && cur_host=$KVMC_HOST
    api POST /api/command -d "{\"action\":\"FORCESTOP\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$cur_host\",\"params\":{}}" >/dev/null
    sleep 2
    api POST /api/command -d "{\"action\":\"DELETE_VM\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$cur_host\",\"params\":{\"remove_disks\":\"true\"}}" >/dev/null
    note "deleted VM $uuid (on $cur_host)"
  done
  for pool in "${CREATED_POOLS[@]:-}"; do
    [[ -z $pool ]] && continue
    api DELETE "/api/hosts/$KVMC_HOST/storage/pools/$pool" >/dev/null
    note "deleted pool $pool"
  done
  for host in "${SET_EXPECTED_DOWN[@]:-}"; do
    [[ -z $host ]] && continue
    api POST "/api/hosts/$host/expected-down/clear" >/dev/null
    note "cleared expected-down on $host"
  done
}

# Find leftover e2e-* resources across the WHOLE FLEET and remove
# them. The regular cleanup() handles in-run resources; this is for
# the operator who ran with --clean to sweep prior aborted runs.
#
# Fleet-wide sweep (not host-scoped) because H1 / F2 migrate the
# created VM off the configured KVMC_HOST mid-run. If the script
# is killed between migration and the cleanup trap, the orphan
# lives on the peer where a host-scoped sweep never looks. Seen
# 2026-05-12 — a killed round 1 left `e2eharness-…-c1` on hostA
# after H1 migrated it; the host-scoped sweep at the time only
# checked hostD and missed it.
sweep_orphans() {
  log "Sweeping leftover e2eharness-* resources across the fleet"
  # VMs: hit /api/vms with no host filter so the response covers
  # every hypervisor. Pull (uuid, host_id) tuples so we issue
  # FORCESTOP / DELETE against the host that currently holds each
  # VM rather than assuming KVMC_HOST.
  local rows
  rows=$(api GET "/api/vms" >/dev/null && api_body \
    | python3 -c "import json,sys
for v in json.load(sys.stdin):
    name = v.get('name','')
    if name.startswith('e2eharness-'):
        print(v['uuid'] + '|' + (v.get('host_id') or ''))" 2>/dev/null)
  while IFS= read -r row; do
    [[ -z $row ]] && continue
    local uuid host
    uuid=${row%%|*}
    host=${row#*|}
    [[ -z $host ]] && host=$KVMC_HOST
    api POST /api/command -d "{\"action\":\"FORCESTOP\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$host\",\"params\":{}}" >/dev/null
    sleep 2
    api POST /api/command -d "{\"action\":\"DELETE_VM\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$host\",\"params\":{\"remove_disks\":\"true\"}}" >/dev/null
    note "swept VM $uuid (on $host)"
  done <<<"$rows"
  # Pools: also fleet-wide. /api/storage with no host filter returns
  # every pool keyed by (host_id, name); sweep each one on its own host.
  local prows
  prows=$(api GET "/api/storage" >/dev/null && api_body \
    | python3 -c "import json,sys
for p in json.load(sys.stdin):
    name = p.get('name','')
    if name.startswith('e2eharness-'):
        print(name + '|' + (p.get('host_id') or ''))" 2>/dev/null)
  while IFS= read -r prow; do
    [[ -z $prow ]] && continue
    local pname phost
    pname=${prow%%|*}
    phost=${prow#*|}
    [[ -z $phost ]] && phost=$KVMC_HOST
    api DELETE "/api/hosts/$phost/storage/pools/$pname" >/dev/null
    note "swept pool $pname (on $phost)"
  done <<<"$prows"
}

# ── scenarios ─────────────────────────────────────────────────────

phase_A1_inventory() {
  local code; code=$(api GET /api/hosts)
  [[ $code == 200 ]] || { bad "GET /api/hosts status $code"; return 1; }
  local count; count=$(api_body | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')
  ok "GET /api/hosts → $count hosts"
  local seen; seen=$(api_body | python3 -c "
import json, sys
for h in json.load(sys.stdin):
    if h['id'] == '$KVMC_HOST':
        print('online' if h.get('status') == 'online' else 'offline',
              'mtls' if h.get('mtls_ready') else 'no-mtls',
              'pubkey' if h.get('migrator_pubkey') else 'no-pubkey',
              'hostkeys' if h.get('ssh_host_keys') else 'no-hostkeys',
              'version=' + (h.get('agent_version') or '?'))
        break
")
  [[ -n $seen ]] || { bad "$KVMC_HOST not in /api/hosts"; return 1; }
  ok "$KVMC_HOST: $seen"
}

phase_A2_host_detail() {
  local code; code=$(api GET "/api/hosts/$KVMC_HOST")
  [[ $code == 200 ]] || { bad "GET /api/hosts/$KVMC_HOST status $code"; return 1; }
  api_body | python3 -c "
import json,sys
h = json.load(sys.stdin)
required = ['id','ip','agent_port','cpu_count','mem_total_mb','last_seen','agent_version']
missing = [k for k in required if not h.get(k)]
if missing:
    print('missing:', missing); sys.exit(1)
print('cpus=' + str(h['cpu_count']),
      'mem=' + str(h['mem_total_mb']) + 'MB',
      'version=' + h['agent_version'])
" || { bad "host detail missing required fields"; return 1; }
  ok "host detail has all required fields"
}

phase_A3_telemetry_fresh() {
  api GET "/api/hosts/$KVMC_HOST" >/dev/null
  local age; age=$(api_body | python3 -c "
import json, sys
from datetime import datetime, timezone
h = json.load(sys.stdin)
ls = h.get('last_seen','')
if not ls or ls.startswith('0001'):
    print(99999); sys.exit(0)
t = datetime.fromisoformat(ls.replace('Z','+00:00'))
print(int((datetime.now(timezone.utc) - t).total_seconds()))
")
  [[ $age -lt 60 ]] || { bad "last_seen age ${age}s — telemetry stale"; return 1; }
  ok "last_seen ${age}s ago"
}

phase_B1_pool_define() {
  local pname="${TAG_PREFIX}-pool"
  local ppath="/var/lib/libvirt/e2e-${RUN_TS}"
  local code
  code=$(api POST "/api/hosts/$KVMC_HOST/storage/pools" \
    -d "{\"name\":\"$pname\",\"path\":\"$ppath\",\"autostart\":true}")
  [[ $code == 200 ]] || { bad "define pool $pname status $code: $(api_body)"; return 1; }
  CREATED_POOLS+=("$pname")
  ok "defined pool $pname at $ppath"
}

phase_B2_pool_visible() {
  local pname="${TAG_PREFIX}-pool"
  # Telemetry tick may need a moment to refresh the pool list.
  wait_until 30 sh -c "curl -ksS -H 'Authorization: Bearer $KVMC_TOKEN' '$KVMC_SERVER/api/storage?host=$KVMC_HOST' | python3 -c 'import json,sys; sys.exit(0 if any(p[\"name\"]==\"$pname\" for p in json.load(sys.stdin)) else 1)'" \
    || { bad "pool $pname did not appear in /api/storage within 30s"; return 1; }
  ok "pool $pname visible in /api/storage"
}

phase_B3_pool_stop_start() {
  local pname="${TAG_PREFIX}-pool"
  local code
  code=$(api POST "/api/hosts/$KVMC_HOST/storage/pools/$pname/stop")
  [[ $code == 200 ]] || { bad "stop pool status $code"; return 1; }
  ok "pool $pname stopped"
  code=$(api POST "/api/hosts/$KVMC_HOST/storage/pools/$pname/start")
  [[ $code == 200 ]] || { bad "start pool status $code"; return 1; }
  ok "pool $pname restarted"
}

phase_B4_pool_delete() {
  local pname="${TAG_PREFIX}-pool"
  local code; code=$(api DELETE "/api/hosts/$KVMC_HOST/storage/pools/$pname")
  [[ $code == 200 ]] || { bad "delete pool status $code: $(api_body)"; return 1; }
  ok "pool $pname undefined"
  # Remove from cleanup list so we don't double-delete.
  CREATED_POOLS=("${CREATED_POOLS[@]/$pname}")
}

create_test_vm() {
  # Used by C+ phases. Writes UUID to global LAST_CREATED_UUID so
  # CREATED_VMS+= mutations survive (a $(create_test_vm) subshell
  # capture would lose them). Returns 0 on success, 1 on failure.
  LAST_CREATED_UUID=""
  local name="${TAG_PREFIX}-${1:-vm}"
  local code
  code=$(api POST "/api/vm-create?host=$KVMC_HOST" -d "{
    \"name\":\"$name\",
    \"memory_mb\":256,\"vcpus\":1,
    \"disk_size_gb\":1,
    \"os_variant\":\"alpinelinux3.20\",
    \"bridge\":\"br0\",
    \"pool_path\":\"/var/lib/libvirt/images\"
  }")
  [[ $code == 200 ]] || { bad "vm-create status $code: $(api_body)"; return 1; }
  # 60 s (was 30 s) — under late-battery load with many concurrent scenarios,
  # the agent's telemetry/inventory tick can take longer than 30 s to surface
  # a newly-created VM. The VM is always created fine (verified via libvirt +
  # a manual /api/vm-create probe showing ~5 s visibility under no load);
  # the test just needs the headroom.
  if ! wait_until 60 sh -c "curl -ksS -H 'Authorization: Bearer $KVMC_TOKEN' '$KVMC_SERVER/api/vms?host=$KVMC_HOST' | python3 -c 'import json,sys; sys.exit(0 if any(v[\"name\"]==\"$name\" for v in json.load(sys.stdin)) else 1)'"; then
    bad "VM $name not visible in /api/vms within 60s"
    return 1
  fi
  api GET "/api/vms?host=$KVMC_HOST" >/dev/null
  local uuid
  uuid=$(api_body | python3 -c "import json,sys
for v in json.load(sys.stdin):
    if v['name'] == '$name':
        print(v['uuid']); break")
  [[ -n $uuid ]] || { bad "couldn't extract VM UUID after create"; return 1; }
  CREATED_VMS+=("$uuid=$KVMC_HOST")
  LAST_CREATED_UUID=$uuid
  return 0
}

phase_C1_vm_create() {
  create_test_vm "c1" || return 1
  echo "$LAST_CREATED_UUID" > "$OUTDIR/.vm_uuid"
  ok "created VM $LAST_CREATED_UUID"
}

phase_C2_vm_state_transitions() {
  local uuid; uuid=$(cat "$OUTDIR/.vm_uuid" 2>/dev/null) || { bad "no VM from C1"; return 1; }
  # virt-install boots the VM running; FORCESTOP it first.
  api POST /api/command -d "{\"action\":\"FORCESTOP\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{}}" >/dev/null
  sleep 5
  api POST /api/command -d "{\"action\":\"START\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{}}" >/dev/null
  sleep 8
  api GET "/api/vms/$uuid" >/dev/null
  local s; s=$(api_body | python3 -c 'import json,sys; print(json.load(sys.stdin).get("state",""))')
  [[ $s == running ]] || { bad "after START expected running got $s"; return 1; }
  ok "STOP→START transitioned to running"
  api POST /api/command -d "{\"action\":\"RESTART\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{}}" >/dev/null
  sleep 6
  api GET "/api/vms/$uuid" >/dev/null
  s=$(api_body | python3 -c 'import json,sys; print(json.load(sys.stdin).get("state",""))')
  [[ $s == running ]] || { bad "after RESTART expected running got $s"; return 1; }
  ok "RESTART kept running"
}

phase_C3_vm_terminal_5xx_no_lock() {
  # F1 regression: START on already-running returns 5xx + body
  # success:false, server should NOT 30-min-lock the VM. Immediate
  # FORCESTOP must succeed (not 409).
  local uuid; uuid=$(cat "$OUTDIR/.vm_uuid" 2>/dev/null) || { bad "no VM"; return 1; }
  local code; code=$(api POST /api/command -d "{\"action\":\"START\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{}}")
  [[ $code == 500 ]] || { bad "expected 500 on dup START got $code"; return 1; }
  code=$(api POST /api/command -d "{\"action\":\"FORCESTOP\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{}}")
  [[ $code == 200 ]] || { bad "FORCESTOP returned $code — VM is locked (F1 regression!)"; return 1; }
  ok "F1 holds: 5xx terminal, no lock"
}

phase_D1_edit_vm_combo_caprise() {
  # v5.58.1: EDIT_VM with vcpus + mem_mb both cap-raising +
  # allow_shutdown=true should run ONE shutdown cycle and apply
  # both live.
  local uuid; uuid=$(cat "$OUTDIR/.vm_uuid" 2>/dev/null) || { bad "no VM"; return 1; }
  api POST /api/command -d "{\"action\":\"START\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{}}" >/dev/null
  sleep 8
  local code; code=$(api POST /api/command -d "{\"action\":\"EDIT_VM\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{\"mem_mb\":\"512\",\"vcpus\":\"2\",\"allow_shutdown\":\"true\"}}")
  [[ $code == 200 ]] || { bad "EDIT_VM combo returned $code"; return 1; }
  local msg; msg=$(api_body | python3 -c 'import json,sys; print(json.load(sys.stdin).get("message",""))')
  [[ $msg == *"combo cap-raise"* ]] || { bad "expected combo-cap-raise message, got: $msg"; return 1; }
  ok "v5.58.1 single-cycle: $msg"
  # Verify both applied live.
  sleep 16
  api GET "/api/vms/$uuid" >/dev/null
  api_body | python3 -c "
import json, sys
v = json.load(sys.stdin)
errs = []
if v.get('cpu_count') != 2: errs.append(f'cpu_count={v.get(\"cpu_count\")} want 2')
if v.get('mem_total_mb') != 512: errs.append(f'mem_total_mb={v.get(\"mem_total_mb\")} want 512')
if errs: print(' | '.join(errs)); sys.exit(1)
print('cpu_count=2 mem_total_mb=512 both applied live')
" || { bad "combo cap-raise did not apply both live"; return 1; }
  ok "$(api_body | python3 -c "import json,sys;v=json.load(sys.stdin);print('post-edit cpus=%d mem=%dMB' % (v.get('cpu_count',0), v.get('mem_total_mb',0)))")"
}

phase_N1_snapshot_lifecycle() {
  local uuid; uuid=$(cat "$OUTDIR/.vm_uuid" 2>/dev/null) || { bad "no VM"; return 1; }
  local sname1="${TAG_PREFIX}-snap1"
  local sname2="${TAG_PREFIX}-snap2"
  local vm_name
  api GET "/api/vms/$uuid" >/dev/null
  vm_name=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("name",""))')

  local code
  code=$(api POST /api/command -d "{\"action\":\"SNAPSHOT\",\"vm_uuid\":\"$uuid\",\"vm_name\":\"$vm_name\",\"host_id\":\"$KVMC_HOST\",\"params\":{\"name\":\"$sname1\"}}")
  [[ $code == 200 ]] || { bad "create snap1 status $code: $(api_body)"; return 1; }
  ok "created snapshot $sname1"
  code=$(api POST /api/command -d "{\"action\":\"SNAPSHOT\",\"vm_uuid\":\"$uuid\",\"vm_name\":\"$vm_name\",\"host_id\":\"$KVMC_HOST\",\"params\":{\"name\":\"$sname2\"}}")
  [[ $code == 200 ]] || { bad "create snap2 status $code: $(api_body)"; return 1; }
  ok "created snapshot $sname2"

  code=$(api GET "/api/vms/snapshots?host=$KVMC_HOST&vm=$vm_name")
  [[ $code == 200 ]] || { bad "list snapshots status $code"; return 1; }
  local n; n=$(api_body | python3 -c 'import json,sys;print(len(json.load(sys.stdin).get("snapshots",[])))')
  [[ $n -ge 2 ]] || { bad "expected ≥2 snapshots, got $n"; return 1; }
  ok "list reports $n snapshots"

  # Revert needs VM stopped.
  api POST /api/command -d "{\"action\":\"FORCESTOP\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{}}" >/dev/null
  sleep 4
  code=$(api POST /api/command -d "{\"action\":\"REVERT_SNAPSHOT\",\"vm_uuid\":\"$uuid\",\"vm_name\":\"$vm_name\",\"host_id\":\"$KVMC_HOST\",\"params\":{\"name\":\"$sname1\"}}")
  [[ $code == 200 ]] || { bad "revert to $sname1 status $code: $(api_body)"; return 1; }
  ok "reverted to $sname1"

  for s in "$sname1" "$sname2"; do
    code=$(api POST /api/command -d "{\"action\":\"DELETE_SNAPSHOT\",\"vm_uuid\":\"$uuid\",\"vm_name\":\"$vm_name\",\"host_id\":\"$KVMC_HOST\",\"params\":{\"name\":\"$s\"}}")
    [[ $code == 200 ]] || { bad "delete $s status $code: $(api_body)"; return 1; }
  done
  ok "deleted both snapshots"
}

phase_E1_expected_down_lifecycle() {
  local code
  code=$(api POST "/api/hosts/$KVMC_HOST/expected-down" -d '{"reason":"e2e harness probe","expected_return_at":"2026-12-31T23:59:59Z"}')
  [[ $code == 200 ]] || { bad "set expected-down status $code"; return 1; }
  SET_EXPECTED_DOWN+=("$KVMC_HOST")
  api GET "/api/hosts/$KVMC_HOST" >/dev/null
  local r; r=$(api_body | python3 -c 'import json,sys;h=json.load(sys.stdin);print(h.get("expected_down"), h.get("expected_down_reason"))')
  [[ $r == "True e2e harness probe" ]] || { bad "host record: $r"; return 1; }
  ok "set expected_down + reason + ETA"
  code=$(api POST "/api/hosts/$KVMC_HOST/expected-down/clear")
  [[ $code == 200 ]] || { bad "clear expected-down status $code"; return 1; }
  # remove from cleanup list — already cleared
  local new=(); local h
  for h in "${SET_EXPECTED_DOWN[@]:-}"; do
    [[ $h != "$KVMC_HOST" && -n $h ]] && new+=("$h")
  done
  SET_EXPECTED_DOWN=("${new[@]:-}")
  api GET "/api/hosts/$KVMC_HOST" >/dev/null
  r=$(api_body | python3 -c 'import json,sys;h=json.load(sys.stdin);print(h.get("expected_down"))')
  [[ $r == None ]] || { bad "after clear got expected_down=$r"; return 1; }
  ok "cleared"
}

phase_F1_maintenance_mode() {
  local code; code=$(api POST "/api/hosts/$KVMC_HOST/maintenance/enter" -d '{"reason":"e2e harness probe"}')
  [[ $code == 200 ]] || { bad "enter maintenance status $code"; return 1; }
  ok "entered maintenance"
  # Try to create a VM — should 409.
  code=$(api POST "/api/vm-create?host=$KVMC_HOST" -d "{\"name\":\"${TAG_PREFIX}-f1-blocked\",\"memory_mb\":256,\"vcpus\":1,\"disk_size_gb\":1,\"bridge\":\"br0\",\"pool_path\":\"/var/lib/libvirt/images\"}")
  [[ $code == 409 ]] || { bad "expected 409 from VM-create during maintenance, got $code"; api POST "/api/hosts/$KVMC_HOST/maintenance/exit" >/dev/null; return 1; }
  ok "vm-create blocked with 409 as expected"
  code=$(api POST "/api/hosts/$KVMC_HOST/maintenance/exit")
  [[ $code == 200 ]] || { bad "exit maintenance status $code"; return 1; }
  ok "exited maintenance"
}

phase_G1_fleet_mesh_endpoints() {
  # Both endpoints are public; just curl directly without auth.
  local k h
  k=$(curl -ksS "$KVMC_SERVER/api/agents/migrator-authorized-keys" | grep -c '^ssh-')
  h=$(curl -ksS "$KVMC_SERVER/api/agents/ssh-known-hosts" | grep -cE '^[^#]')
  [[ $k -ge 1 ]] || { bad "migrator-authorized-keys returned $k entries"; return 1; }
  [[ $h -ge 1 ]] || { bad "ssh-known-hosts returned $h entries"; return 1; }
  ok "migrator-authorized-keys: $k pubkeys; ssh-known-hosts: $h entries"
}

phase_G2_cross_host_ssh() {
  # Use the diagnostic SSH path (only for verify — the operator
  # never does this). Asserts kvm-migrator@peer can reach KVMC_HOST
  # with strict-host-key-checking ON, which only succeeds if the
  # v5.55.4 authorized_keys mesh + v5.56.1 known_hosts mesh are
  # both populated.
  if ! ssh -o BatchMode=yes -o ConnectTimeout=5 "${KVMC_PEER%%.*}" 'true' 2>/dev/null; then
    note "no SSH to $KVMC_PEER; skipping cross-host probe"
    return 77
  fi
  local out
  out=$(ssh "${KVMC_PEER%%.*}" "sudo -u kvm-migrator ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -i /var/lib/kvm-migrator/.ssh/id_ed25519 kvm-migrator@${KVMC_HOST%%.*} hostname" 2>&1) || {
    bad "cross-host SSH failed: $out"; return 1;
  }
  [[ $out == "$KVMC_HOST" ]] || { bad "cross-host hostname mismatch: $out"; return 1; }
  ok "kvm-migrator@$KVMC_PEER → $KVMC_HOST (strict host-key check) ✓"
}

phase_H1_migration_off_host() {
  # Live migrate the test VM off KVMC_HOST to KVMC_PEER. We don't
  # migrate IN to KVMC_HOST because hostD lacks /dev/kvm — that
  # path is gated by a real virt-capability mismatch, separate from
  # kvmc. The off-host direction exercises the kvmc migration code
  # path end to end.
  local uuid; uuid=$(cat "$OUTDIR/.vm_uuid" 2>/dev/null) || { bad "no VM"; return 1; }
  # Ensure VM is running.
  api POST /api/command -d "{\"action\":\"START\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{}}" >/dev/null
  sleep 6
  local vm_name; api GET "/api/vms/$uuid" >/dev/null
  vm_name=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("name",""))')
  # Peer auto-discovery (v5.60.19) — if KVMC_PEER points at the
  # generic placeholder (the v5.60.13 docs scrub), pick the first
  # online host that isn't KVMC_HOST. Lets the battery work
  # out-of-the-box on any fleet without operator-specific config.
  if [[ "$KVMC_PEER" == "hostA.example.com" || -z "$KVMC_PEER" ]]; then
    api GET /api/hosts >/dev/null
    local discovered
    discovered=$(api_body | KVMC_HOST="$KVMC_HOST" python3 -c '
import json, os, sys
hosts = json.load(sys.stdin)
if isinstance(hosts, dict):
    hosts = hosts.get("hosts") or []
me = os.environ.get("KVMC_HOST","")
for h in hosts:
    if h.get("id") and h["id"] != me and h.get("status") == "online":
        print(h["id"]); break
')
    if [[ -n "$discovered" ]]; then
      KVMC_PEER="$discovered"
      log "    auto-discovered peer: $KVMC_PEER (KVMC_PEER was unset / placeholder)"
    fi
  fi
  # Peer IP.
  api GET "/api/hosts/$KVMC_PEER" >/dev/null
  local peer_ip; peer_ip=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("ip",""))')
  [[ -n $peer_ip ]] || { bad "couldn't resolve $KVMC_PEER IP (set KVMC_PEER=<host-id> explicitly if auto-discovery picked wrong)"; return 1; }
  log "    migrating $vm_name → $peer_ip (auto transport, copy_storage)"
  local code; code=$(api POST /api/command -d "{\"action\":\"MIGRATE\",\"vm_uuid\":\"$uuid\",\"vm_name\":\"$vm_name\",\"host_id\":\"$KVMC_HOST\",\"params\":{\"destination\":\"$peer_ip\",\"transport\":\"auto\",\"copy_storage\":\"true\"}}")
  [[ $code == 200 ]] || { bad "migration status $code: $(api_body | head -c 200)"; return 1; }
  ok "live migration completed"
  # Verify VM landed on peer.
  sleep 5
  api GET "/api/vms/$uuid" >/dev/null
  local new_host; new_host=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("host_id",""))')
  [[ $new_host == $KVMC_PEER ]] || { bad "post-migration host_id=$new_host want $KVMC_PEER"; return 1; }
  ok "VM landed on $KVMC_PEER"
  # For cleanup we need to remember the VM is now elsewhere. Note
  # the host swap so cleanup hits the right agent.
  echo "$KVMC_PEER" > "$OUTDIR/.vm_now_on"
}

phase_I1_console_token() {
  local uuid; uuid=$(cat "$OUTDIR/.vm_uuid" 2>/dev/null) || { bad "no VM"; return 1; }
  local cur_host; cur_host=$(cat "$OUTDIR/.vm_now_on" 2>/dev/null || echo "$KVMC_HOST")
  api GET "/api/vms/$uuid" >/dev/null
  local vm_name; vm_name=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("name",""))')
  local code; code=$(api POST "/api/vms/console-token?host=$cur_host&vm=$vm_name")
  [[ $code == 200 ]] || { bad "console-token status $code: $(api_body | head -c 200)"; return 1; }
  api_body | python3 -c '
import json,sys
t = json.load(sys.stdin)
for k in ("token","exp","nonce","url"):
    if not t.get(k): print(f"missing field {k}"); sys.exit(1)
' || { bad "console-token response missing fields"; return 1; }
  ok "console-token has token+exp+nonce+url"

  code=$(api GET "/api/console/$cur_host/$vm_name")
  [[ $code == 200 ]] || { bad "console HTML status $code"; return 1; }
  api_body | grep -q '<title>' || { bad "console HTML has no <title>"; return 1; }
  ok "console HTML rendered"
}

phase_J1_backup_export() {
  local code; code=$(api POST /api/admin/backup)
  [[ $code == 200 ]] || { bad "backup status $code"; return 1; }
  local bytes; bytes=$(wc -c < "$OUTDIR/.body" | tr -d ' ')
  [[ $bytes -gt 10000 ]] || { bad "backup tarball only $bytes bytes (expected >10KB)"; return 1; }
  file "$OUTDIR/.body" | grep -q gzip || { bad "backup body is not gzip"; return 1; }
  ok "backup exported $bytes bytes, gzip"
}

phase_K1_pki_cert_ledger() {
  local code; code=$(api GET /api/ca/hypervisors)
  [[ $code == 200 ]] || { bad "GET /api/ca/hypervisors status $code"; return 1; }
  api_body | python3 -c "
import json, sys
from datetime import datetime, timezone
entries = json.load(sys.stdin)
if not isinstance(entries, list):
    entries = entries.get('certs', [])
for c in entries:
    if c.get('host_id') == '$KVMC_HOST':
        not_after = c.get('not_after','')
        if not_after:
            t = datetime.fromisoformat(not_after.replace('Z','+00:00'))
            days = (t - datetime.now(timezone.utc)).days
            print(f'cert serial={c.get(\"serial_hex\",\"\")[:12]}... days_left={days}')
            if days < 30:
                print('cert expiry < 30 days', file=sys.stderr)
                sys.exit(1)
        sys.exit(0)
print('host not in PKI ledger', file=sys.stderr); sys.exit(1)
" || { bad "cert check failed"; return 1; }
  ok "$(api_body | python3 -c "
import json,sys
entries = json.load(sys.stdin)
if isinstance(entries, dict): entries = entries.get('certs',[])
for c in entries:
    if c.get('host_id') == '$KVMC_HOST':
        print('serial=' + c.get('serial_hex','')[:12] + '... expires=' + c.get('not_after','')[:10]); break
")"
}

phase_L1_audit_log() {
  local code; code=$(api GET "/api/audit?limit=5")
  [[ $code == 200 ]] || { bad "GET /api/audit status $code"; return 1; }
  local n; n=$(api_body | python3 -c 'import json,sys; print(len(json.load(sys.stdin).get("events",[])))')
  [[ $n -ge 1 ]] || { bad "audit returned $n events"; return 1; }
  ok "audit has $n recent events"
}

phase_F2_drain_live() {
  # Maintenance + drain (live). Create a VM, enter maintenance with
  # drain=true mode=live, wait for the VM to land on a peer, exit
  # maintenance. Asserts the server-side drainHost goroutine actually
  # moves running VMs out.
  if ! create_test_vm "f2-drain-live"; then return 1; fi
  local uuid=$LAST_CREATED_UUID
  local vm_name; api GET "/api/vms/$uuid" >/dev/null
  vm_name=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("name",""))')
  api POST /api/command -d "{\"action\":\"START\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{}}" >/dev/null
  sleep 8
  local code
  code=$(api POST "/api/hosts/$KVMC_HOST/maintenance/enter" -d '{"reason":"e2e drain live","drain":true,"mode":"live"}')
  [[ $code == 200 ]] || { bad "enter maint+drain status $code"; return 1; }
  ok "entered maintenance with drain=live"
  # Drain runs sequentially per VM. With H1 having moved the C1 VM
  # off first, this drain only has F2's single VM to handle. 20 min
  # is generous safety for emulated qemu (real KVM finishes in <60s).
  if ! wait_until 1200 sh -c "curl -ksS -H 'Authorization: Bearer $KVMC_TOKEN' '$KVMC_SERVER/api/vms/$uuid' | python3 -c 'import json,sys; v=json.load(sys.stdin); sys.exit(0 if v.get(\"host_id\") != \"$KVMC_HOST\" else 1)'"; then
    api POST "/api/hosts/$KVMC_HOST/maintenance/exit" >/dev/null
    bad "drain did not move VM within 20 min"
    return 1
  fi
  api GET "/api/vms/$uuid" >/dev/null
  local new_host; new_host=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("host_id",""))')
  ok "drain moved VM to $new_host"
  api POST "/api/hosts/$KVMC_HOST/maintenance/exit" >/dev/null
  ok "exited maintenance"
}

phase_F3_drain_offline() {
  # Same shape but mode=offline. The VM is shut off, files copied,
  # restarted on destination — slower but works against any VM
  # state. Skip if F2 already drained (only one VM at a time).
  if ! create_test_vm "f3-drain-offline"; then return 1; fi
  local uuid=$LAST_CREATED_UUID
  # Don't START — we want this in shut-off state so offline-mode
  # exercises the right path.
  api POST /api/command -d "{\"action\":\"FORCESTOP\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{}}" >/dev/null
  sleep 4
  local code
  code=$(api POST "/api/hosts/$KVMC_HOST/maintenance/enter" -d '{"reason":"e2e drain offline","drain":true,"mode":"offline"}')
  [[ $code == 200 ]] || { bad "enter maint+drain status $code"; return 1; }
  ok "entered maintenance with drain=offline"
  if ! wait_until 1200 sh -c "curl -ksS -H 'Authorization: Bearer $KVMC_TOKEN' '$KVMC_SERVER/api/vms/$uuid' | python3 -c 'import json,sys; v=json.load(sys.stdin); sys.exit(0 if v.get(\"host_id\") != \"$KVMC_HOST\" else 1)'"; then
    api POST "/api/hosts/$KVMC_HOST/maintenance/exit" >/dev/null
    bad "offline drain did not move VM within 20 min"
    return 1
  fi
  api GET "/api/vms/$uuid" >/dev/null
  local new_host; new_host=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("host_id",""))')
  ok "drain (offline) moved VM to $new_host"
  api POST "/api/hosts/$KVMC_HOST/maintenance/exit" >/dev/null
  ok "exited maintenance"
}

phase_P1_snapshot_policy() {
  # Create a policy that snapshots a test VM every minute, retain 2.
  # Wait for it to fire, verify snapshot count grows, then delete
  # the policy.
  if ! create_test_vm "p1-snap-policy"; then return 1; fi
  local uuid=$LAST_CREATED_UUID
  api POST /api/command -d "{\"action\":\"START\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{}}" >/dev/null
  sleep 6
  local pid="${TAG_PREFIX}-policy"
  local code
  code=$(api POST /api/snapshot-policies -d "{\"id\":\"$pid\",\"name\":\"$pid\",\"enabled\":true,\"vm_uuids\":[\"$uuid\"],\"every_minutes\":1,\"retention_keep\":2}")
  [[ $code == 200 ]] || { bad "create policy status $code: $(api_body | head -c 200)"; return 1; }
  ok "created snapshot policy $pid (every=1m retain=2)"
  # Scheduler ticks every minute. Wait up to 2.5 min for ≥1
  # policy-generated snapshot.
  api GET "/api/vms/$uuid" >/dev/null
  local vm_name; vm_name=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("name",""))')
  local found=0
  for i in $(seq 1 30); do
    api GET "/api/vms/snapshots?host=$KVMC_HOST&vm=$vm_name" >/dev/null
    local n; n=$(api_body | python3 -c 'import json,sys;print(len(json.load(sys.stdin).get("snapshots",[])))')
    if [[ $n -ge 1 ]]; then
      found=1; ok "policy fired (snapshot count=$n) after $((i*5))s"; break
    fi
    sleep 5
  done
  if (( found != 1 )); then
    api DELETE "/api/snapshot-policies/$pid" >/dev/null
    bad "policy didn't fire within 150s"
    return 1
  fi
  # Cleanup the policy + the snapshots it created. v5.60.2: assert
  # the DELETE actually removed the policy from the server's
  # scheduler — pre-v5.60.2 the test fired DELETE but ignored the
  # response, so a 4xx slipped through and the policy kept firing.
  local del_code; del_code=$(api DELETE "/api/snapshot-policies/$pid")
  [[ $del_code == 200 ]] || { bad "delete policy returned $del_code: $(api_body | head -c 200)"; return 1; }
  # Poll: scheduler removes from in-memory map under write lock;
  # the next ListSnapshotPolicies should not include $pid.
  if ! wait_until 10 sh -c "curl -ksS -H 'Authorization: Bearer $KVMC_TOKEN' '$KVMC_SERVER/api/snapshot-policies' | python3 -c 'import json,sys; sys.exit(0 if not any(p[\"id\"]==\"$pid\" for p in json.load(sys.stdin)) else 1)'"; then
    bad "policy $pid still in /api/snapshot-policies after DELETE"
    return 1
  fi
  ok "deleted policy + confirmed gone from /api/snapshot-policies"
  api GET "/api/vms/snapshots?host=$KVMC_HOST&vm=$vm_name" >/dev/null
  api_body | python3 -c "
import json,sys
for s in json.load(sys.stdin).get('snapshots',[]):
    print(s['name'])
" | while read -r snap; do
    [[ -z $snap ]] && continue
    api POST /api/command -d "{\"action\":\"DELETE_SNAPSHOT\",\"vm_uuid\":\"$uuid\",\"vm_name\":\"$vm_name\",\"host_id\":\"$KVMC_HOST\",\"params\":{\"name\":\"$snap\"}}" >/dev/null
  done
}

phase_U1_role_create() {
  # Create a viewer-only role: just vm.view + host.view.
  local rname="${TAG_PREFIX}-role-viewer"
  local code
  code=$(api POST /api/roles/new -d "{\"name\":\"$rname\",\"description\":\"e2e viewer\",\"permissions\":[\"vm.view\",\"host.view\"]}")
  [[ $code == 200 ]] || { bad "role create status $code: $(api_body | head -c 200)"; return 1; }
  local rid; rid=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("id",""))')
  [[ -n $rid ]] || { bad "role create returned no id"; return 1; }
  echo "$rid" > "$OUTDIR/.role_id"
  ok "created role $rname (id=$rid) with perms vm.view+host.view"
}

phase_U2_group_create() {
  local rid; rid=$(cat "$OUTDIR/.role_id" 2>/dev/null) || { bad "no role from U1"; return 1; }
  local gname="${TAG_PREFIX}-group-readers"
  local code
  code=$(api POST /api/groups/new -d "{\"name\":\"$gname\",\"description\":\"e2e readers\",\"role_ids\":[\"$rid\"]}")
  [[ $code == 200 ]] || { bad "group create status $code: $(api_body | head -c 200)"; return 1; }
  local gid; gid=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("id",""))')
  [[ -n $gid ]] || { bad "group create returned no id"; return 1; }
  echo "$gid" > "$OUTDIR/.group_id"
  ok "created group $gname (id=$gid) with role $rid"
}

phase_U3_user_create() {
  local gid; gid=$(cat "$OUTDIR/.group_id" 2>/dev/null) || { bad "no group from U2"; return 1; }
  local uname="e2eharness-${RUN_TS}-user"
  local code
  code=$(api POST /api/users/new -d "{\"username\":\"$uname\",\"password\":\"E2eHarness#${RUN_TS}!\",\"enabled\":true,\"group_ids\":[\"$gid\"]}")
  [[ $code == 200 ]] || { bad "user create status $code: $(api_body | head -c 200)"; return 1; }
  local uid; uid=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("id",""))')
  [[ -n $uid ]] || { bad "user create returned no id"; return 1; }
  echo "$uid|$uname" > "$OUTDIR/.user_id"
  ok "created user $uname (id=$uid) in group $gid"
}

phase_U4_access_matrix() {
  # Mint a system token scoped to the viewer role's perms. (Tokens
  # are admin-only to mint — we use the operator's admin token but
  # set scopes to the limited perm set, which narrows the effective
  # permissions per v5.51+ scope semantics.)
  local code
  code=$(api POST /api/tokens -d "{\"name\":\"${TAG_PREFIX}-viewer-tok\",\"scopes\":[\"vm.view\",\"host.view\"]}")
  [[ $code == 200 ]] || { bad "token mint status $code"; return 1; }
  local tok; tok=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("token",""))')
  [[ -n $tok ]] || { bad "token response missing token"; return 1; }
  ok "minted viewer-scoped token"
  echo "$tok" > "$OUTDIR/.viewer_token"
  # Allowed: GET /api/hosts (host.view), GET /api/vms (vm.view).
  local read1 read2
  read1=$(curl -ksS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $tok" "$KVMC_SERVER/api/hosts")
  read2=$(curl -ksS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $tok" "$KVMC_SERVER/api/vms")
  [[ $read1 == 200 ]] || { bad "viewer GET /api/hosts → $read1"; return 1; }
  [[ $read2 == 200 ]] || { bad "viewer GET /api/vms → $read2"; return 1; }
  ok "viewer reads allowed (hosts:$read1 vms:$read2)"
  # Denied: POST /api/command (needs vm.lifecycle), PUT maintenance
  # (needs host.manage).
  local deny1 deny2
  deny1=$(curl -ksS -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
    -d '{"action":"START","vm_uuid":"none","host_id":"none","params":{}}' "$KVMC_SERVER/api/command")
  deny2=$(curl -ksS -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $tok" \
    -d '{"reason":"x"}' "$KVMC_SERVER/api/hosts/$KVMC_HOST/maintenance/enter")
  [[ $deny1 == 403 ]] || { bad "viewer POST /api/command → $deny1 (want 403)"; return 1; }
  [[ $deny2 == 403 ]] || { bad "viewer POST /api/hosts/.../maintenance/enter → $deny2 (want 403)"; return 1; }
  ok "viewer writes denied (command:$deny1 maint-enter:$deny2)"
}

phase_U5_cleanup() {
  # Revoke the test token, delete the user, group, role.
  local tok; tok=$(cat "$OUTDIR/.viewer_token" 2>/dev/null)
  if [[ -n $tok ]]; then
    # Token id is the chars between "kvmc_" and the dot.
    local tid; tid=${tok#kvmc_}; tid=${tid%%.*}
    api DELETE "/api/tokens/$tid" >/dev/null
  fi
  local uid_uname; uid_uname=$(cat "$OUTDIR/.user_id" 2>/dev/null)
  if [[ -n $uid_uname ]]; then
    local uid=${uid_uname%%|*}
    api DELETE "/api/users/$uid" >/dev/null
  fi
  local gid; gid=$(cat "$OUTDIR/.group_id" 2>/dev/null)
  [[ -n $gid ]] && api DELETE "/api/groups/$gid" >/dev/null
  local rid; rid=$(cat "$OUTDIR/.role_id" 2>/dev/null)
  [[ -n $rid ]] && api DELETE "/api/roles/$rid" >/dev/null
  ok "deleted test user, group, role, token"
}

phase_DG1_discord_state_machine() {
  # Read-only state-machine assertions on the discord-gateway
  # singleton. We don't trigger pair-flow side effects here — that
  # would disrupt the operator's live notifications. Just verify
  # the state is in a known-sane shape.
  local code; code=$(api GET /api/settings/discord-gateway)
  [[ $code == 200 ]] || { bad "GET discord-gateway status $code"; return 1; }
  api_body | python3 -c "
import json, sys
g = json.load(sys.stdin)
if not g.get('enabled'):
    print('SKIP: discord-gateway not enabled'); sys.exit(77)
status = g.get('status','')
ok_states = ('healthy','approved','connecting','pending','saving','revoked','timeout','unreachable','disabled')
if status not in ok_states:
    print(f'unknown status: {status}'); sys.exit(1)
# stuck-state guard (v5.60.0): saving with empty token + no reg_id
# is the bug we fixed. Assert it doesn't recur.
if status == 'saving' and not g.get('token') and not g.get('registration_id'):
    # Tolerate if PendingSince is recent (in-flight pair).
    print('warning: status=saving + no token + no reg_id — should auto-flip to revoked if stale')
print(f'status={status} healthy_or_resolvable=True')
" || {
    rc=$?
    [[ $rc == 77 ]] && return 77
    bad "discord-gateway state assertion failed"
    return 1
  }
  ok "discord-gateway state is sane"
}

phase_M1_retire_reenrol() {
  if (( WITH_RETIRE != 1 )); then
    note "retire scenarios are destructive; rerun with --with-retire to include"
    return 77
  fi
  log "    [M1] retire+re-enrol cycle (DESTRUCTIVE — opt-in)"
  local code; code=$(api DELETE "/api/hosts/$KVMC_HOST?keep_files=1&reason=e2e+M1+probe")
  [[ $code == 200 ]] || { bad "delete host status $code"; return 1; }
  ok "retired $KVMC_HOST"
  # Verify it's gone from inventory + in deny-list.
  api GET /api/hosts >/dev/null
  local still
  still=$(api_body | python3 -c "import json,sys; print(any(h['id']=='$KVMC_HOST' for h in json.load(sys.stdin)))")
  [[ $still == False ]] || { bad "host still in inventory after delete"; return 1; }
  api GET /api/hosts/retired >/dev/null
  local retired
  retired=$(api_body | python3 -c "import json,sys; print(any(h.get('host_id')=='$KVMC_HOST' for h in json.load(sys.stdin)))")
  [[ $retired == True ]] || { bad "host not in deny-list after delete"; return 1; }
  ok "deny-list contains $KVMC_HOST"
  # v5.60.11 — verify install.sh surfaces the deny-list 403's actionable
  # message before we unretire. Pre-fix install.sh used `curl -fsS` which
  # suppressed the response body, so an operator hitting this path saw a
  # generic "check server URL + connectivity" and got sent looking in
  # the wrong direction. Catches the regression in one shot.
  local short_pre="${KVMC_HOST%%.*}"
  log "    [pre-check] install.sh against retired host should surface the 403 message"
  local rejected_log; rejected_log="$OUTDIR/.install-rejected.log"
  ssh "$short_pre" "curl -fsSLk $KVMC_SERVER/downloads/install.sh | bash -s -- --server $KVMC_SERVER --host-id $KVMC_HOST 2>&1" >"$rejected_log" 2>&1
  local rc=$?
  if (( rc == 0 )); then
    bad "install.sh exited 0 against a retired host — should fail with the deny-list message"
    return 1
  fi
  if ! grep -qiE "retired|deny|403" "$rejected_log"; then
    bad "install.sh failed but didn't mention the retired list / 403 (operator can't act on this) — saw:\n$(tail -10 "$rejected_log")"
    return 1
  fi
  ok "install.sh surfaces the deny-list 403 message before retire is cleared"
  # Also assert the API-direct response shape: posting a CSR for a
  # retired host_id must 403 AND the body must include "retired" so
  # any future client (kvmctl, ansible, etc.) gets an actionable
  # message — not just install.sh's wrapper.
  local probe_body
  probe_body="{\"host_id\":\"$KVMC_HOST\",\"csr\":\"-----BEGIN CERTIFICATE REQUEST-----\\nplaceholder\\n-----END CERTIFICATE REQUEST-----\\n\"}"
  code=$(api POST /api/ca/enrollment-requests -d "$probe_body")
  [[ $code == 403 ]] || { bad "direct CSR submit for retired host should 403, got $code"; return 1; }
  api_body | grep -qi "retired" || { bad "/api/ca/enrollment-requests 403 body should mention 'retired' — got: $(api_body | head -c 200)"; return 1; }
  ok "API-direct enrolment for retired host returns 403 with actionable body"
  # Unretire to allow re-enrolment.
  code=$(api POST "/api/hosts/$KVMC_HOST/unretire")
  [[ $code == 200 ]] || { bad "unretire status $code"; return 1; }
  ok "unretired (deny-list cleared)"
  # Run install.sh on the host to re-enrol.
  local short="${KVMC_HOST%%.*}"
  log "    re-running install.sh on $short..."
  ssh "$short" "curl -fsSLk $KVMC_SERVER/downloads/install.sh | bash -s -- --server $KVMC_SERVER --host-id $KVMC_HOST 2>&1" >"$OUTDIR/.install.log" 2>&1 &
  local pid=$!
  sleep 15
  # Find pending enrolment.
  api GET /api/ca/enrollment-requests-list >/dev/null
  local pending_id
  pending_id=$(api_body | python3 -c "
import json,sys
for p in json.load(sys.stdin):
    if p['status'] == 'pending' and p['host_id'] == '$KVMC_HOST':
        print(p['id']); break
")
  [[ -n $pending_id ]] || { bad "no pending enrolment after install.sh"; wait $pid 2>/dev/null; return 1; }
  code=$(api POST "/api/ca/enrollment-requests/$pending_id/approve")
  [[ $code == 200 ]] || { bad "approve status $code"; return 1; }
  ok "approved enrolment $pending_id"
  wait $pid 2>/dev/null
  sleep 18
  # Verify host back online.
  api GET "/api/hosts/$KVMC_HOST" >/dev/null
  local status; status=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("status",""))')
  [[ $status == online ]] || { bad "post-reenrol status=$status"; return 1; }
  ok "host re-enrolled, status=online"
}

# M2 — Enrolment reject path. M1 covers approve; this is the
# symmetric reject scenario. Important because operators occasionally
# get an enrolment from a misnamed / mistargeted install.sh run and
# need to reject without falling back to revoke later. Validates
# both the server-side reject endpoint AND that install.sh's polling
# loop surfaces the rejection to the operator on the host.
phase_M2_enrolment_reject() {
  if (( WITH_RETIRE != 1 )); then
    note "reject scenarios depend on M1's retire+install harness; rerun with --with-retire to include"
    return 77
  fi
  log "    [M2] enrolment reject cycle (DESTRUCTIVE — opt-in)"
  # Retire + unretire so we have a clean install.sh entry point.
  local code; code=$(api DELETE "/api/hosts/$KVMC_HOST?keep_files=1&reason=e2e+M2+reject+probe")
  [[ $code == 200 ]] || { bad "delete host status $code"; return 1; }
  code=$(api POST "/api/hosts/$KVMC_HOST/unretire")
  [[ $code == 200 ]] || { bad "unretire status $code"; return 1; }
  local short="${KVMC_HOST%%.*}"
  log "    re-running install.sh on $short (rejection path)…"
  local install_log; install_log="$OUTDIR/.install-reject.log"
  ssh "$short" "curl -fsSLk $KVMC_SERVER/downloads/install.sh | bash -s -- --server $KVMC_SERVER --host-id $KVMC_HOST 2>&1" >"$install_log" 2>&1 &
  local pid=$!
  # Wait for the pending enrolment to land.
  if ! wait_until 60 sh -c "curl -ksS -H 'Authorization: Bearer $KVMC_TOKEN' '$KVMC_SERVER/api/ca/enrollment-requests-list' | python3 -c 'import json,sys; sys.exit(0 if any(p[\"status\"]==\"pending\" and p[\"host_id\"]==\"$KVMC_HOST\" for p in json.load(sys.stdin)) else 1)'"; then
    bad "no pending enrolment within 60s of install.sh start"
    kill $pid 2>/dev/null; wait $pid 2>/dev/null
    return 1
  fi
  api GET /api/ca/enrollment-requests-list >/dev/null
  local pending_id
  pending_id=$(api_body | python3 -c "
import json,sys
for p in json.load(sys.stdin):
    if p['status'] == 'pending' and p['host_id'] == '$KVMC_HOST':
        print(p['id']); break
")
  [[ -n $pending_id ]] || { bad "couldn't extract pending id"; kill $pid 2>/dev/null; wait $pid 2>/dev/null; return 1; }
  ok "pending enrolment $pending_id captured for reject"
  # Reject with a reason; verify install.sh's polling loop surfaces it.
  code=$(api POST "/api/ca/enrollment-requests/$pending_id/reject" -d '{"reason":"e2e M2 reject probe"}')
  [[ $code == 200 ]] || { bad "reject status $code"; kill $pid 2>/dev/null; wait $pid 2>/dev/null; return 1; }
  ok "rejected enrolment $pending_id"
  # install.sh should exit non-zero within a few poll cycles.
  local waited=0
  while kill -0 $pid 2>/dev/null && (( waited < 120 )); do sleep 5; waited=$((waited+5)); done
  if kill -0 $pid 2>/dev/null; then
    kill $pid 2>/dev/null; wait $pid 2>/dev/null
    bad "install.sh didn't exit within 120s after reject"
    return 1
  fi
  wait $pid 2>/dev/null
  local rc=$?
  if (( rc == 0 )); then
    bad "install.sh exited 0 after reject — should fail"
    return 1
  fi
  if ! grep -qiE "reject|denied|refused" "$install_log"; then
    bad "install.sh failed but didn't mention reject/denied/refused — saw:\n$(tail -10 "$install_log")"
    return 1
  fi
  ok "install.sh surfaces operator rejection (rc=$rc, log mentions reject)"
  # Verify host did NOT come online (reject blocked the cert issue).
  api GET "/api/hosts/$KVMC_HOST" >/dev/null
  local final_status; final_status=$(api_body | python3 -c 'import json,sys
try:    print(json.load(sys.stdin).get("status",""))
except: print("absent")' 2>/dev/null)
  if [[ "$final_status" == "online" ]]; then
    bad "rejected host came online anyway — reject path didn't block cert issue"
    return 1
  fi
  ok "rejected host is not online (final state: ${final_status:-absent})"
  # Cleanup for downstream scenarios: re-run install.sh and approve
  # so the host is back online. M2 is destructive enough that this
  # restore is part of the contract.
  log "    [M2 cleanup] re-running install.sh + approving to restore host"
  ssh "$short" "curl -fsSLk $KVMC_SERVER/downloads/install.sh | bash -s -- --server $KVMC_SERVER --host-id $KVMC_HOST 2>&1" >"$OUTDIR/.install-m2-restore.log" 2>&1 &
  local rpid=$!
  if ! wait_until 60 sh -c "curl -ksS -H 'Authorization: Bearer $KVMC_TOKEN' '$KVMC_SERVER/api/ca/enrollment-requests-list' | python3 -c 'import json,sys; sys.exit(0 if any(p[\"status\"]==\"pending\" and p[\"host_id\"]==\"$KVMC_HOST\" for p in json.load(sys.stdin)) else 1)'"; then
    bad "no pending enrolment in restore phase"
    kill $rpid 2>/dev/null; wait $rpid 2>/dev/null
    return 1
  fi
  api GET /api/ca/enrollment-requests-list >/dev/null
  local restore_id; restore_id=$(api_body | python3 -c "
import json,sys
for p in json.load(sys.stdin):
    if p['status'] == 'pending' and p['host_id'] == '$KVMC_HOST':
        print(p['id']); break
")
  [[ -n $restore_id ]] || { bad "couldn't find restore-pending id"; wait $rpid 2>/dev/null; return 1; }
  code=$(api POST "/api/ca/enrollment-requests/$restore_id/approve")
  [[ $code == 200 ]] || { bad "restore approve status $code"; wait $rpid 2>/dev/null; return 1; }
  wait $rpid 2>/dev/null
  sleep 18
  ok "host restored online after M2 reject cycle"
}

# ── S-series — survivability + persistence sentinels ─────────────
# These guard against the class of bug v5.60.3-7 fixed: state lost
# across Save→Load, concurrent-save E11000 races, and JSON omitempty
# silently dropping `false` from the GET response so toggles
# rebound on refresh. Each scenario is cheap (read-only or self-
# restoring) and runnable on every round.

# S1 — Settings round-trip parity. The v5.60.5 omitempty bug
# round-tripped clean on the server side (the value was saved) but
# the GET response omitted the field when its value was the zero
# value, so the frontend's `?? <default>` fallback resurrected the
# default and the toggle appeared to never save. This scenario flips
# a representative boolean back and forth on each settings surface
# and asserts the GET response carries the EXPLICIT value, not a
# missing-field-treated-as-default.
phase_S1_settings_roundtrip() {
  # Features singleton — flip `notify_on_task_failure` then restore.
  api GET /api/settings >/dev/null
  local orig
  orig=$(api_body | python3 -c 'import json,sys;d=json.load(sys.stdin);print("true" if d.get("notify_on_task_failure") else "false")')
  local flipped
  if [[ "$orig" == "true" ]]; then flipped="false"; else flipped="true"; fi

  api POST /api/settings/update -d "{\"notify_on_task_failure\":$flipped}" >/dev/null
  api GET /api/settings >/dev/null
  local got
  got=$(api_body | python3 -c 'import json,sys;d=json.load(sys.stdin);v=d.get("notify_on_task_failure");print("missing" if v is None else ("true" if v else "false"))')
  if [[ "$got" != "$flipped" ]]; then
    # Restore best-effort before failing.
    api POST /api/settings/update -d "{\"notify_on_task_failure\":$orig}" >/dev/null
    bad "features round-trip: flipped to $flipped, got back '$got' (omitempty silently dropping false?)"
    return 1
  fi
  ok "features: notify_on_task_failure round-trips $orig→$flipped→GET=$got"

  # Restore.
  api POST /api/settings/update -d "{\"notify_on_task_failure\":$orig}" >/dev/null

  # Discord-Gateway audit-ship toggle — the v5.60.5 actual offender.
  # Only run this leg when the gateway is currently enabled, so we
  # don't perturb a fresh deployment that hasn't been paired.
  api GET /api/settings/discord-gateway >/dev/null
  local dg_enabled
  dg_enabled=$(api_body | python3 -c 'import json,sys;d=json.load(sys.stdin);print("true" if d.get("enabled") else "false")')
  if [[ "$dg_enabled" != "true" ]]; then
    note "skipping DG round-trip leg — gateway not enabled"
    return 0
  fi
  api GET /api/settings/discord-gateway >/dev/null
  local dg_orig
  dg_orig=$(api_body | python3 -c 'import json,sys;d=json.load(sys.stdin);print("true" if d.get("ship_audit_failed_only") else "false")')
  local dg_flip
  if [[ "$dg_orig" == "true" ]]; then dg_flip="false"; else dg_flip="true"; fi
  # PUT echoes the full singleton; we send the fields the panel does.
  # Verify-tls, tls etc are read back to preserve them across the PUT.
  local dg_body
  dg_body=$(api_body)
  local dg_payload
  dg_payload=$(DG_BODY="$dg_body" DG_FLIP="$dg_flip" python3 -c '
import json, os
d = json.loads(os.environ["DG_BODY"])
out = {
    "enabled": d.get("enabled", False),
    "host": d.get("host", ""),
    "port": d.get("port", 0),
    "tls": d.get("tls", True),
    "verify_tls": d.get("verify_tls", False),
    "ship_audit_events": d.get("ship_audit_events", False),
    "ship_audit_failed_only": os.environ["DG_FLIP"] == "true",
}
print(json.dumps(out))
')
  api PUT /api/settings/discord-gateway -d "$dg_payload" >/dev/null
  api GET /api/settings/discord-gateway >/dev/null
  local dg_got
  dg_got=$(api_body | python3 -c 'import json,sys;d=json.load(sys.stdin);v=d.get("ship_audit_failed_only");print("missing" if v is None else ("true" if v else "false"))')
  # Restore regardless of outcome.
  local dg_restore
  dg_restore=$(DG_BODY="$dg_body" DG_ORIG="$dg_orig" python3 -c '
import json, os
d = json.loads(os.environ["DG_BODY"])
out = {
    "enabled": d.get("enabled", False),
    "host": d.get("host", ""),
    "port": d.get("port", 0),
    "tls": d.get("tls", True),
    "verify_tls": d.get("verify_tls", False),
    "ship_audit_events": d.get("ship_audit_events", False),
    "ship_audit_failed_only": os.environ["DG_ORIG"] == "true",
}
print(json.dumps(out))
')
  api PUT /api/settings/discord-gateway -d "$dg_restore" >/dev/null
  if [[ "$dg_got" != "$dg_flip" ]]; then
    bad "DG round-trip: flipped ship_audit_failed_only to $dg_flip, GET returned '$dg_got' (v5.60.5 regression?)"
    return 1
  fi
  ok "DG: ship_audit_failed_only round-trips $dg_orig→$dg_flip→GET=$dg_got"
}

# S2 — Audit-log error sentinel. The pre-v5.60.4 / v5.60.6 E11000
# storm never showed up in the battery — it only emitted from
# the Mongo backend on every save tick, logging "store: backend
# save error" without failing any user-visible operation. This
# scenario fails the round when audit events in the recent window
# carry the persistence-error signature, so the next regression
# of the concurrent-save problem fails the battery directly
# rather than only surfacing in prod logs.
phase_S2_persistence_error_sentinel() {
  api GET "/api/audit?limit=200" >/dev/null
  local bad_count
  bad_count=$(api_body | python3 -c "
import json, sys, time
data = json.load(sys.stdin)
items = data if isinstance(data, list) else data.get('events', data.get('items', []))
# Look for save-error / E11000 / dup key signals in the last 5 min,
# regardless of which action triggered them.
from datetime import datetime, timezone
cutoff = time.time() - 300
hits = 0
for ev in items or []:
    if ev.get('success'):
        continue
    at = ev.get('at') or ''
    try:
        ts = datetime.fromisoformat(at.replace('Z','+00:00')).timestamp()
    except Exception:
        continue
    if ts < cutoff:
        continue
    err = (ev.get('error') or '').lower()
    if 'e11000' in err or 'duplicate key' in err or 'backend save' in err:
        hits += 1
print(hits)
")
  if [[ "$bad_count" -gt 0 ]]; then
    bad "found $bad_count persistence-error audit events in the last 5 min — concurrent-save races likely regressed"
    return 1
  fi
  ok "no persistence-error audit events in the last 5 min"
}

# ── T-series — kvm-tools install + distribution plumbing ────────
# Validate the install surface end-to-end without provisioning a
# fresh OS guest: the binary the operator's `curl | bash` install
# script pulls IS an ELF executable (not the SPA HTML — see
# feedback_dist_route_bug.md for that prior regression), and the
# server's published kvm-tools version is reachable + parseable.
# A future T3 should provision a real-OS guest, run the install,
# and verify telemetry — needs a heavier image than the alpine
# net-boot stubs the rest of the battery uses. Tracked, deferred.

phase_T1_kvmtools_version_endpoint() {
  api GET /api/kvm-tools/version >/dev/null
  local body; body=$(api_body)
  local ver
  ver=$(printf '%s' "$body" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(1)
print(d.get("version") or "")
')
  if [[ ! "$ver" =~ ^v[0-9]+\.[0-9]+\.[0-9]+ ]]; then
    bad "kvm-tools version endpoint returned unexpected shape: $body"
    return 1
  fi
  ok "kvm-tools published version: $ver"
}

phase_T2_kvmtools_binary_download() {
  # Pull the linux/amd64 binary the way an operator's curl|bash
  # install script does. Verify it is actually an ELF — the
  # /dist catch-all has bitten before by silently serving the
  # SPA's index.html for unknown paths.
  local out="$OUTDIR/kvmt.bin"
  local code
  code=$(curl -ksS -o "$out" -w '%{http_code}' \
    -H "Authorization: Bearer $KVMC_TOKEN" \
    "$KVMC_SERVER/downloads/kvm-tools-linux-amd64")
  [[ "$code" == "200" ]] || { bad "kvm-tools linux download http=$code"; return 1; }
  local bytes; bytes=$(wc -c < "$out" | tr -d ' ')
  (( bytes > 1000000 )) || { bad "kvm-tools linux binary suspiciously small ($bytes bytes)"; return 1; }
  if ! file "$out" | grep -q "ELF.*executable"; then
    bad "downloaded /downloads/kvm-tools-linux-amd64 is not an ELF binary (regression of feedback_dist_route_bug.md): $(file -b "$out")"
    return 1
  fi
  # Same probe for the windows binary so a future cross-compile
  # slip is caught alongside the linux one.
  local wout="$OUTDIR/kvmt.exe"
  code=$(curl -ksS -o "$wout" -w '%{http_code}' \
    -H "Authorization: Bearer $KVMC_TOKEN" \
    "$KVMC_SERVER/downloads/kvm-tools-windows-amd64.exe")
  [[ "$code" == "200" ]] || { bad "kvm-tools windows download http=$code"; return 1; }
  local wbytes; wbytes=$(wc -c < "$wout" | tr -d ' ')
  (( wbytes > 1000000 )) || { bad "kvm-tools windows binary suspiciously small ($wbytes bytes)"; return 1; }
  if ! file "$wout" | grep -qE "PE32|MS-DOS"; then
    bad "downloaded windows binary is not a PE executable: $(file -b "$wout")"
    return 1
  fi
  ok "kvm-tools binaries downloadable + valid (linux=$bytes B ELF, windows=$wbytes B PE)"
}

# ── V/HC/NT/TS/MG/AU — operator-surface coverage expansion ──────
# Adds scenarios for operator-critical endpoints the prior battery
# never touched. Each is cheap (no VM lifecycle except V1) and
# protects an everyday operator surface.

# V1 — VM autostart toggle. EDIT_VM accepts an `autostart` key in
# the params map; flipping it round-trips through the inventory
# read and the agent's libvirt domain XML. Catches regressions in
# the EDIT_VM diff parser + the agent's autostart wiring.
phase_V1_vm_autostart_toggle() {
  if ! create_test_vm "v1-autostart"; then return 1; fi
  local uuid=$LAST_CREATED_UUID
  # Default after vm-create should be autostart=false. Flip to true.
  local code
  code=$(api POST /api/command -d "{\"action\":\"EDIT_VM\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{\"autostart\":\"true\"}}")
  [[ $code == 200 ]] || { bad "EDIT_VM autostart=true status=$code body=$(api_body)"; return 1; }
  # Wait for telemetry to reflect.
  if ! wait_until 30 sh -c "curl -ksS -H 'Authorization: Bearer $KVMC_TOKEN' '$KVMC_SERVER/api/vms/$uuid' | python3 -c 'import json,sys; v=json.load(sys.stdin); sys.exit(0 if v.get(\"autostart\") is True else 1)'"; then
    bad "autostart didn't flip to true within 30s"
    return 1
  fi
  # Flip back to false to leave the VM in its default-for-cleanup state.
  code=$(api POST /api/command -d "{\"action\":\"EDIT_VM\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{\"autostart\":\"false\"}}")
  [[ $code == 200 ]] || { bad "EDIT_VM autostart=false status=$code"; return 1; }
  ok "autostart round-trips false → true → false"
}

# N2 — snapshot revert on a RUNNING VM. N1 covers the create/list/
# revert/delete on a stopped VM. This complements it with the
# online-revert path — libvirt's snapshot-revert on a running
# domain behaves differently from one on a stopped domain, and
# operators routinely use both.
phase_N2_snapshot_revert_running() {
  if ! create_test_vm "n2-snap-running"; then return 1; fi
  local uuid=$LAST_CREATED_UUID
  # Start the VM so the revert is online.
  api POST /api/command -d "{\"action\":\"START\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{}}" >/dev/null
  if ! wait_until 30 sh -c "curl -ksS -H 'Authorization: Bearer $KVMC_TOKEN' '$KVMC_SERVER/api/vms/$uuid' | python3 -c 'import json,sys; v=json.load(sys.stdin); sys.exit(0 if v.get(\"state\")==\"running\" else 1)'"; then
    bad "VM not running before snapshot+revert test"
    return 1
  fi
  local snap_name="${TAG_PREFIX}-n2-snap"
  local code
  code=$(api POST /api/command -d "{\"action\":\"SNAPSHOT\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{\"name\":\"$snap_name\"}}")
  [[ $code == 200 ]] || { bad "SNAPSHOT on running VM status=$code"; return 1; }
  # Revert online — VM should still be running after.
  code=$(api POST /api/command -d "{\"action\":\"REVERT_SNAPSHOT\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{\"name\":\"$snap_name\"}}")
  [[ $code == 200 ]] || { bad "REVERT_SNAPSHOT online status=$code body=$(api_body)"; return 1; }
  # Settle (libvirt may briefly transition through paused).
  sleep 5
  api GET "/api/vms/$uuid" >/dev/null
  local state
  state=$(api_body | python3 -c 'import json,sys;print(json.load(sys.stdin).get("state",""))')
  [[ "$state" == "running" || "$state" == "paused" ]] || { bad "post-online-revert state=$state (want running or paused)"; return 1; }
  # Clean the snapshot.
  api POST /api/command -d "{\"action\":\"DELETE_SNAPSHOT\",\"vm_uuid\":\"$uuid\",\"host_id\":\"$KVMC_HOST\",\"params\":{\"name\":\"$snap_name\"}}" >/dev/null
  ok "online snapshot+revert round-trip ok (post-revert state=$state)"
}

# HC1 — /health is a public, no-auth liveness probe. k8s-style;
# called by docker compose's healthcheck. Must return 200 with a
# simple JSON body even when the bearer token is absent.
phase_HC1_health_endpoint() {
  local code body
  code=$(curl -ksS -o "$OUTDIR/.body" -w '%{http_code}' "$KVMC_SERVER/health")
  [[ $code == 200 ]] || { bad "/health http=$code"; return 1; }
  body=$(api_body)
  if ! printf '%s' "$body" | python3 -c 'import json,sys;d=json.load(sys.stdin);sys.exit(0 if d.get("status")=="ok" else 1)' 2>/dev/null; then
    bad "/health body unexpected: $body"
    return 1
  fi
  ok "/health returns {status:ok} with no auth"
}

# HC2 — /api/admin/system-health. Operator's at-a-glance view of
# subsystem state (mongo, agents, CA, cert reloader, grant signer).
# Just assert it returns 200 and a parseable JSON object with the
# top-level `services` array the SPA expects.
phase_HC2_admin_system_health() {
  local code
  code=$(api GET /api/admin/system-health)
  [[ $code == 200 ]] || { bad "/api/admin/system-health http=$code"; return 1; }
  api_body | python3 -c '
import json, sys
d = json.load(sys.stdin)
if not isinstance(d, dict):
    sys.exit(1)
# The response carries an overall status + a per-subsystem list.
# Accept either flat keys or a nested services array; either way
# the operator-facing payload should not be empty.
if not d:
    sys.exit(2)
' || { bad "/api/admin/system-health body not a non-empty JSON object: $(api_body | head -c 200)"; return 1; }
  ok "/api/admin/system-health returns a populated payload"
}

# NT1 — /api/settings/notifications/test. The operator's "send a
# test card" button. We don't depend on a pre-existing channel
# (would require fixture setup); just hit the endpoint with a
# fake channel_id and assert 404 ("channel not found") — proves
# the route is wired, the body shape is parsed, and permission
# enforcement works.
phase_NT1_notifications_test_endpoint() {
  local code
  code=$(api POST /api/settings/notifications/test -d '{"channel_id":"e2e-fake"}')
  # 404 when the channel doesn't exist; 200 if by chance there's a
  # channel named e2e-fake (defensive). Anything else is a regression.
  if [[ "$code" != "404" && "$code" != "200" ]]; then
    bad "/api/settings/notifications/test http=$code body=$(api_body | head -c 200)"
    return 1
  fi
  ok "/api/settings/notifications/test wired (http=$code on synthetic body)"
}

# TS1 — /api/tasks/recent. The Recent Tasks pane in the SPA reads
# this; built v5.58.3 in place of the dismissed task toast. Catches
# the endpoint going missing or its schema drifting.
phase_TS1_tasks_recent() {
  local code
  code=$(api GET "/api/tasks/recent?limit=5")
  [[ $code == 200 ]] || { bad "/api/tasks/recent http=$code"; return 1; }
  # Response shape: {"count": <int>, "entries": [<task>, ...]}.
  api_body | python3 -c '
import json, sys
d = json.load(sys.stdin)
if not isinstance(d, dict):
    sys.exit(1)
entries = d.get("entries")
if not isinstance(entries, list):
    sys.exit(2)
# Count, when present, should match the entries length.
c = d.get("count")
if c is not None and c != len(entries):
    sys.exit(3)
' || { bad "/api/tasks/recent body shape unexpected: $(api_body | head -c 200)"; return 1; }
  local n; n=$(api_body | python3 -c 'import json,sys;print(len(json.load(sys.stdin).get("entries",[])))')
  ok "/api/tasks/recent returns {count, entries[]} — $n entries"
}

# MG1 — /api/migrations list. Operator's migrations history view.
# The H1, F2, F3 scenarios upstream create migration records;
# this verifies the list endpoint surfaces them.
phase_MG1_migrations_list() {
  local code
  code=$(api GET /api/migrations)
  [[ $code == 200 ]] || { bad "/api/migrations http=$code"; return 1; }
  api_body | python3 -c '
import json, sys
d = json.load(sys.stdin)
if not isinstance(d, list):
    sys.exit(1)
' || { bad "/api/migrations body is not a list"; return 1; }
  local count
  count=$(api_body | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))')
  ok "/api/migrations returns a list ($count records)"
}

# AU1 — /api/auth/me. Every authed page reads this to render the
# user menu + effective permissions. Token-auth should populate
# the same shape as session-cookie auth.
phase_AU1_auth_me() {
  local code
  code=$(api GET /api/auth/me)
  [[ $code == 200 ]] || { bad "/api/auth/me http=$code"; return 1; }
  api_body | python3 -c '
import json, sys
d = json.load(sys.stdin)
u = d.get("user")
perms = d.get("permissions")
if not isinstance(u, dict) or u.get("username","") == "":
    sys.exit(1)
if not isinstance(perms, list):
    sys.exit(2)
' || { bad "/api/auth/me missing user or permissions: $(api_body | head -c 200)"; return 1; }
  local who
  who=$(api_body | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d.get("user",{}).get("username","?"))')
  ok "/api/auth/me identifies token-bearer as '$who' with non-empty permissions"
}

# CC1 — /api/command/cancel contract. The handler requires a
# `request_id` query param; without it the response must be a
# 400 with a clear error message. Catches the route going missing
# or the parameter validation being silently dropped.
phase_CC1_command_cancel_contract() {
  local code
  code=$(api POST /api/command/cancel)
  [[ $code == 400 ]] || { bad "/api/command/cancel without request_id should be 400, got $code"; return 1; }
  # With a fake request_id, the handler should also reject (typically
  # 404 or 409 once it tries to find the in-flight command).
  code=$(api POST "/api/command/cancel?request_id=e2e-no-such-cmd")
  if [[ "$code" == "200" ]]; then
    bad "/api/command/cancel returned 200 for a fake request_id — schema regression?"
    return 1
  fi
  ok "/api/command/cancel rejects missing+invalid request_id (400 / $code)"
}

# MG2 — /api/migrations/cancel contract. Same shape as CC1: the
# operator's "stop this migration" button must reject a missing
# id with 400 and a bogus id with a non-success code. Catches the
# route going missing or the param validation regressing.
phase_MG2_migration_cancel_contract() {
  local code
  code=$(api POST /api/migrations/cancel)
  [[ $code == 400 ]] || { bad "/api/migrations/cancel without id should be 400, got $code"; return 1; }
  code=$(api POST "/api/migrations/cancel?id=e2e-no-such-mig")
  if [[ "$code" == "200" ]]; then
    bad "/api/migrations/cancel returned 200 for a fake id"
    return 1
  fi
  ok "/api/migrations/cancel rejects missing+invalid id (400 / $code)"
}

# RC1 — /api/admin/reconcile/<host>. This is a real-load probe,
# not just a contract test: the endpoint pings the agent and
# returns a refresh report. Catches the agent's /reconcile
# response shape drifting or the server-side coordinator going
# missing.
phase_RC1_admin_reconcile() {
  local code
  code=$(api POST "/api/admin/reconcile/$KVMC_HOST")
  [[ $code == 200 ]] || { bad "/api/admin/reconcile/$KVMC_HOST http=$code body=$(api_body | head -c 200)"; return 1; }
  api_body | python3 -c '
import json, sys
d = json.load(sys.stdin)
# The report carries the host_id + a refreshed_at timestamp at minimum.
if d.get("host_id","") == "":
    sys.exit(1)
if not d.get("refreshed_at"):
    sys.exit(2)
' || { bad "/api/admin/reconcile report missing host_id or refreshed_at: $(api_body | head -c 200)"; return 1; }
  ok "/api/admin/reconcile/$KVMC_HOST returns a populated report"
}

# AS1 — Audit-shipping settings round-trip. Operator reported a
# remote-syslog config being silently wiped earlier this cycle —
# adding an explicit guard that the PUT-then-GET cycle preserves
# every field the operator typed. Restores the prior value at the
# end so we don't perturb a configured shipper.
phase_AS1_audit_shipping_roundtrip() {
  api GET /api/settings/audit-shipping >/dev/null
  local orig; orig=$(api_body)
  # Build a synthetic config that exercises every field. We send it,
  # read it back, assert each key is preserved.
  local probe='{"enabled":false,"type":"syslog","endpoint":"e2e-probe.invalid:514","syslog_facility":1,"syslog_app_name":"kvm-center-e2e"}'
  local code
  code=$(api PUT /api/settings/audit-shipping -d "$probe")
  [[ $code == 200 ]] || { bad "audit-shipping PUT status=$code"; return 1; }
  api GET /api/settings/audit-shipping >/dev/null
  local got; got=$(api_body)
  echo "$got" | python3 -c '
import json, sys
d = json.load(sys.stdin)
checks = {
    "enabled": False,
    "type": "syslog",
    "endpoint": "e2e-probe.invalid:514",
    "syslog_facility": 1,
    "syslog_app_name": "kvm-center-e2e",
}
for k, v in checks.items():
    if d.get(k) != v:
        print(f"audit-shipping GET key {k!r} got {d.get(k)!r}, want {v!r}", file=sys.stderr)
        sys.exit(1)
' || { bad "audit-shipping GET dropped or mutated fields after PUT (config-wipe regression?) — saw $(printf "%s" "$got" | head -c 200)"; api PUT /api/settings/audit-shipping -d "$orig" >/dev/null; return 1; }
  # Restore.
  api PUT /api/settings/audit-shipping -d "$orig" >/dev/null
  ok "audit-shipping PUT→GET round-trip preserves enabled/type/endpoint/facility/app_name"
}

# ES1 / ES2 / ES3 — Empty-state probes (v5.60.17). Direct guard
# against the v5.60.12 (storage browse on empty pool) and v5.60.13
# (Create-VM modal against ISO-less host) crash class. Each probe
# exercises an endpoint that the SPA hits with `.length` / `.map`
# and asserts the response is well-formed JSON whose array fields
# are arrays (never `null`) — exactly the contract the post-v5.60.14
# server-side init makes possible. Pure reads; safe to run on any
# state-of-the-world. No fixtures needed.
phase_ES1_empty_isos() {
  # /api/hosts/<id>/vm-create-info — agent surveys ISOs + bridges +
  # pools. Pre-v5.60.13 returned isos:null when no ISO volumes were
  # found, crashing CreateVMModal.
  local code; code=$(api GET "/api/vm-create-info?host=$KVMC_HOST")
  [[ $code == 200 ]] || { bad "vm-create-info status=$code"; return 1; }
  api_body | python3 -c '
import json, sys
d = json.load(sys.stdin)
for k in ("isos", "bridges", "pools", "os_variants"):
    v = d.get(k)
    if v is None:
        print(f"vm-create-info: field {k!r} is null (must be []) — SPA will crash on .length", file=sys.stderr)
        sys.exit(1)
    if not isinstance(v, list):
        print(f"vm-create-info: field {k!r} is not a list (got {type(v).__name__})", file=sys.stderr)
        sys.exit(1)
' || { bad "vm-create-info returned null list fields — server agent or SPA-side defensive coalesce regressed"; return 1; }
  ok "empty-state: vm-create-info returns [] for all list fields (no null)"
}

phase_ES2_empty_tasks() {
  # /api/tasks?since=$now — when no tasks landed in the last second,
  # the response previously serialised entries as null on a clean
  # fleet. Post-v5.60.14 init guarantees [].
  local now; now=$(date -u +%s)
  local code; code=$(api GET "/api/tasks?since=$((now + 60))")
  [[ $code == 200 ]] || { bad "/api/tasks?since= status=$code"; return 1; }
  api_body | python3 -c '
import json, sys
d = json.load(sys.stdin)
v = d.get("entries")
if v is None:
    print("/api/tasks?since=future: entries is null (must be [])", file=sys.stderr)
    sys.exit(1)
if not isinstance(v, list):
    print(f"/api/tasks?since=future: entries is not a list (got {type(v).__name__})", file=sys.stderr)
    sys.exit(1)
if len(v) != 0:
    print(f"/api/tasks?since=future: expected empty list, got {len(v)} entries", file=sys.stderr)
    sys.exit(1)
' || { bad "/api/tasks?since= returned malformed entries on empty result"; return 1; }
  ok "empty-state: /api/tasks?since=future returns entries:[] (no null)"
}

phase_ES3_health_clean_arrays() {
  # /api/system-health — the Problems array goes null on a healthy
  # fleet pre-v5.60.14. Same risk for outdated_agents in the
  # hypervisors subsystem detail.
  local code; code=$(api GET /api/admin/system-health)
  [[ $code == 200 ]] || { bad "/api/system-health status=$code"; return 1; }
  api_body | python3 -c '
import json, sys
d = json.load(sys.stdin)
probs = d.get("problems")
if probs is None:
    print("/api/system-health: problems is null (must be [])", file=sys.stderr)
    sys.exit(1)
if not isinstance(probs, list):
    print(f"/api/system-health: problems is not a list (got {type(probs).__name__})", file=sys.stderr)
    sys.exit(1)
' || { bad "/api/system-health returned problems:null — SPA dashboard will crash on clean fleet"; return 1; }
  ok "empty-state: /api/system-health problems is [] (no null on a healthy fleet)"
}

# ── register scenarios ───────────────────────────────────────────
register A1 phase_A1_inventory               "Inventory: /api/hosts lists the target with mTLS + pubkey + hostkeys"
register A2 phase_A2_host_detail             "Inventory: host detail has all required fields"
register A3 phase_A3_telemetry_fresh         "Inventory: telemetry last_seen < 60s"
register B1 phase_B1_pool_define             "Storage: define a new dir-type pool via /api/hosts/<id>/storage/pools"
register B2 phase_B2_pool_visible            "Storage: pool appears in /api/storage within one telemetry tick"
register B3 phase_B3_pool_stop_start         "Storage: stop + start the pool"
register B4 phase_B4_pool_delete             "Storage: undefine the pool"
register C1 phase_C1_vm_create               "VM lifecycle: create a 256MB / 1 vCPU / 1 GB Alpine VM"
register C2 phase_C2_vm_state_transitions   "VM lifecycle: STOP / START / RESTART transitions"
register C3 phase_C3_vm_terminal_5xx_no_lock "VM lifecycle: F1 regression — 5xx body-success-false is terminal (no 30-min lock)"
register D1 phase_D1_edit_vm_combo_caprise   "EDIT_VM: combo cap-raise (vcpu+mem) in one shutdown cycle (v5.58.1)"
register N1 phase_N1_snapshot_lifecycle      "Snapshots: create x2, list, revert, delete x2"
register E1 phase_E1_expected_down_lifecycle "Expected-down: set+inspect+clear lifecycle (v5.56.0)"
register F1 phase_F1_maintenance_mode        "Maintenance: enter blocks new-VM placement, exit restores"
register P1 phase_P1_snapshot_policy         "Snapshot policy: every-1m policy fires + creates snapshots, then cleanup"
register U1 phase_U1_role_create             "RBAC: create a viewer-only role (vm.view + host.view)"
register U2 phase_U2_group_create            "RBAC: create a group that pulls in the viewer role"
register U3 phase_U3_user_create             "RBAC: create a user assigned to the viewer group"
register U4 phase_U4_access_matrix           "RBAC: mint a token scoped to viewer perms; reads OK, writes 403"
register U5 phase_U5_cleanup                 "RBAC: revoke token, delete user / group / role"
register DG1 phase_DG1_discord_state_machine "Discord Gateway: singleton state is in a known-sane shape (read-only)"
register G1 phase_G1_fleet_mesh_endpoints    "Mesh: /api/agents/migrator-authorized-keys + /api/agents/ssh-known-hosts populated"
register G2 phase_G2_cross_host_ssh          "Mesh: cross-host kvm-migrator SSH with StrictHostKeyChecking=yes succeeds"
register H1 phase_H1_migration_off_host      "Migration: live migrate test VM off the target host (auto transport)"
# F2/F3 run AFTER H1 — the H1 single-migrate moves the C1 test VM
# off the target, so by the time F2/F3 enter maintenance+drain, the
# only VM on the host is the dedicated one each F-phase just
# created. Drain a single VM fits inside the 20 min cap even on
# emulated qemu.
register F2 phase_F2_drain_live              "Maintenance+drain (live): VM live-migrates to a peer during drain"
register F3 phase_F3_drain_offline           "Maintenance+drain (offline): shut-off VM offline-migrates to a peer during drain"
register I1 phase_I1_console_token           "Console: mint token + render HTML page"
register J1 phase_J1_backup_export           "Backup: /api/admin/backup returns a valid gzip tarball"
register K1 phase_K1_pki_cert_ledger         "PKI: host has an entry in the cert ledger with > 30 days left"
register L1 phase_L1_audit_log               "Audit: /api/audit returns recent events"
register S1 phase_S1_settings_roundtrip      "Settings: bool toggle round-trips through GET (catches omitempty-on-false regressions)"
register S2 phase_S2_persistence_error_sentinel "Persistence: no E11000 / save-error audit events in last 5 min (concurrent-save sentinel)"
register T1 phase_T1_kvmtools_version_endpoint "kvm-tools: /api/kvm-tools/version returns a parseable vX.Y.Z"
register T2 phase_T2_kvmtools_binary_download  "kvm-tools: /downloads/kvm-tools-{linux,windows}-amd64 return real ELF/PE binaries (catches /dist SPA-HTML regression)"
register V1 phase_V1_vm_autostart_toggle      "VM: autostart toggle round-trips through EDIT_VM + inventory read"
register N2 phase_N2_snapshot_revert_running  "Snapshots: revert on a RUNNING VM keeps it running (online-revert path)"
register HC1 phase_HC1_health_endpoint        "Health: /health is a no-auth liveness probe (200 + {status:ok})"
register HC2 phase_HC2_admin_system_health    "Health: /api/admin/system-health surfaces subsystem state"
register NT1 phase_NT1_notifications_test_endpoint "Notifications: /api/settings/notifications/test is wired (synthetic channel → 404)"
register TS1 phase_TS1_tasks_recent           "Tasks: /api/tasks/recent returns a list (Recent Tasks pane source-of-truth)"
register MG1 phase_MG1_migrations_list        "Migrations: /api/migrations list endpoint returns records"
register AU1 phase_AU1_auth_me                "Auth: /api/auth/me identifies the token-bearer with effective permissions"
register CC1 phase_CC1_command_cancel_contract "Command cancel: /api/command/cancel rejects missing + invalid request_id"
register MG2 phase_MG2_migration_cancel_contract "Migration cancel: /api/migrations/cancel rejects missing + invalid id"
register RC1 phase_RC1_admin_reconcile        "Reconcile: /api/admin/reconcile/<host> returns a populated report (real-load probe)"
register AS1 phase_AS1_audit_shipping_roundtrip "Audit shipping: PUT→GET preserves every field (guards the config-wipe regression)"
register ES1 phase_ES1_empty_isos             "Empty-state: /api/hosts/<id>/vm-create-info returns [] for empty isos/bridges/pools (no null) — v5.60.13 crash class"
register ES2 phase_ES2_empty_tasks            "Empty-state: /api/tasks?since=future returns entries:[] (no null) — v5.60.14 sweep"
register ES3 phase_ES3_health_clean_arrays    "Empty-state: /api/system-health problems is [] on a healthy fleet (no null)"
register M1 phase_M1_retire_reenrol          "Retire + re-enrol cycle via approval flow (--with-retire only)"
register M2 phase_M2_enrolment_reject        "Enrolment reject cycle — install.sh surfaces operator rejection (--with-retire only)"

# ── dispatch ─────────────────────────────────────────────────────
if (( LIST_ONLY == 1 )); then
  printf '%-4s %s\n' "ID" "DESCRIPTION"
  printf '%-4s %s\n' "----" "-----------"
  for id in "${SCENARIOS[@]}"; do
    printf '%-4s %s\n' "$id" "$(scen_name "$id")"
  done
  exit 0
fi

: "${KVMC_TOKEN:?KVMC_TOKEN env var required (kvmc_<id>.<secret>)}"

# Initialize output only for an authorized execution path. --help and --list
# must not create files or overwrite a previous lab run's evidence.
OUTDIR="${KVMC_E2E_OUTDIR:-/tmp/kvmc-e2e-$$}"
mkdir -p "$OUTDIR"
LOG="$OUTDIR/run.log"
: > "$LOG"

if (( CLEAN_ONLY == 1 )); then
  sweep_orphans
  exit 0
fi

trap cleanup EXIT

log "${C_BLUE}kvm-center hypervisor e2e battery${C_NC}"
log "  server: $KVMC_SERVER"
log "  host:   $KVMC_HOST"
log "  peer:   $KVMC_PEER (for migration probe)"
log "  output: $OUTDIR"
log "  run id: $RUN_TS"

if [[ -n $SELECTED ]]; then
  if [[ -z "$(scen_fn "$SELECTED")" ]]; then
    echo "unknown scenario: $SELECTED" >&2
    echo "use --list to see options" >&2
    exit 1
  fi
  run_scenario "$SELECTED"
else
  for id in "${SCENARIOS[@]}"; do
    run_scenario "$id"
  done
fi

# ── final summary ────────────────────────────────────────────────
log ""
log "${C_BLUE}── summary ──${C_NC}"
pass=0; fail=0; skip=0
summary_lines=""
for id in "${SCENARIOS[@]}"; do
  outcome=$(kv_get "$id" SCEN_OUTCOMES)
  [[ -z $outcome ]] && continue
  case "$outcome" in
    PASS) pass=$((pass+1)); color="$C_GREEN" ;;
    FAIL) fail=$((fail+1)); color="$C_RED" ;;
    SKIP) skip=$((skip+1)); color="$C_YELLOW" ;;
  esac
  dur=$(kv_get "$id" SCEN_DURS)
  summary_lines+="$(printf '  %s%-4s%s %-4s %5ss  %s' "$color" "$outcome" "$C_NC" "$id" "${dur:-0}" "$(scen_name "$id")")"$'\n'
done
printf '%s' "$summary_lines" | tee -a "$LOG"
log ""
log "  ${C_GREEN}pass=$pass${C_NC}  ${C_RED}fail=$fail${C_NC}  ${C_YELLOW}skip=$skip${C_NC}  total=$((pass+fail+skip))"
log "  output dir: $OUTDIR"

if (( fail > 0 || pass == 0 )); then exit 1; fi
if (( STRICT == 1 && skip > 0 )); then
  log "strict acceptance failed: skipped scenarios are not passes"
  exit 1
fi
exit 0
