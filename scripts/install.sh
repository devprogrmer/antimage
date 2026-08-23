#!/bin/bash
# Antimage node bootstrap installer
# Fetches the node binary, verifies its checksum, and enrolls with the panel.
# Usage: curl -fsSL https://panel.example.com/install.sh | bash -s -- ENROLLMENT_TOKEN

set -euo pipefail

PANEL_URL="${PANEL_URL:-https://panel.example.com}"
BINARY_URL="${PANEL_URL}/download/antimage-node"
CHECKSUM_URL="${PANEL_URL}/download/antimage-node.sha256"
INSTALL_DIR="/opt/antimage"
BINARY_PATH="${INSTALL_DIR}/antimage-node"

# Enrollment token from first argument
ENROLLMENT_TOKEN="${1:-}"
if [[ -z "${ENROLLMENT_TOKEN}" ]]; then
    echo "Error: enrollment token required" >&2
    echo "Usage: curl -fsSL ${PANEL_URL}/install.sh | bash -s -- TOKEN" >&2
    exit 1
fi

# Detect OS and architecture
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "${ARCH}" in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *)
        echo "Error: unsupported architecture ${ARCH}" >&2
        exit 1
        ;;
esac

echo "Installing antimage-node for ${OS}-${ARCH}"

# Create installation directory
mkdir -p "${INSTALL_DIR}"
cd "${INSTALL_DIR}"

# Download binary and checksum
echo "Downloading binary..."
curl -fsSL -o antimage-node "${BINARY_URL}"
curl -fsSL -o antimage-node.sha256 "${CHECKSUM_URL}"

# Verify checksum
echo "Verifying checksum..."
sha256sum -c antimage-node.sha256

# Make executable
chmod +x antimage-node

# Enroll with panel
echo "Enrolling with panel..."
./antimage-node enroll --token="${ENROLLMENT_TOKEN}" --panel="${PANEL_URL}"

echo "Installation complete. Starting node..."
./antimage-node start
