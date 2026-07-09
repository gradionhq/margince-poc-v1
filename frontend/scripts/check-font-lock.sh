#!/usr/bin/env bash
# Font-lock gate (ported from the foundation skeleton): the three-family type
# rule (design §2) — every font-family declaration under frontend/src names
# only Outfit (display), DM Sans (body), or JetBrains Mono (mono).
#
# Allowed besides the three families: the generic stack fallbacks the §2
# token definitions name (system-ui, sans-serif, ui-monospace, monospace) and
# var(--f-*) references, which resolve inside tokens.css.
#
# Fail-closed grep arm on top of the vitest conformance suite — same
# discipline even if the test tree regresses.
#
# Usage: frontend/scripts/check-font-lock.sh   (wired into `make frontend-check`)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_DIR="$(cd "$SCRIPT_DIR/.." && pwd)/src"

FILES=()
while IFS= read -r -d '' f; do FILES+=("$f"); done < <(
  find "$SRC_DIR" -type f \( -name "*.ts" -o -name "*.tsx" -o -name "*.css" \) \
    -not -name "*.test.*" \
    -not -name "schema.d.ts" \
    -print0 2>/dev/null
)

if [[ "${#FILES[@]}" -eq 0 ]]; then
  echo "FAIL: font-lock found no files under $SRC_DIR — the gate is miswired" >&2
  exit 1
fi

echo "==> Font-lock check (${#FILES[@]} files under frontend/src)"

EXIT=0

# For each font-family declaration, strip everything allowed; any residue is
# a family outside the three-family rule.
while IFS= read -r hit; do
  value=$(echo "$hit" | grep -oE "font-family\s*:[^;]+" | head -1)
  [[ -z "$value" ]] && continue
  stripped=$(echo "$value" \
    | sed -E 's/font-family\s*://g' \
    | sed -E 's/var\(--[A-Za-z0-9-]+\)//g' \
    | sed -E 's/JetBrains Mono//g' \
    | sed -E 's/DM Sans//g' \
    | sed -E 's/Outfit//g' \
    | sed -E 's/system-ui//g' \
    | sed -E 's/sans-serif//g' \
    | sed -E 's/ui-monospace//g' \
    | sed -E 's/monospace//g' \
    | tr -d '",'"'"',; \t')
  if [[ -n "$stripped" ]]; then
    echo "FAIL (family outside the three-family rule): $hit"
    EXIT=1
  fi
done < <(
  printf '%s\0' "${FILES[@]}" \
    | xargs -0 grep -nHE "font-family\s*:" 2>/dev/null \
  || true
)

if [[ "$EXIT" == "0" ]]; then
  echo "PASS — only Outfit / DM Sans / JetBrains Mono (+ generic fallbacks)"
else
  echo ""
  echo "Allowed: Outfit, DM Sans, JetBrains Mono; generics system-ui,"
  echo "sans-serif, ui-monospace, monospace; var(--f-*) token references."
fi

exit $EXIT
