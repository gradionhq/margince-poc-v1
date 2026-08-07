#!/usr/bin/env bash
# Native-control gate: no product surface may render a browser-drawn dropdown.
#
# A `<select>` is the one control the platform paints for itself. Its closed face
# takes our tokens; the option list behind it — fill, type, highlight, scrollbar,
# and on a phone a whole system sheet — does not, and cannot: `option` is not
# stylable in any engine we ship to. So on a screen built entirely from
# src/design-system/ a native dropdown reads as a hole in the product, and the
# defect is invisible in review because the closed control looks correct. The
# replacement is `Select` in src/design-system/select.tsx — a button plus a
# portalled listbox — which is also the only file this gate exempts.
#
# Fails on `<select`, `<option` and `<optgroup` anywhere under frontend/src except
# design-system/select.tsx, in any *.ts / *.tsx — tests and stories included,
# because a test that drives a native control is a test of the wrong control, and
# a story catalogues what we ship.
#
# All three, not just the element that names the gate: `<option>` is what the
# native dropdown is BUILT from, it is meaningless anywhere else, and it is what a
# half-finished migration leaves behind — a screen still handing option children
# to a control that no longer takes them. Catching only `<select` would call that
# tree clean.
#
# Comments are stripped before matching: a `<select>` inside a comment is a
# cross-reference (this replaced that), which is exactly how select.tsx and its
# neighbours cite the thing they exist to remove. A `//` that follows a colon is
# NOT a comment — it is the scheme separator in a URL, and treating it as one
# would blank the rest of a line that may still hold real markup.
#
# Companion to the vitest suites rather than a substitute: this is the
# fail-closed grep arm, so the discipline holds even if the test tree regresses.
#
# Usage: frontend/scripts/check-native-controls.sh   (wired into `make frontend-check`)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_DIR="$(cd "$SCRIPT_DIR/.." && pwd)/src"

# The ONE exemption, and it is a full PATH rather than a name or a glob: widening
# it to a directory is how a second hand-rolled dropdown gets in beside the real
# one, and matching on the basename alone would exempt any `select.tsx` anywhere
# in the tree — including a screen that wrote its own.
FILES=()
while IFS= read -r -d '' f; do FILES+=("$f"); done < <(
  find "$SRC_DIR" -type f \( -name "*.ts" -o -name "*.tsx" \) \
    -not -path "$SRC_DIR/design-system/select.tsx" \
    -print0 2>/dev/null
)

# An empty scan means the gate is pointed at the wrong tree — fail closed.
if [[ "${#FILES[@]}" -eq 0 ]]; then
  echo "FAIL: native-control check found no files under $SRC_DIR — the gate is miswired" >&2
  exit 1
fi

echo "==> Native-control check (${#FILES[@]} files under frontend/src)"

# Blank out every comment while KEEPING the line count, so a hit still reports the
# author's own line number. Handles `//` to end of line, `/* … */` on one line, and
# a block comment spanning lines (which is also the `{/* … */}` JSX form).
strip_comments() {
  awk '
    BEGIN { inblock = 0 }
    {
      line = $0
      out = ""
      while (length(line) > 0) {
        if (inblock) {
          close_at = index(line, "*/")
          if (close_at == 0) { line = ""; break }
          line = substr(line, close_at + 2)
          inblock = 0
          continue
        }
        block_at = index(line, "/*")
        eol_at = index(line, "//")
        # A scheme separator, not a comment: skip past `://` and keep looking.
        while (eol_at > 1 && substr(line, eol_at - 1, 1) == ":") {
          out = out substr(line, 1, eol_at + 1)
          line = substr(line, eol_at + 2)
          block_at = index(line, "/*")
          eol_at = index(line, "//")
        }
        if (eol_at > 0 && (block_at == 0 || eol_at < block_at)) {
          out = out substr(line, 1, eol_at - 1)
          line = ""
          break
        }
        if (block_at == 0) { out = out line; line = ""; break }
        out = out substr(line, 1, block_at - 1)
        line = substr(line, block_at + 2)
        inblock = 1
      }
      print out
    }
  ' "$1"
}

# Each element followed by anything that is not another identifier character:
# catches `<select>`, `<select ` and a `<select` whose attributes wrap onto the
# next line. The design-system component is `<Select`, capitalised, so it is not a
# hit — and neither is `<Option`, should anything ever be called that.
PATTERN='<(select|option|optgroup)([^a-zA-Z0-9]|$)'

EXIT=0
for f in "${FILES[@]}"; do
  hits=$(strip_comments "$f" | grep -nE "$PATTERN" || true)
  if [[ -n "$hits" ]]; then
    if [[ "$EXIT" == "0" ]]; then
      echo ""
      echo "FAIL: native dropdown markup outside design-system/select.tsx"
      EXIT=1
    fi
    while IFS= read -r hit; do
      echo "  ${f#"$SRC_DIR"/}:${hit}"
    done <<< "$hits"
  fi
done

if [[ "$EXIT" == "0" ]]; then
  echo "PASS — no native dropdown under frontend/src"
  exit 0
fi

echo ""
echo "Use Select from src/design-system/select.tsx; see src/design-system/README.md."
echo "In tests, drive it with pickOption from src/design-system/select-testing.ts."
exit $EXIT
