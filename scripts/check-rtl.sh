#!/usr/bin/env bash
# Mechanically enforces the RTL rules from spec section 8. Retrofitting RTL
# fails because nobody remembers the rules; a failing build remembers them.
set -uo pipefail

SRC="web/src"
fails=0

# Physical direction utilities are banned; logical ones must be used instead.
if matches=$(grep -rnE '\b(ml-|mr-|pl-|pr-|left-|right-|text-left|text-right)[0-9a-z]' \
     --include='*.tsx' --include='*.ts' --include='*.css' "$SRC" 2>/dev/null); then
  if [ -n "$matches" ]; then
    echo "FAIL: physical direction utilities found. Use ms-/me-/ps-/pe-/start-/end-/text-start/text-end."
    echo "$matches"
    fails=$((fails + 1))
  fi
fi

# Literal user-facing strings in JSX must go through t().
#
# The '>' that opens the text node must be the close of a tag, so it has to
# follow an identifier, a quote, a brace or a slash. Without that anchor the
# pattern also fires on ordinary comparisons that happen to return an element
# --- `if (n > Number.MAX_SAFE_INTEGER) return <Fallback />;` matched, and a
# gate that fails on correct code is a gate someone deletes. The text itself
# may not contain '<', '{', '}' or a quote, which keeps attribute values and
# interpolated expressions out, and the match must end at a real tag ('</' or
# '<' + letter). This is deliberately conservative: it would rather miss an
# odd literal than reject correct code.
if matches=$(grep -rnE '[A-Za-z0-9">}/]>[[:space:]]*[A-Z][a-z]{2,}[^<{}"]*<(/|[A-Za-z])' \
     --include='*.tsx' "$SRC" 2>/dev/null | grep -v 't('); then
  if [ -n "$matches" ]; then
    echo "FAIL: literal strings in JSX. Wrap them in t() so they can be translated."
    echo "$matches"
    fails=$((fails + 1))
  fi
fi

if [ "$fails" -eq 0 ]; then
  echo "OK: RTL and i18n gates clean."
fi
exit "$fails"
