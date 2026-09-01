#!/bin/bash
# Antimage node bootstrap installer
# Fetches the node binary, verifies its checksum, and enrolls with the panel.
# Usage: curl -fsSL https://panel.example.com/install.sh | bash -s -- ENROLLMENT_TOKEN

set -euo pipefail

# Configuration
PANEL_URL="${PANEL_URL:-https://panel.example.com}"
BINARY_URL="${PANEL_URL}/download/antimage-node"
CHECKSUM_URL="${PANEL_URL}/download/antimage-node.sha256"
INSTALL_DIR="/opt/antimage"
BINARY_PATH="${INSTALL_DIR}/antimage-node"
SERVICE_NAME="antimage-node"
# Where antimage-node itself looks, by default (cmd/antimage-node/main.go's
# -config flag) and what every README's manual-install fallback already
# documents -- state_dir here is not a free choice, it is what makes that
# fallback and this script agree.
CONFIG_DIR="/etc/antimage"
CONFIG_PATH="${CONFIG_DIR}/node.yaml"
STATE_DIR="/var/lib/antimage"

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

# Argument parsing.
#
# Both forms are accepted: the piped one-liner passes the token positionally,
# while --panel/--token is what an operator types by hand and what the
# definition-of-done gate asserts. Unknown arguments are rejected rather than
# ignored: silently dropping a misspelt flag installs a node pointed at the
# wrong panel, which is discovered much later and by somebody else.
ENROLLMENT_TOKEN=""
PANEL_FLAG=""
CA_FINGERPRINT=""
EXPLICIT_FLAGS=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --panel)
            EXPLICIT_FLAGS=1
            PANEL_FLAG="${2:-}"
            shift 2 || true
            ;;
        --token)
            EXPLICIT_FLAGS=1
            ENROLLMENT_TOKEN="${2:-}"
            shift 2 || true
            ;;
        --ca-fingerprint)
            # Optional. The panel already knows this and passes it (both the
            # plain enroll-token command and the SSH-bootstrap command include
            # it), which saves the extra round trip below. It stays optional
            # so the hand-typed form this --help text advertises keeps working
            # without the operator hunting the value down first.
            CA_FINGERPRINT="${2:-}"
            shift 2 || true
            ;;
        --help|-h)
            echo "Usage: curl -fsSL ${PANEL_URL}/install.sh | bash -s -- TOKEN"
            echo "   or: install.sh --panel https://panel.example.com --token TOKEN [--ca-fingerprint SHA256]"
            exit 0
            ;;
        -*)
            log_error "unknown argument: $1"
            exit 1
            ;;
        *)
            ENROLLMENT_TOKEN="$1"
            shift
            ;;
    esac
done

# When flags are used, both are required: a node enrolled against the default
# panel because --panel was omitted is worse than a refusal.
if [[ "${EXPLICIT_FLAGS}" -eq 1 ]]; then
    if [[ -z "${PANEL_FLAG}" ]]; then
        log_error "--panel is required"
        exit 1
    fi
    if [[ -z "${ENROLLMENT_TOKEN}" ]]; then
        log_error "--token is required"
        exit 1
    fi
    PANEL_URL="${PANEL_FLAG}"
    BINARY_URL="${PANEL_URL}/download/antimage-node"
    CHECKSUM_URL="${PANEL_URL}/download/antimage-node.sha256"
fi

if [[ -z "${ENROLLMENT_TOKEN}" ]]; then
    log_error "Enrollment token required"
    echo "Usage: curl -fsSL ${PANEL_URL}/install.sh | bash -s -- TOKEN" >&2
    exit 1
fi

# Check root privileges
if [[ $EUID -ne 0 ]]; then
   log_error "This script must run as root"
   exit 1
fi

# Validate token format (basic sanity check)
if [[ ! "${ENROLLMENT_TOKEN}" =~ ^[A-Za-z0-9_-]{16,}$ ]]; then
    log_error "Invalid enrollment token format"
    exit 1
fi

# Detect OS and architecture
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "${ARCH}" in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *)
        log_error "Unsupported architecture: ${ARCH}"
        exit 1
        ;;
esac

# Verify supported OS
case "${OS}" in
    linux) ;;
    *)
        log_error "Unsupported operating system: ${OS}"
        exit 1
        ;;
esac

log_info "Installing antimage-node for ${OS}-${ARCH}"

# Check required commands
for cmd in curl sha256sum; do
    if ! command -v "${cmd}" >/dev/null 2>&1; then
        log_error "Required command not found: ${cmd}"
        exit 1
    fi
done

# Create installation directory with secure permissions
log_info "Creating installation directory..."
mkdir -p "${INSTALL_DIR}"
chmod 755 "${INSTALL_DIR}"
cd "${INSTALL_DIR}"

