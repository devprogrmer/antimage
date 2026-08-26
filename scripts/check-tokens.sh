#!/usr/bin/env bash
#
# Every colour in the UI must resolve through a design token.
#
# The tokens are defined once in web/src/index.css and have a value per theme,
# so `bg-card` is correct in both and `bg-zinc-900` is correct in neither --
# it is a dark surface hardcoded into a component that also has to render on a
# light page. Before Phase A3 the Dashboard was written against a white page
# (bg-white cards, gray-900 headings) inside an otherwise dark application, and
# nothing caught it because nothing was looking.
#
# This is the same shape of gate as check-rtl.sh: cheap, mechanical, and it
# fails on the way in rather than being discovered by whoever switches theme.
set -uo pipefail

SRC="web/src"
fails=0

# Tailwind's built-in palettes. A utility ending in a numeric shade is a raw
# colour; a token utility (bg-card, text-muted-foreground) never is.
PALETTES='slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose'

if matches=$(grep -rnE "(^|[\"'\` ])([a-z-]+:)*(bg|text|border|ring|from|to|via|fill|stroke|decoration|outline|shadow|accent|caret|divide|placeholder)-($PALETTES)-[0-9]{2,3}" \
     --include='*.tsx' --include='*.ts' --include='*.css' "$SRC" 2>/dev/null); then
  if [ -n "$matches" ]; then
    echo "FAIL: raw palette colours found. Use a design token (bg-card, text-muted-foreground, border-border, text-destructive, text-success, text-warning)."
    echo "$matches"
    fails=$((fails + 1))
  fi
fi

# bg-white and text-black are the same mistake without a shade number.
if matches=$(grep -rnE "(^|[\"'\` ])([a-z-]+:)*(bg|text|border)-(white|black)([\"'\` ]|$)" \
     --include='*.tsx' --include='*.ts' "$SRC" 2>/dev/null); then
  if [ -n "$matches" ]; then
    echo "FAIL: bg-white / text-black found. These are one theme's answer to a question both themes ask."
    echo "$matches"
    fails=$((fails + 1))
  fi
fi

if [ "$fails" -eq 0 ]; then
  echo "OK: design tokens clean."
fi
exit "$fails"
