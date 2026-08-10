#!/usr/bin/env bash
# Every --space-* token a stylesheet USES must be DEFINED in tokens.css.
#
# An undefined custom property does not fall back to a smaller value — it
# resolves to nothing, and the declaration is dropped. `--space-5` was missing
# from the scale while six rules across the tree spelled it, so a drawer that
# declared `padding: var(--space-5)` rendered with NO padding and clipped its
# own heading against the viewport edge. Nothing failed: not the typecheck, not
# the unit tests (jsdom does not resolve custom properties), not the spacing
# gate, which only reads raw px.
#
# This is the fitness-function form of that bug: derive the obligation from the
# tree rather than maintain a list.
set -euo pipefail

cd "$(dirname "$0")/.."

TOKENS="src/design-system/tokens.css"
if [[ ! -f "$TOKENS" ]]; then
  echo "FAIL: $TOKENS not found"
  exit 1
fi

defined=$(grep -oE -- '--space-[a-z0-9-]+:' "$TOKENS" | sed 's/:$//' | sort -u)
sources=$(find src -type f \( -name '*.css' -o -name '*.tsx' -o -name '*.ts' \))
used=$(echo "$sources" | tr '\n' '\0' | xargs -0 grep -hoE -- 'var\(--space-[a-z0-9-]+' 2>/dev/null \
  | sed 's/^var(//' | sort -u)

missing=$(comm -13 <(echo "$defined") <(echo "$used"))

if [[ -n "$missing" ]]; then
  echo "FAIL: stylesheets use --space tokens that tokens.css does not define"
  echo ""
  while read -r token; do
    [[ -z "$token" ]] && continue
    echo "  $token — used in:"
    echo "$sources" | tr '\n' '\0' | xargs -0 grep -ln -- "var($token)" 2>/dev/null \
      | sed 's/^/      /'
  done <<< "$missing"
  echo ""
  echo "An undefined custom property resolves to NOTHING: the declaration is"
  echo "dropped and the element renders with no value at all. Define it in"
  echo "$TOKENS, or use a token that exists."
  exit 1
fi

echo "OK: every --space token used is defined"