# Download binary and checksum with timeout
log_info "Downloading binary..."
if ! curl -fsSL --max-time 300 -o antimage-node "${BINARY_URL}"; then
    log_error "Failed to download binary"
    exit 1
fi

log_info "Downloading checksum..."
if ! curl -fsSL --max-time 30 -o antimage-node.sha256 "${CHECKSUM_URL}"; then
    log_error "Failed to download checksum"
    rm -f antimage-node
    exit 1
fi

# Verify checksum format (should be: hash  filename)
if ! grep -qE '^[a-f0-9]{64}  antimage-node$' antimage-node.sha256; then
    log_error "Invalid checksum file format"
    rm -f antimage-node antimage-node.sha256
    exit 1
fi

# Verify checksum
log_info "Verifying checksum..."
if ! sha256sum -c antimage-node.sha256 >/dev/null 2>&1; then
    log_error "Checksum verification failed - binary may be corrupted or tampered"
    rm -f antimage-node antimage-node.sha256
    exit 1
fi

log_info "Checksum verified successfully"

# Make executable with secure permissions
chmod 755 antimage-node

# Create dedicated user if not exists
if ! id -u antimage >/dev/null 2>&1; then
    log_info "Creating antimage system user..."
    useradd --system --no-create-home --shell /bin/false antimage
fi

# Set ownership
chown root:root antimage-node
chown antimage:antimage "${INSTALL_DIR}"

# Fetch the panel's CA fingerprint if the caller did not already supply one.
# node.yaml pins against it so a hijacked DNS record cannot impersonate the
# panel; refusing to start without it is agent.LoadConfig's own rule, not a
# choice this script is adding.
if [[ -z "${CA_FINGERPRINT}" ]]; then
    log_info "Fetching panel CA fingerprint..."
    if ! CA_FINGERPRINT="$(curl -fsSL --max-time 30 "${PANEL_URL}/api/v1/ca-fingerprint")"; then
        log_error "Failed to fetch panel CA fingerprint"
        exit 1
    fi
    if [[ -z "${CA_FINGERPRINT}" ]]; then
        log_error "Panel returned an empty CA fingerprint"
        exit 1
    fi
fi

# Write node configuration. There is no "antimage-node enroll" command --
# enrollment happens automatically, inside the long-running process, the
# first time it starts and finds no certificate yet in state_dir (see
# loadOrEnroll in cmd/antimage-node/main.go). node.yaml is the only input
# that step takes, which is why it is written before the service ever starts.
log_info "Writing node configuration..."
mkdir -p "${CONFIG_DIR}"
chmod 700 "${CONFIG_DIR}"
mkdir -p "${STATE_DIR}"
chown antimage:antimage "${STATE_DIR}"
chmod 700 "${STATE_DIR}"
cat > "${CONFIG_PATH}" <<EOF
panel_url: "${PANEL_URL}"
token: "${ENROLLMENT_TOKEN}"
ca_fingerprint: "${CA_FINGERPRINT}"
state_dir: "${STATE_DIR}"
EOF
chown antimage:antimage "${CONFIG_DIR}" "${CONFIG_PATH}"
chmod 600 "${CONFIG_PATH}"

# Create systemd service
log_info "Creating systemd service..."
cat > /etc/systemd/system/antimage-node.service <<EOF
[Unit]
Description=Antimage VPN Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=antimage
Group=antimage
ExecStart=${BINARY_PATH} --config ${CONFIG_PATH}
Restart=on-failure
RestartSec=5s
LimitNOFILE=65536

# Security hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${INSTALL_DIR} ${CONFIG_DIR} ${STATE_DIR}
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF

# Reload systemd
systemctl daemon-reload

# Enable and start service
log_info "Enabling and starting service..."
systemctl enable ${SERVICE_NAME}
systemctl start ${SERVICE_NAME}

# Wait for enrollment to complete. The service enrolls automatically on its
# first start, so node.crt appearing in state_dir -- not merely the process
# staying up -- is the actual proof it worked; a bad token or an unreachable
# panel exits the process too, and systemd restarting it on a 5s backoff can
# look "active" in between attempts even though enrollment never succeeded.
log_info "Waiting for enrollment..."
ENROLLED=0
for _ in $(seq 1 30); do
    if [[ -f "${STATE_DIR}/node.crt" ]]; then
        ENROLLED=1
        break
    fi
    sleep 1
done

if [[ "${ENROLLED}" -eq 1 ]] && systemctl is-active --quiet ${SERVICE_NAME}; then
    log_info "Installation complete. Node is enrolled and running."
    log_info "Check status with: systemctl status ${SERVICE_NAME}"
    log_info "View logs with: journalctl -u ${SERVICE_NAME} -f"
else
    log_error "Enrollment did not complete. Check logs with: journalctl -u ${SERVICE_NAME}"
    exit 1
fi
