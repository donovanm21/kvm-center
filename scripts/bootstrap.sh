#!/usr/bin/env bash
# bootstrap.sh — Operational launcher for KVM Center.
# Manages: server, stack, deploy, and agent installation.
#
# Usage: ./scripts/bootstrap.sh [--server | --stack | --deploy | --deploy-agent]
#
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_DIR"

usage() {
  cat <<EOF
KVM Center bootstrap

Usage: $0 <mode> [options]

Modes:
  --interactive     First-boot installer — prompts for Mongo creds, cluster
                    domain, etc., writes .env (0600), then runs --deploy
  --server          Run the server locally (go run)
  --agent           Run the agent locally (go run, for testing)
  --stack           Build and start the full Docker stack
  --deploy          Pull latest git, rebuild Docker stack, push agent updates
  --update-agents   Push the latest agent binary to every known hypervisor
  --logs            Tail Docker stack logs
  --status          Show Docker container status
  --stop            Stop Docker stack
  --deploy-agent    Interactive: build agent binaries and guide installation

Options for --server:
  --addr  <addr>    Listen address (default: :8090)
  --data  <path>    Data file path (default: ./data.json)

Options for --agent:
  --server-url <u>  Control plane URL (default: http://localhost:8090)
  --port <port>     Command port (default: 8081)

EOF
  exit 1
}

[[ $# -eq 0 ]] && usage

MODE="$1"; shift

case "$MODE" in

  --server)
    ADDR="${1:-:8090}"
    DATA="${2:-$REPO_DIR/data.json}"
    echo "Starting KVM Center server on $ADDR..."
    KVM_CENTER_ADDR="$ADDR" KVM_CENTER_DATA="$DATA" go run ./server
    ;;

  --agent)
    SERVER="${1:-http://localhost:8090}"
    PORT="${2:-8081}"
    echo "Starting KVM Center agent → $SERVER..."
    go run ./agent -server "$SERVER" -port "$PORT"
    ;;

  --stack)
    echo "Building and starting KVM Center stack..."
    # Pass the current commit SHA as a build arg so the Dockerfile's
    # test + build stages pick up a fresh layer on every new commit.
    # Without this, BuildKit can reuse a stale `go test ./...` layer
    # from a prior state and report green even when the current source
    # has broken tests — we hit this on v5.13.1. Commit SHA is already
    # HEAD at this point because --deploy did a `git reset --hard`.
    GIT_SHA=$(git rev-parse HEAD)
    docker compose build --build-arg GIT_SHA="$GIT_SHA"
    docker compose up -d
    echo ""
    IP=$(hostname -I 2>/dev/null | awk '{print $1}' || true)
    echo "  Web UI → https://${IP:-localhost}:8190"
    echo "  API    → https://${IP:-localhost}:8190/api"
    ;;

  --interactive)
    # v5.20: first-boot installer. Every prompt has a safe default so
    # the operator can mash Enter and still end up with a working
    # stack; the web wizard (Settings → General + the /setup redirect)
    # handles the rest. .env is written with 0600 and gitignored, so
    # secrets never reach the repo.
    echo "=== kvm-center interactive installer ==="

    # Prefer whiptail if available (RHEL/Rocky/Debian ship it) for
    # dialog-style prompts; fall back to plain `read` so the script
    # still works on a minimal container.
    HAVE_WHIPTAIL=0
    if command -v whiptail >/dev/null 2>&1; then HAVE_WHIPTAIL=1; fi

    prompt() {
      # $1 = title, $2 = message, $3 = default, sets REPLY
      local title="$1" msg="$2" def="$3"
      if [[ $HAVE_WHIPTAIL -eq 1 ]]; then
        REPLY=$(whiptail --title "$title" --inputbox "$msg" 10 70 "$def" 3>&1 1>&2 2>&3) || return 1
      else
        read -rp "$msg [$def]: " REPLY
        REPLY="${REPLY:-$def}"
      fi
    }
    ask_yesno() {
      local title="$1" msg="$2"
      if [[ $HAVE_WHIPTAIL -eq 1 ]]; then
        whiptail --title "$title" --yesno "$msg" 10 70
        return $?
      else
        read -rp "$msg [y/N]: " a
        [[ "$a" == "y" || "$a" == "Y" ]]
      fi
    }
    # menu title msg tag1 label1 tag2 label2 ... — sets REPLY to the picked tag
    menu() {
      local title="$1" msg="$2"; shift 2
      if [[ $HAVE_WHIPTAIL -eq 1 ]]; then
        REPLY=$(whiptail --title "$title" --menu "$msg" 18 78 8 "$@" 3>&1 1>&2 2>&3) || return 1
      else
        echo "$msg"
        local items=("$@") i=1
        local tags=()
        while [[ $# -gt 0 ]]; do
          tags+=("$1")
          echo "  $i) $2"
          shift 2
          i=$((i + 1))
        done
        read -rp "Pick 1-${#tags[@]}: " n
        [[ "$n" =~ ^[0-9]+$ ]] && (( n >= 1 && n <= ${#tags[@]} )) || return 1
        REPLY="${tags[$((n - 1))]}"
      fi
    }

    # Detect what's already on disk so a re-run doesn't silently clobber
    # a working install. Three outcomes the operator gets to pick from:
    #   * keep      — preserve .env + Mongo volume, just redeploy code
    #   * reconfig  — regenerate .env (new Mongo password → WILL lock
    #                 you out of the existing Mongo volume; we warn)
    #   * fresh     — nuke containers + named volumes + .env, start over
    HAS_ENV=0; [[ -f .env ]] && HAS_ENV=1
    HAS_MONGO=0
    if command -v docker >/dev/null 2>&1; then
      docker volume inspect kvm-center_kvm-center-mongo-data >/dev/null 2>&1 && HAS_MONGO=1
    fi
    STACK_UP=0
    if command -v docker >/dev/null 2>&1; then
      docker inspect kvm-center-server >/dev/null 2>&1 && STACK_UP=1
    fi

    if [[ $HAS_ENV -eq 1 || $HAS_MONGO -eq 1 || $STACK_UP -eq 1 ]]; then
      echo "Detected existing install: .env=$HAS_ENV, mongo-volume=$HAS_MONGO, stack-running=$STACK_UP"
      menu "kvm-center — existing install" \
           "This host already has kvm-center state. What should I do?" \
           "keep"     "Keep everything and run --deploy (safest, no data loss)" \
           "reconfig" "Regenerate .env — NEW Mongo password (locks out the existing volume)" \
           "fresh"    "Wipe containers + volumes + .env and start over (destroys ALL data)" \
           "cancel"   "Cancel — don't touch anything" \
        || { echo "aborted"; exit 1; }

      case "$REPLY" in
        keep)
          "$REPO_DIR/scripts/bootstrap.sh" --deploy
          exit $?
          ;;
        reconfig)
          if ! ask_yesno "kvm-center" \
               "Regenerating .env will set a NEW Mongo password. The existing Mongo volume was initialised with the OLD one and WILL NOT authenticate — you'll lose all state in Mongo (VMs, snapshots, audit, users). Continue?"; then
            echo "Cancelled."
            exit 0
          fi
          if [[ $HAS_ENV -eq 1 ]]; then
            cp .env ".env.bak.$(date +%s)"
            echo "Backed up existing .env to .env.bak.$(date +%s)."
          fi
          # Stop the stack so the next --deploy starts clean against
          # the fresh Mongo volume we're about to wipe.
          if [[ $STACK_UP -eq 1 ]]; then docker compose down; fi
          if [[ $HAS_MONGO -eq 1 ]]; then docker volume rm -f kvm-center_kvm-center-mongo-data >/dev/null || true; fi
          # Fall through to the prompts below.
          ;;
        fresh)
          if ! ask_yesno "kvm-center — DESTRUCTIVE" \
               "This will:
  • docker compose down -v (removes ALL kvm-center containers and volumes)
  • delete .env and any .env.bak.* files
  • drop the locally generated TLS material

VM / hypervisor data on your hypervisors is NOT touched. Proceed?"; then
            echo "Cancelled."
            exit 0
          fi
          docker compose down -v 2>&1 | grep -vE '^\s*$' || true
          # docker compose down -v handles named volumes declared in
          # docker-compose.yml; be explicit about kvm-center-tls and
          # -data in case they were created under a different project
          # name on a previous deploy.
          for v in kvm-center_kvm-center-mongo-data kvm-center_kvm-center-data kvm-center_kvm-center-tls; do
            docker volume rm -f "$v" >/dev/null 2>&1 || true
          done
          rm -f .env .env.bak.*
          echo "Clean slate. Proceeding with fresh install…"
          ;;
        cancel)
          echo "Cancelled."
          exit 0
          ;;
      esac
    fi

    prompt "MongoDB" "MongoDB username" "kvmcenter" || { echo aborted; exit 1; }
    MONGO_USER="$REPLY"

    prompt "MongoDB" "MongoDB password (empty = auto-generate)" "" || { echo aborted; exit 1; }
    MONGO_PW="$REPLY"
    if [[ -z "$MONGO_PW" ]]; then
      MONGO_PW=$(openssl rand -base64 33 | tr -d '=+/' | head -c 40)
      echo "Generated a 40-char Mongo password (stored in .env mode 0600)."
    fi

    prompt "Cluster" "Cluster DNS domain (used as a hypervisor cert SAN)" "cluster.local" || { echo aborted; exit 1; }
    CLUSTER_DOMAIN="$REPLY"

    # Public URL threads into every install snippet on the Deployment
    # page and webhook deep links. Blank is safe — the SPA falls back
    # to window.location. Admins can set/change this later in Settings.
    # hostname -I is Linux-only; on macOS (operator running --interactive
    # locally to test) it fails and pipefail would kill the installer.
    HOST_IP=$(hostname -I 2>/dev/null | awk '{print $1}' || true)
    prompt "Public URL" "Canonical URL clients use to reach this server (blank to skip)" "${HOST_IP:+https://${HOST_IP}:8190}" || { echo aborted; exit 1; }
    PUBLIC_URL="$REPLY"

    umask 077
    {
      echo "# Auto-generated by scripts/bootstrap.sh --interactive on $(date -u +%Y-%m-%dT%H:%M:%SZ)."
      echo "# Kept out of git via .gitignore. Back up off-host for disaster recovery."
      echo "KVM_MONGO_USER=${MONGO_USER}"
      echo "KVM_MONGO_PASSWORD=${MONGO_PW}"
      echo "KVM_CENTER_CLUSTER_DOMAIN=${CLUSTER_DOMAIN}"
      [[ -n "$PUBLIC_URL" ]] && echo "KVM_CENTER_PUBLIC_URL=${PUBLIC_URL}"
    } > .env
    chmod 0600 .env
    # Clear the captured password from the shell env so it doesn't
    # leak into process listings or later `env` dumps.
    unset MONGO_PW REPLY
    echo "Wrote .env (0600)."

    if ask_yesno "kvm-center" "Build and deploy the stack now?"; then
      "$REPO_DIR/scripts/bootstrap.sh" --deploy
      echo
      echo "=============================================================="
      echo "  kvm-center is up. Open https://$(hostname -I 2>/dev/null | awk '{print $1}'):8190/"
      echo "  On first load you'll create the admin account and step"
      echo "  through the setup wizard — every step is skippable."
      echo "=============================================================="
    else
      echo "Skipping deploy. Re-run: ./scripts/bootstrap.sh --deploy"
    fi
    ;;

  --deploy)
    echo "Pulling latest changes and redeploying..."
    git fetch origin
    git reset --hard origin/main
    # v5.1+: compose requires KVM_MONGO_PASSWORD in .env. Generate a
    # strong random password on first run so the operator never has to
    # think about it. Subsequent runs leave the file alone — Mongo's
    # volume holds the user that was created against this password,
    # and rotating it requires a separate mongo-side dance.
    if [ ! -f .env ]; then
      echo "First-time deploy: generating .env with a fresh Mongo password…"
      pw=$(openssl rand -base64 33 | tr -d '=+/' | head -c 40)
      cat > .env <<EOF_ENV
