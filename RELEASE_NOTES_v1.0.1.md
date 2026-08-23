# antimage v1.0.1 - Production-Ready Public Release

**Release Date**: 2024-08-23  
**Tag**: v1.0.1  
**Commit**: b3c1f70

---

## Overview

This release transforms antimage into a professional, production-ready open-source project with a clean repository structure and zero-npm end-user installation.

**Key Achievement**: End users can now install antimage without Node.js, npm, or git—just download pre-built binaries from GitHub Releases.

---

## What's New

### 🎯 Zero-NPM Production Installation

- **Pre-built binaries** with embedded web UI available for download
- **No npm/Node.js required** for end users
- Production installation via direct binary download or curl installer
- npm/Node.js now development-only dependencies

### 🧹 Clean Repository Structure

- **Removed 90+ internal development reports** (PHASE*, MILESTONE*, etc.)
- **Organized documentation** into docs/ structure:
  - docs/protocols/ - Protocol implementation guides
  - docs/operations/ - Operational guides
  - docs/SECURITY.md - Security documentation
- **Professional public repository** ready for open-source community

### 🏗️ Production Build System

- **New build-release.sh script** creates production artifacts:
  1. Builds frontend (development step)
  2. Embeds frontend into Go binary via go:embed
  3. Compiles static binaries for Linux amd64/arm64
  4. Generates SHA256SUMS for verification
- **Reproducible builds** with version embedding
- **Verified checksums** for all release artifacts

### 🐛 Bug Fixes

- Fixed i18n duplicate keys in fa, ru, zh-CN, ar locales
- Fixed unsafe React hook dependencies in multiple components
- Added BulkActions component i18n support with RTL fixes
- Synced embedded install script to resolve test failure

### 📚 Documentation Improvements

- **Production-first README** with clear separation from development
- **Enhanced installer documentation** with security hardening details
- **Localized READMEs** for multiple languages

---

## Installation

### Production Installation (Recommended)

Download pre-built binaries from [GitHub Releases](https://github.com/devprogrmer/antimage/releases/tag/v1.0.1):

```bash
# Linux x86-64
wget https://github.com/devprogrmer/antimage/releases/download/v1.0.1/antimage-panel-linux-amd64
wget https://github.com/devprogrmer/antimage/releases/download/v1.0.1/antimage-ctl-linux-amd64
wget https://github.com/devprogrmer/antimage/releases/download/v1.0.1/SHA256SUMS

# Verify checksums
sha256sum -c SHA256SUMS --ignore-missing

# Make executable
chmod +x antimage-panel-linux-amd64 antimage-ctl-linux-amd64

# Install
sudo mv antimage-panel-linux-amd64 /usr/local/bin/antimage-panel
sudo mv antimage-ctl-linux-amd64 /usr/local/bin/antimage-ctl
```

**No npm/Node.js required!** The binaries contain the embedded web UI.

### Development Installation

For contributors:

```bash
git clone https://github.com/devprogrmer/antimage.git
cd antimage
cd web && npm install && npm run build && cd ..
go build -o bin/antimage-panel ./cmd/antimage-panel
```

---

## Release Artifacts

All binaries are statically compiled (`CGO_ENABLED=0`) for maximum portability:

- `antimage-panel-linux-amd64` - Control plane panel (21 MB)
- `antimage-panel-linux-arm64` - Control plane panel (20 MB)
- `antimage-node-linux-amd64` - Node agent (12 MB)
- `antimage-node-linux-arm64` - Node agent (11 MB)
- `antimage-ctl-linux-amd64` - CLI tool (8.6 MB)
- `antimage-ctl-linux-arm64` - CLI tool (8.2 MB)
- `SHA256SUMS` - Checksums for verification

---

## Verification Status

### ✅ Tests Passing

- **Go Tests**: All 38 packages passing (400+ tests)
- **Frontend Tests**: 3 test files, 16 tests passing
- **Go Vet**: Clean (no issues)
- **Build**: Successful for Linux amd64/arm64

### ✅ CI Status

- GitHub Actions: All checks passing
- Frontend tests: Passing
- Go tests: Passing
- Linting: Clean

---

## What's Unchanged

This is a **repository cleanup and packaging release**—no functional changes:

- ✅ All 5 protocol adapters still working
- ✅ Xray enforcement still fully functional
- ✅ RBAC, authentication, audit logging unchanged
- ✅ Quota enforcement and connection limits unchanged
- ✅ Subscription generation unchanged
- ✅ Deployment orchestration unchanged

---

## Known Limitations

Same as v1.0.0—see [README.md](README.md#known-limitations) for details:

- **Xray speed limits**: tc-based only (upSpeed/downSpeed config ignored)
- **Hysteria2**: No runtime enforcement yet (configuration only)
- **WireGuard**: No quota enforcement (kernel VPN limitation)
- **L2TP/IPsec**: No runtime enforcement yet (configuration only)

---

## Upgrade from v1.0.0

**This is a drop-in replacement:**

```bash
# Stop panel
sudo systemctl stop antimage-panel

# Download new binary
wget https://github.com/devprogrmer/antimage/releases/download/v1.0.1/antimage-panel-linux-amd64
sha256sum -c SHA256SUMS --ignore-missing
sudo mv antimage-panel-linux-amd64 /usr/local/bin/antimage-panel
chmod +x /usr/local/bin/antimage-panel

# Start panel
sudo systemctl start antimage-panel
```

**No database migration required.** The binary is backward-compatible.

---

## Breaking Changes

**None.** This release is 100% backward-compatible with v1.0.0.

---

## For Contributors

### Building from Source

```bash
# Full release build (requires npm for frontend build)
./scripts/build-release.sh v1.0.1

# Development build
cd web && npm install && npm run build && cd ..
go build -o bin/antimage-panel ./cmd/antimage-panel
```

### Repository Structure

The repository is now clean and professional:

- Root contains only essential project files
- Internal reports removed (90+ files)
- Documentation organized in docs/
- Clear separation between production and development

---

## Credits

Built with ❤️ by the antimage team.

Special thanks to all contributors and testers.

---

## Links

- **Repository**: https://github.com/devprogrmer/antimage
- **Release**: https://github.com/devprogrmer/antimage/releases/tag/v1.0.1
- **Documentation**: https://github.com/devprogrmer/antimage/blob/master/README.md
- **Issues**: https://github.com/devprogrmer/antimage/issues
- **License**: MIT
