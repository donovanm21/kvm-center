#!/usr/bin/env bash
# install.sh — install the kvm-center Claude skill.
#
# Writes SKILL.md + TOOL.md to ~/.claude/skills/kvm-center/.
# Safe to re-run (overwrites the files).
#
# Usage:
#   curl -fsSL https://<server>:8190/downloads/kvm-center-skill.tar.gz |
#     tar xzf - -C /tmp/kvmc-skill
#   bash /tmp/kvmc-skill/install.sh
#
# Or, if unpacked alongside this file:
#   bash ./install.sh

set -euo pipefail

DEST="${HOME}/.claude/skills/kvm-center"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

mkdir -p "$DEST"
cp "$HERE/SKILL.md" "$DEST/SKILL.md"
cp "$HERE/TOOL.md"  "$DEST/TOOL.md"
if [[ -f "$HERE/plugin.yaml" ]]; then
  cp "$HERE/plugin.yaml" "$DEST/plugin.yaml"
fi

echo "Installed kvm-center skill to $DEST"
echo ""
echo "Next steps:"
echo "  1. Make sure ~/.config/kvm-center/config.json has your API token,"
echo "     or export KVMCTL_SERVER + KVMCTL_TOKEN."
echo "  2. In a new Claude Code conversation, ask e.g.:"
echo "       'list VMs on host-b'"
echo "       'is the control plane healthy?'"
echo "       'show recent migrations'"
echo "  3. Claude will load SKILL.md lazily when the conversation"
echo "     matches a kvm-center intent."
