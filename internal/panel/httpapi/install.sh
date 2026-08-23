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

# Enrollment token from first argument
ENROLLMENT_TOKEN="${1:-}"
if [[ -z "${ENROLLMENT_TOKEN}" ]]; then
    log_error "Enrollment token required"
    echo "Usage: curl -fsSL ${PANEL_URL}/install.sh | bash -s -- TOKEN" >&2
    exit 1
fi

# Validate token format (basic sanity check)
if [[ ! "${ENROLLMENT_TOKEN}" =~ ^[A-Za-z0-9_-]{16,}$ ]]; then
    log_error "Invalid enrollment token format"
    exit 1
fi

# Check root privileges
if [[ $EUID -ne 0 ]]; then
   log_error "This script must be run as root"
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

# Create systemd service
log_info "Creating systemd service..."
cat > /etc/systemd/system/${SERVICE_NAME}.service <<EOF
[Unit]
Description=Antimage VPN Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=antimage
Group=antimage
ExecStart=${BINARY_PATH} start
Restart=on-failure
RestartSec=5s
LimitNOFILE=65536

# Security hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${INSTALL_DIR}
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF

# Reload systemd
systemctl daemon-reload

# Enroll with panel
log_info "Enrolling with panel..."
if ! sudo -u antimage ./antimage-node enroll --token="${ENROLLMENT_TOKEN}" --panel="${PANEL_URL}"; then
    log_error "Enrollment failed"
    exit 1
fi

# Enable and start service
log_info "Enabling and starting service..."
systemctl enable ${SERVICE_NAME}
systemctl start ${SERVICE_NAME}

# Wait for service to stabilize
sleep 2

# Check service status
if systemctl is-active --quiet ${SERVICE_NAME}; then
    log_info "Installation complete. Node is running."
    log_info "Check status with: systemctl status ${SERVICE_NAME}"
    log_info "View logs with: journalctl -u ${SERVICE_NAME} -f"
else
    log_error "Service failed to start. Check logs with: journalctl -u ${SERVICE_NAME}"
    exit 1
fi
