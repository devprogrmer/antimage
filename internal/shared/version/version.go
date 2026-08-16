// Package version carries build identity shared by all three binaries.
package version

// Version is overridden at build time via
// -ldflags "-X github.com/amyrm/antimage/internal/shared/version.Version=v0.1.0".
var Version = "dev"

// Protocol is the panel<->agent wire protocol version. Bump it whenever a
// change would make an older agent misbehave rather than fail loudly.
const Protocol = 1
