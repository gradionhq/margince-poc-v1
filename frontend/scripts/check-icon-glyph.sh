#!/usr/bin/env bash
# Icon-glyph gate: UI glyphs are
# lucide-react only — no emoji/pictographic Unicode in rendered frontend
# code. The sanctioned 🟢/🟡 autonomy semantics render through the CSS .dot
# component (design-system/trust.tsx AutonomyDot), never as text glyphs.
#
# Scope: code, not commentary. Comment content is stripped before scanning —
# source comments quote the spec's 🟢/🟡 tier notation (house style across the
# repo) and are not rendered UI. String literals hiding an emoji behind a
# "//" (rare) are still caught by the AST-accurate vitest conformance gate;
# this is the fail-closed grep arm on top of it.
#
# Excluded: test files (fixtures) and the generated schema.d.ts (its doc
# comments quote crm.yaml prose verbatim).
#
# Uses perl -CSD for Unicode ranges (BSD grep lacks -P).
#
# Usage: frontend/scripts/check-icon-glyph.sh   (wired into `make frontend-check`)

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
  echo "FAIL: icon-glyph found no files under $SRC_DIR — the gate is miswired" >&2
  exit 1
fi

echo "==> Icon-glyph check (${#FILES[@]} files under frontend/src)"

EXIT=0

# Ranges: Misc Symbols & Pictographs / Emoticons / Transport / Supplemental
# (U+1F300–U+1FAFF), regional indicators (U+1F1E6–U+1F1FF), Misc Symbols &
# Dingbats (U+2600–U+27BF), Misc Symbols and Arrows (U+2B00–U+2BFF), and
# VS-16 (U+FE0F). U+2022 "•" (the FieldGuard mask) is outside all of them.
while IFS= read -r hit; do
  [[ -z "$hit" ]] && continue
  echo "FAIL (emoji glyph in rendered code — use a lucide-react icon): $hit"
  EXIT=1
done < <(
  printf '%s\0' "${FILES[@]}" \
    | xargs -0 perl -CSD -ne '
      my $line = $_;
      $line =~ s{^\s*(?:/\*|\*|//).*$}{};   # whole-line comments
      $line =~ s{\{?/\*.*?\*/\}?}{}g;       # inline (JSX) block comments
      $line =~ s{//.*$}{};                  # trailing line comments
      if ($line =~ /[\x{1F300}-\x{1FAFF}\x{1F1E6}-\x{1F1FF}\x{2600}-\x{27BF}\x{2B00}-\x{2BFF}\x{FE0F}]/) {
        chomp; print $ARGV . ":" . $. . ": " . $_ . "\n";
      }
      close ARGV if eof
    ' 2>/dev/null \
  || true
)

if [[ "$EXIT" == "0" ]]; then
  echo "PASS — no emoji glyphs in rendered code (lucide-react only)"
else
  echo ""
  echo "Replace the glyph with a lucide-react icon, or — for autonomy tiers —"
  echo "the AutonomyDot component (design-system/trust.tsx)."
fi

exit $EXIT
