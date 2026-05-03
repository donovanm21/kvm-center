#!/usr/bin/env bash
# One-shot first-boot setup for the kvm-center appliance.
#
# Runs once on the very first boot of the qcow2 (gated by
# /var/lib/kvm-center/.firstboot-done in the systemd unit). Generates:
#   - random admin password (logged to /etc/issue + /etc/motd so the
#     operator sees it whether they SSH in or open a libvirt console),
#   - random Mongo password (kept private at /etc/kvm-center/mongo.env,
#     mode 0600 root:root — kvm-center.service sources it).
#
# Idempotent: re-running it after the marker exists is a no-op.

set -euo pipefail

MARK=/var/lib/kvm-center/.firstboot-done
DATA=/var/lib/kvm-center
ETC=/etc/kvm-center

if [ -f "$MARK" ]; then
  echo "kvm-center firstboot: already complete, skipping"
  exit 0
fi

mkdir -p "$DATA" "$ETC"

# Random admin password (32 chars, base64). The kvm-center setup
# wizard will accept it and let the operator change it from the SPA.
ADMIN_PWD=$(openssl rand -base64 24 | tr -d '=+/' | head -c 32)
MONGO_PWD=$(openssl rand -base64 24 | tr -d '=+/' | head -c 32)

# Persist Mongo password where kvm-center.service can read it.
cat > "$ETC/mongo.env" <<EOF
KVM_CENTER_MONGO_PASSWORD=$MONGO_PWD
EOF
chmod 0600 "$ETC/mongo.env"

# Pre-seed the admin user at boot via a setup-wizard hint. The setup
# wizard at /setup will pick this up when the operator first opens
# the SPA.
cat > "$DATA/admin-bootstrap.json" <<EOF
{ "admin_password": "$ADMIN_PWD", "generated_at": "$(date -Iseconds)" }
EOF
chmod 0600 "$DATA/admin-bootstrap.json"

# Discover the IP libvirt gave us (DHCP-assigned via virtio-net).
PRIMARY_IP=$(hostname -I | awk '{print $1}')
[ -z "$PRIMARY_IP" ] && PRIMARY_IP="<dhcp-pending>"

# Write the credentials banner to /etc/issue (libvirt console) and
# the MOTD (visible on SSH login). Operator sees the password the
# moment they connect either way.
BANNER="
╔══════════════════════════════════════════════════════════╗
║  kvm-center appliance                                    ║
║                                                          ║
║  URL:            https://${PRIMARY_IP}:8190               ║
║  admin password: ${ADMIN_PWD}                             ║
║                                                          ║
║  Reset password from the SPA → Settings → Users.         ║
║  This banner clears on next boot.                        ║
╚══════════════════════════════════════════════════════════╝
"
echo "$BANNER" > /etc/issue
echo "$BANNER" > /etc/motd

# Mark complete.
touch "$MARK"

echo "kvm-center firstboot: complete"
echo "  admin URL:       https://$PRIMARY_IP:8190"
echo "  admin password:  $ADMIN_PWD"
