#!/usr/bin/env bash
# Design-system spacing gate: NEW code should not hand-set vertical rhythm with
# raw pixel literals in inline React styles. Use the --space-* scale
# (src/design-system/tokens.css) or a layout class (.filter-tabs, .form-stack,
# .card, …) so the same gap reads the same everywhere — the drift this catches
# is the recurring "spacing not good" report (10 vs 12 vs 14 vs 16 for the same
# separator), and the boundary rules in atoms.css that own header/tab/card
# seams.
#
# DIFF-SCOPED, by design: it inspects only the lines THIS branch adds to
# frontend/src/**/*.tsx versus the merge-base with origin/main. The large
# pre-existing backlog of inline px is NOT gated — write it right the first
# time, exactly like the craft pre-push hook. A genuine one-off is waived
# in-line with a reason: add `// ds:ignore <reason>` on the offending line.
#
# Flags, on ADDED *.tsx lines only, inline-style props set to a bare non-zero
# number:  margin* / padding* / gap / rowGap / columnGap : <n>
# Never flags string values ("0", "var(--space-3)") or a bare 0 reset.
#
# Usage: frontend/scripts/check-ds-spacing.sh   (wired into `make frontend-check`)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR" && git rev-parse --show-toplevel 2>/dev/null || true)"

if [[ -z "$REPO_ROOT" ]]; then
  echo "==> DS spacing check: not a git checkout — skipped"
  exit 0
fi

# The comparison point: the merge-base with origin/main (what this branch adds).
# Fall back to origin/main directly, then to a no-op if neither resolves (e.g.
# a shallow CI clone without the remote ref) — fail-open so the gate never
# blocks on missing history, only on real new violations it can see.
BASE=""
if git -C "$REPO_ROOT" rev-parse --verify --quiet origin/main >/dev/null; then
  BASE="$(git -C "$REPO_ROOT" merge-base origin/main HEAD 2>/dev/null || echo origin/main)"
fi
if [[ -z "$BASE" ]]; then
  echo "==> DS spacing check: no origin/main baseline — skipped"
  exit 0
fi

# Read-loop rather than mapfile — the CI/dev host ships bash 3.2 (no mapfile),
# same portability constraint as check-ds-purity.sh.
CHANGED=()
while IFS= read -r f; do
  [[ -n "$f" ]] && CHANGED+=("$f")
done < <(
  git -C "$REPO_ROOT" diff --name-only --diff-filter=d "$BASE" -- 'frontend/src/**/*.tsx' 'frontend/src/*.tsx' 2>/dev/null || true
)

if [[ "${#CHANGED[@]}" -eq 0 ]]; then
  echo "==> DS spacing check: no changed frontend *.tsx — nothing to gate"
  exit 0
fi

echo "==> DS spacing check (${#CHANGED[@]} changed *.tsx vs ${BASE:0:12})"

# margin*/padding*/gap/rowGap/columnGap : <non-zero number>. camelCase only —
# that is how inline React styles spell it; CSS files are out of scope.
PATTERN='\b(margin|padding)([A-Z][A-Za-z]*)?[[:space:]]*:[[:space:]]*[1-9]|\b(gap|rowGap|columnGap)[[:space:]]*:[[:space:]]*[1-9]'

EXIT=0
for f in "${CHANGED[@]}"; do
  # Only the ADDED lines (leading '+', not the '+++' file header), with their
  # new line numbers, so the message points at the author's own change.
  hits=$(
    git -C "$REPO_ROOT" diff --unified=0 "$BASE" -- "$f" \
      | awk '
          /^@@/ {
            match($0, /\+[0-9]+/); ln = substr($0, RSTART + 1, RLENGTH - 1) + 0; next
          }
          /^\+\+\+/ { next }
          /^\+/ {
            line = substr($0, 2)
            if (line !~ /ds:ignore/ && line ~ /(margin|padding)([A-Z][A-Za-z]*)?[[:space:]]*:[[:space:]]*[1-9]|(gap|rowGap|columnGap)[[:space:]]*:[[:space:]]*[1-9]/)
              printf "  %s:%d: %s\n", FILENAME, ln, line
            ln++
            next
          }
          { ln++ }
        ' FILENAME="$f"
  )
  if [[ -n "$hits" ]]; then
    if [[ "$EXIT" -eq 0 ]]; then
      echo ""
      echo "FAIL: raw-px spacing in inline styles (new code)"
    fi
    echo "$hits"
    EXIT=1
  fi
done

if [[ "$EXIT" == "0" ]]; then
  echo "PASS — no new inline-px spacing"
else
  echo ""
  echo "Use the --space-* scale (tokens.css) or a layout class instead of a raw"
  echo "px margin/padding/gap in inline styles — e.g. className=\"filter-tabs\","
  echo "the .card/.form-stack rhythm, or style={{ marginTop: 'var(--space-3)' }}."
  echo "A genuine one-off is waived in-line: // ds:ignore <reason>"
fi

exit $EXIT
