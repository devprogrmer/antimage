#!/usr/bin/env bash
# Argument and platform-guard tests for install.sh. No network, no root.
#
# Each failure case asserts the specific message install.sh printed, not just
# a non-zero exit. Checking only the exit status made three of these vacuous:
# every invocation died at the same guard, so deleting the --panel and --token
# checks entirely would still have left the suite green.
set -uo pipefail

SCRIPT="$(dirname "$0")/install.sh"
fails=0

fail() {
  echo "FAIL: $*"
  fails=$((fails + 1))
}

# expect_fail DESC EXPECTED_STDERR_SUBSTRING CMD...
expect_fail() {
  local desc="$1" want="$2"
  shift 2
  local output status
  # stderr is captured; stdout is discarded, so a message printed to the wrong
  # stream cannot satisfy the assertion.
  output="$("$@" 2>&1 >/dev/null)"
  status=$?
  if [ "$status" -eq 0 ]; then
    fail "$desc — expected a non-zero exit, got 0"
    return
  fi
  case "$output" in
    *"$want"*) echo "ok: $desc" ;;
    *) fail "$desc — expected stderr to contain '$want', got: ${output:-<empty>}" ;;
  esac
}

expect_fail "rejects unknown arguments" "unknown argument: --bogus" \
  bash "$SCRIPT" --bogus x
expect_fail "requires --panel" "--panel is required" \
  bash "$SCRIPT" --token t
expect_fail "requires --token" "--token is required" \
  bash "$SCRIPT" --panel https://p

# The root guard can only be exercised by a non-root user. Running the suite
# as root would silently turn this case into a test of the next guard down, so
# skip it loudly instead.
if [ "$(id -u)" -eq 0 ]; then
  echo "SKIP: refuses to run as non-root — this suite is running as uid 0"
else
  expect_fail "refuses to run as non-root" "must run as root" \
    bash "$SCRIPT" --panel https://p --token t
fi

if grep -q 'set -euo pipefail' "$SCRIPT"; then
  echo "ok: strict mode enabled"
else
  fail "install.sh must use 'set -euo pipefail'"
fi

if grep -q 'sha256sum -c' "$SCRIPT"; then
  echo "ok: verifies the binary checksum"
else
  fail "install.sh must verify the downloaded binary"
fi

# The embedded copy the panel serves must be byte-identical to this one.
# Go's TestEmbeddedScriptMatchesSource asserts the same thing, but running it
# here means editing scripts/install.sh and forgetting to sync fails the
# script suite too, wherever it is run from.
EMBEDDED="$(dirname "$0")/../internal/panel/httpapi/install.sh"
if [ ! -f "$EMBEDDED" ]; then
  fail "the embedded copy $EMBEDDED is missing"
elif cmp -s "$SCRIPT" "$EMBEDDED"; then
  echo "ok: embedded copy matches scripts/install.sh"
else
  fail "internal/panel/httpapi/install.sh has drifted from scripts/install.sh; run 'make sync-install'"
fi

# install.sh writes the node unit from an inline heredoc, so packaging/ holds a
# second copy for operators who install by hand. Same drift risk as above for
# everything that must not differ -- but ExecStart and ReadWritePaths ARE
# allowed to: "Option 2: Manual Installation" in README.md installs the
# binary to /usr/local/bin, not install.sh's own /opt/antimage, and the two
# genuinely different paths are the whole reason packaging/ is a second file
# rather than a symlink. What must not happen is packaging/antimage-node.service
# shipping the ${VAR} placeholders themselves -- systemd does not expand
# ${...} in a unit file, so a copy-pasted ${BINARY_PATH} is not a stale path,
# it is a unit that cannot start at all.
UNIT="$(dirname "$0")/../packaging/antimage-node.service"
extracted="$(mktemp)"
trap 'rm -f "$extracted"' EXIT
awk '/^cat > \/etc\/systemd\/system\/antimage-node\.service <</ {flag=1; next}
     flag && /^EOF$/ {flag=0}
     flag' "$SCRIPT" > "$extracted"

if [ ! -s "$extracted" ]; then
  fail "could not find the antimage-node.service heredoc in install.sh"
elif [ ! -f "$UNIT" ]; then
  fail "the packaging copy $UNIT is missing"
else
  if grep -v '^#' "$UNIT" | grep -q '\${'; then
    fail "packaging/antimage-node.service still has an unexpanded \${VAR} placeholder -- systemd cannot start this unit"
  fi
  # Structural lines -- everything but ExecStart/ReadWritePaths and comments,
  # which are the two lines install.sh's own INSTALL_DIR-based paths would
  # never match, and comments, which the heredoc carries none of -- must
  # still agree line for line: the hardening flags, Restart policy, and
  # everything else here is not install-method-specific.
  structural() {
    grep -v -E '^(#|ExecStart=|ReadWritePaths=)' "$1"
  }
  if diff -q <(structural "$extracted") <(structural "$UNIT") >/dev/null; then
    echo "ok: packaging/antimage-node.service matches the unit install.sh writes"
  else
    fail "packaging/antimage-node.service has drifted from the unit install.sh writes"
  fi
fi

if [ "$fails" -eq 0 ]; then
  echo "all install.sh checks passed"
fi
exit "$fails"
