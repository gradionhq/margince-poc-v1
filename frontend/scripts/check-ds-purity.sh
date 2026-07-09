#!/usr/bin/env bash
# Design-token purity gate (ported from the foundation skeleton, adapted to
# the Ledger-Green token system): every colour in hand-written frontend code
# reads a token — literal colours live ONLY in src/design-system/tokens.css,
# where tokens.test.ts pins each value to the design source of truth.
#
# Fails on, anywhere else under frontend/src (*.ts / *.tsx / *.css):
#   1. Hex literals (#abc / #aabbcc / #aabbccdd) — use var(--token)
#   2. Raw colour functions rgb()/rgba()/hsl()/hsla()/oklch() — use a token
#
# This is the fail-closed grep arm on top of the vitest conformance suite
# (design-system/conformance.test.ts): the same discipline holds even if the
# test tree regresses. The skeleton's Tailwind-utility checks (text-[Npx],
# gf-prefix classes) have no equivalent here — our DS is CSS-custom-property
# based, not Tailwind-class based.
#
# Usage: frontend/scripts/check-ds-purity.sh   (wired into `make frontend-check`)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_DIR="$(cd "$SCRIPT_DIR/.." && pwd)/src"

# Excluded: the token source file (literals are its job), generated contract
# types, and test files (fixtures aren't shipped UI).
FILES=()
while IFS= read -r -d '' f; do FILES+=("$f"); done < <(
  find "$SRC_DIR" -type f \( -name "*.ts" -o -name "*.tsx" -o -name "*.css" \) \
    -not -name "*.test.*" \
    -not -name "tokens.css" \
    -not -name "schema.d.ts" \
    -print0 2>/dev/null
)

# An empty scan means the gate is pointed at the wrong tree — fail closed.
if [[ "${#FILES[@]}" -eq 0 ]]; then
  echo "FAIL: DS purity found no files under $SRC_DIR — the gate is miswired" >&2
  exit 1
fi

echo "==> DS purity check (${#FILES[@]} files under frontend/src)"

EXIT=0

check() {
  local label="$1"
  local pattern="$2"
  local hits
  hits=$(printf '%s\0' "${FILES[@]}" | xargs -0 grep -nHE "$pattern" || true)
  if [[ -n "$hits" ]]; then
    echo ""
    echo "FAIL: $label"
    echo "$hits"
    EXIT=1
  fi
}

check "hex colour literal (read it from a token — see design-system/tokens.css)" \
  '#([0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3})\b'

check "raw colour function (use var(--token) or color-mix over a token)" \
  '\b(rgba?|hsla?|oklch)\('

if [[ "$EXIT" == "0" ]]; then
  echo "PASS — every colour outside tokens.css reads a token"
else
  echo ""
  echo "Literal colours live only in src/design-system/tokens.css (ADR-0040)."
fi

exit $EXIT
