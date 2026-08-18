#!/usr/bin/env bash
# antimage node bootstrap. Idempotent: re-running upgrades in place.
set -euo pipefail

PANEL_URL=""
TOKEN=""
CA_FINGERPRINT=""
VERSION="latest"
STATE_DIR="/var/lib/antimage"
CONFIG_DIR="/etc/antimage"

die() { echo "error: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --panel)          PANEL_URL="$2"; shift 2 ;;
    --token)          TOKEN="$2"; shift 2 ;;
    --ca-fingerprint) CA_FINGERPRINT="$2"; shift 2 ;;
    --version)        VERSION="$2"; shift 2 ;;
    *) die "unknown argument: $1" ;;
  esac
done

# Argument validation runs before the root check on purpose. Checking flags
# needs no privilege, and someone who typos a flag should be told which flag
# is wrong, not told to re-run the whole thing under sudo. Ordering it the
# other way also makes the argument tests vacuous: every case would fail for
# the same reason, so deleting these two guards would break nothing visible.
[ -n "$PANEL_URL" ] || die "--panel is required"
[ -n "$TOKEN" ] || die "--token is required"

[ "$(id -u)" -eq 0 ] || die "must run as root"

# Refuse unsupported platforms rather than guessing at package names.
[ -r /etc/os-release ] || die "cannot read /etc/os-release"
. /etc/os-release
case "${ID}:${VERSION_ID%%.*}" in
  debian:11|debian:12|debian:13) ;;
  ubuntu:20|ubuntu:22|ubuntu:24) ;;
  *) die "unsupported OS ${ID} ${VERSION_ID}; antimage supports Debian 11+ and Ubuntu 20.04+" ;;
esac

case "$(uname -m)" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  *) die "unsupported architecture $(uname -m); antimage supports amd64 and arm64" ;;
esac

command -v systemctl >/dev/null 2>&1 || die "systemd is required"
command -v curl >/dev/null 2>&1 || die "curl is required"

# Fetch the CA fingerprint from the panel if it was not supplied. This is
# trust-on-first-use; --ca-fingerprint from an out-of-band channel is stronger.
if [ -z "$CA_FINGERPRINT" ]; then
  CA_FINGERPRINT="$(curl -fsSL "${PANEL_URL}/api/v1/ca-fingerprint")" \
    || die "could not fetch the CA fingerprint from ${PANEL_URL}"
fi
[ -n "$CA_FINGERPRINT" ] || die "empty CA fingerprint"

BIN_URL="${PANEL_URL}/download/antimage-node-linux-${ARCH}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "downloading antimage-node (${ARCH}) from ${PANEL_URL}"
curl -fsSL -o "$TMP/antimage-node" "$BIN_URL" || die "download failed"
curl -fsSL -o "$TMP/antimage-node.sha256" "${BIN_URL}.sha256" || die "checksum download failed"

( cd "$TMP" && echo "$(cat antimage-node.sha256)  antimage-node" | sha256sum -c - ) \
  || die "checksum mismatch; refusing to install"

install -d -m 0700 "$STATE_DIR" "$CONFIG_DIR"
install -m 0755 "$TMP/antimage-node" /usr/local/bin/antimage-node

# Only write node.yaml on first install: rewriting it would clobber the node
# id and force a re-enrollment on every upgrade.
if [ ! -f "$CONFIG_DIR/node.yaml" ]; then
  cat > "$CONFIG_DIR/node.yaml" <<EOF
panel_url: ${PANEL_URL}
token: ${TOKEN}
ca_fingerprint: ${CA_FINGERPRINT}
state_dir: ${STATE_DIR}
EOF
  chmod 0600 "$CONFIG_DIR/node.yaml"
fi

cat > /etc/systemd/system/antimage-node.service <<'EOF'
[Unit]
Description=antimage node agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/antimage-node --config /etc/antimage/node.yaml
Restart=always
RestartSec=5s
User=root
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now antimage-node
echo "antimage-node installed and started. Check: systemctl status antimage-node"
