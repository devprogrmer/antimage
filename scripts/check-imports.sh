#!/usr/bin/env bash
# Enforces the adapter/panel boundary from the design spec section 3.
set -euo pipefail

if ! deps=$(go list -deps -f '{{.ImportPath}}' \
  github.com/amyrm/antimage/internal/node/adapter/... 2>&1); then
  echo "FAIL: go list failed while resolving internal/node/adapter dependencies."
  echo "$deps"
  exit 1
fi

violations=$(printf '%s\n' "$deps" | grep 'github.com/amyrm/antimage/internal/panel' || true)

if [ -n "$violations" ]; then
  echo "FAIL: internal/node/adapter must not depend on internal/panel."
  echo "Offending dependencies:"
  echo "$violations"
  exit 1
fi

if grep -rn "InsecureIgnoreHostKey" --include='*.go' . ; then
  echo "FAIL: InsecureIgnoreHostKey is banned (spec section 7.2)."
  exit 1
fi

echo "OK: import boundaries and SSH host-key policy clean."