# Auto-generated by scripts/bootstrap.sh on first --deploy.
# Rotating requires updating the Mongo user via mongosh, then this file.
KVM_MONGO_USER=kvmcenter
KVM_MONGO_PASSWORD=${pw}
EOF_ENV
      chmod 0600 .env
      echo "Wrote .env (0600). Keep this file safe and off-host for disaster recovery."
    fi
    # Pass the current commit SHA as a build arg so the Dockerfile's
    # test + build stages pick up a fresh layer on every new commit.
    # Without this, BuildKit can reuse a stale `go test ./...` layer
    # from a prior state and report green even when the current source
    # has broken tests — we hit this on v5.13.1. Commit SHA is already
    # HEAD at this point because --deploy did a `git reset --hard`.
    GIT_SHA=$(git rev-parse HEAD)
    docker compose build --build-arg GIT_SHA="$GIT_SHA"
    docker compose up -d
    echo "Stack deployed. Pushing fresh agent binary to each hypervisor…"
    "$REPO_DIR/scripts/bootstrap.sh" --update-agents || \
      echo "WARN: agent push had errors — run './scripts/bootstrap.sh --update-agents' manually"
    echo "Deploy complete."
    ;;

  --update-agents)
    # Roll the latest agent binary out to every host the server knows
    # about. Host discovery order:
    #   1. KVM_HVNODE_IPS env (space-separated)
    #   2. data.json inside the kvm-center-server container (no auth needed)
    #   3. /api/hosts (requires the server to allow unauth read; usually fails)
    # SSH auth: passwordless preferred, or sshpass + KVM_HVNODE_SSH_PASS.
    BUILD_URL="${KVM_CENTER_BUILD_URL:-http://localhost:8090/downloads}"
    SSH_USER="${KVM_HVNODE_SSH_USER:-root}"
    if ! command -v curl >/dev/null 2>&1; then
      echo "ERROR: --update-agents needs curl" >&2
      exit 1
    fi
    HOSTS=""
    if [[ -n "${KVM_HVNODE_IPS:-}" ]]; then
      HOSTS="$KVM_HVNODE_IPS"
    elif command -v docker >/dev/null 2>&1; then
      # v5.3+: prefer Mongo (primary backend since v5.1). mongosh runs
      # inside the mongo container and picks up the root creds from
      # /opt/kvm-center/.env. Falls back to data.json for rollback cases
      # where Mongo is temporarily disabled.
      if docker inspect kvm-center-mongo >/dev/null 2>&1 && [[ -f /opt/kvm-center/.env ]]; then
        mongo_pw=$(grep '^KVM_MONGO_PASSWORD=' /opt/kvm-center/.env | cut -d= -f2-)
        mongo_user=$(grep '^KVM_MONGO_USER=' /opt/kvm-center/.env | cut -d= -f2-)
        mongo_user=${mongo_user:-kvmcenter}
        if [[ -n "$mongo_pw" ]]; then
          HOSTS=$(docker exec kvm-center-mongo mongosh --quiet -u "$mongo_user" -p "$mongo_pw" \
            --authenticationDatabase admin kvm-center \
            --eval 'db.hosts.find({}, {ip: 1, _id: 0}).toArray().map(h => h.ip).join(" ")' \
            2>/dev/null || true)
        fi
      fi
      if [[ -z "$HOSTS" ]] && command -v jq >/dev/null 2>&1 && \
         docker inspect kvm-center-server >/dev/null 2>&1; then
        HOSTS=$(docker exec kvm-center-server cat /var/lib/kvm-center/data.json 2>/dev/null \
                | jq -r '.hosts[].ip' 2>/dev/null || true)
      fi
    fi
    if [[ -z "$HOSTS" ]] && command -v jq >/dev/null 2>&1; then
      SERVER_API="${KVM_CENTER_API:-http://localhost:8090}"
      HOSTS=$(curl -fsS "$SERVER_API/api/hosts" 2>/dev/null | jq -r '.[].ip' 2>/dev/null || true)
    fi
    if [[ -z "$HOSTS" ]]; then
      echo "No hosts to update. Set KVM_HVNODE_IPS=\"ip1 ip2 …\" or run from the docker host." >&2
      exit 0
    fi
    for ip in $HOSTS; do
      echo "── $ip ───────────────────────────"
      SSH_CMD=(ssh -o StrictHostKeyChecking=no -o ConnectTimeout=8 "$SSH_USER@$ip"
        "curl -fsSL ${BUILD_URL}/update-agent.sh | KVM_CENTER_BUILD_URL=${BUILD_URL} bash")
      if [[ -n "${KVM_HVNODE_SSH_PASS:-}" ]] && command -v sshpass >/dev/null 2>&1; then
        sshpass -p "$KVM_HVNODE_SSH_PASS" "${SSH_CMD[@]}" || echo "  ✕ failed on $ip"
      else
        "${SSH_CMD[@]}" || echo "  ✕ failed on $ip (set KVM_HVNODE_SSH_PASS for password auth)"
      fi
    done
    ;;

  --logs)
    docker compose logs -f
    ;;

  --status)
    docker compose ps
    ;;

  --stop)
    docker compose down
    ;;

  --deploy-agent)
    echo "=== KVM Center Agent Deployment ==="
    echo "Building agent binaries..."
    mkdir -p dist
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/kvm-center-agent-amd64 ./agent
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64  go build -ldflags="-s -w" -o dist/kvm-center-agent-arm64  ./agent
    cp scripts/install-agent.sh dist/

    echo ""
    echo "Binaries ready in dist/"
    echo ""
    echo "On each hypervisor, run:"
    echo "  scp dist/kvm-center-agent-amd64 dist/install-agent.sh root@<hypervisor>:/tmp/"
    echo "  ssh root@<hypervisor> 'chmod +x /tmp/kvm-center-agent-amd64 /tmp/install-agent.sh && /tmp/install-agent.sh --server https://<server-ip>:8190'"
    ;;

  --help|-h)
    usage
    ;;

  *)
    echo "Unknown mode: $MODE"
    usage
    ;;
esac
