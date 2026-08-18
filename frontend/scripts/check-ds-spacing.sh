#!/usr/bin/env bash
# Design-system spacing gate: NEW code should not hand-set vertical rhythm with
# raw pixel literals. Use the --space-* scale (src/design-system/tokens.css) or
# a layout class (.filter-tabs, .form-stack, .card, …) so the same gap reads the
# same everywhere — the drift this catches is the recurring "spacing not good"
# report (10 vs 12 vs 14 vs 16 for the same separator), and the boundary rules
# in atoms.css that own header/tab/card seams.
#
# Two arms, one bar:
#   *.tsx — inline React style props set to a bare non-zero number:
#           margin* / padding* / gap / rowGap / columnGap : <n>
#           String values ("0", "var(--space-3)") and a bare 0 reset are fine.
#   *.css — declarations of padding, padding-*, margin, margin-*, gap, row-gap
#           and column-gap whose value carries a raw non-zero px. 0 is fine, a
#           token is fine (including mixed values like `var(--space-2) 0`), and
#           a calc() built on a token is fine — the offset inside it is
#           arithmetic on the scale, not a rhythm value of its own — but only
#           that expression is exempt, not the rest of the value. A declaration
#           is whatever sits between two separators, so a property and its value
#           on different lines are one declaration. Every other
#           property that legitimately measures in px (border, border-radius,
#           min-height, top/left, font-size, box-shadow, …) is untouched: this
#           gate is about rhythm, not about px.
#
# src/design-system/ is EXEMPT from the *.css arm because that tier DEFINES the
# scale rather than consuming it: an atom's optical values (.input and .textarea
# carry `padding: 9px 11px` so the text sits on the same baseline as the label
# beside it) are deliberately off the 4/8/12/16/24 steps, and rounding them onto
# the scale would be the regression. Screens consume the scale, so src/screens/
# and src/app/ are what this gates.
#
# DIFF-SCOPED, by design: it inspects only the lines THIS branch adds versus the
# merge-base with origin/main. The large pre-existing backlog of raw px is NOT
# gated — write it right the first time, exactly like the craft pre-push hook.
# A genuine one-off is waived in-line with a reason, in the file's own comment
# syntax: `// ds:ignore <reason>` in .tsx, `/* ds:ignore <reason> */` in .css.
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

# A brand-new file is the strictest case there is — all of it is new code — yet
# `git diff` cannot see one until it is tracked, so an untracked file would slip
# the gate entirely. Listing it here and diffing it against /dev/null below
# renders it as a full-file addition, which the same awk pass then reads without
# a special case.
untracked() {
  git -C "$REPO_ROOT" ls-files --others --exclude-standard -- "$@" 2>/dev/null || true
}

# The unit trees join the pathspecs: a unit's screen is shipped UI in the same
# bundle, and a spacing gate that stopped at frontend/src would hold the core to
# a scale the extension tier escapes.
#
# ONE pattern per tree, and it is the plain one. A git pathspec is not :(glob)
# magic, so its `*` spans directory separators: 'frontend/src/*.tsx' already
# matches every depth under frontend/src. It is `**/` that carries the
# requirement — an intermediate directory — so '<tree>/**/*.tsx' silently misses
# a file sitting DIRECTLY in <tree>. That is what the extension entry was, on
# its own, while every unit screen sits at extensions/<unit>/frontend/screen.tsx.
# Do not add a `**/` sibling back: it can only ever match a subset.
#
# Spelled ONCE and shared by the tracked and untracked collectors below — two
# copies is how these two trees came to disagree in the first place.
# check-ds-spacing.test.sh holds the census these have to keep collecting.
TSX_PATHSPEC=('frontend/src/*.tsx' 'extensions/*/frontend/*.tsx')
CSS_PATHSPEC=('frontend/src/*.css' 'extensions/*/frontend/*.css')

# Read-loop rather than mapfile — the CI/dev host ships bash 3.2 (no mapfile),
# same portability constraint as check-ds-purity.sh.
CHANGED_TSX=()
while IFS= read -r f; do
  [[ -n "$f" ]] && CHANGED_TSX+=("$f")
done < <(
  git -C "$REPO_ROOT" diff --name-only --diff-filter=d "$BASE" -- "${TSX_PATHSPEC[@]}" 2>/dev/null || true
  untracked "${TSX_PATHSPEC[@]}"
)

CHANGED_CSS=()
while IFS= read -r f; do
  [[ -z "$f" ]] && continue
  [[ "$f" == frontend/src/design-system/* ]] && continue
  CHANGED_CSS+=("$f")
done < <(
  git -C "$REPO_ROOT" diff --name-only --diff-filter=d "$BASE" -- "${CSS_PATHSPEC[@]}" 2>/dev/null || true
  untracked "${CSS_PATHSPEC[@]}"
)

# The added-lines diff for one file, tracked or not. `--no-index` exits non-zero
# when the two sides differ, which is the normal case here, so the status is
# deliberately discarded.
added_diff() {
  local f="$1"
  if git -C "$REPO_ROOT" ls-files --error-unmatch "$f" >/dev/null 2>&1; then
    git -C "$REPO_ROOT" diff --unified=0 "$BASE" -- "$f" 2>/dev/null || true
  else
    git -C "$REPO_ROOT" diff --no-index --unified=0 -- /dev/null "$f" 2>/dev/null || true
  fi
}

if [[ "${#CHANGED_TSX[@]}" -eq 0 && "${#CHANGED_CSS[@]}" -eq 0 ]]; then
  echo "==> DS spacing check: no changed frontend *.tsx or *.css — nothing to gate"
  exit 0
fi

echo "==> DS spacing check (${#CHANGED_TSX[@]} changed *.tsx, ${#CHANGED_CSS[@]} changed *.css vs ${BASE:0:12})"

EXIT=0
TSX_HEADER_DONE=0
CSS_HEADER_DONE=0

# Both arms walk `git diff --unified=0` and track the NEW-file line number, so
# the message points at the author's own change: a hunk header resets the
# counter, an added line consumes one, and a removed line consumes none because
# it does not exist in the new file.
for f in ${CHANGED_TSX[@]+"${CHANGED_TSX[@]}"}; do
  hits=$(
    added_diff "$f" \
      | awk '
          /^@@/ {
            match($0, /\+[0-9]+/); ln = substr($0, RSTART + 1, RLENGTH - 1) + 0; next
          }
          /^\+\+\+/ || /^-/ || /^\\/ { next }
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
    if [[ "$TSX_HEADER_DONE" -eq 0 ]]; then
      echo ""
      echo "FAIL: raw-px spacing in inline styles (new code)"
      TSX_HEADER_DONE=1
    fi
    echo "$hits"
    EXIT=1
  fi
done

for f in ${CHANGED_CSS[@]+"${CHANGED_CSS[@]}"}; do
  # The diff supplies WHICH lines are new; the file itself supplies their
  # content, so a /* */ block spanning several lines is tracked honestly
  # instead of guessed at from one diff line in isolation. Skipped when the
  # diff carries no hunks (a mode-only change), which also keeps awk's
  # first-file test from mistaking the stylesheet for the diff.
  diff_out=$(added_diff "$f")
  [[ -n "$diff_out" ]] || continue

  hits=$(
    printf '%s\n' "$diff_out" \
      | awk '
          # Strips commented-out regions, carrying the open-block state across
          # lines. Returns the code part of the line.
          function decomment(line,   out, p) {
            out = ""
            while (length(line) > 0) {
              if (incomment) {
                p = index(line, "*/")
                if (p == 0) return out
                line = substr(line, p + 2)
                incomment = 0
              } else {
                p = index(line, "/*")
                if (p == 0) return out line
                out = out substr(line, 1, p - 1)
                line = substr(line, p + 2)
                incomment = 1
              }
            }
            return out
          }

          # Drops every calc() built on a token: the offset inside one is
          # arithmetic on the scale, not a rhythm value of its own. ONLY the
          # expression is exempt — whatever else the value carries stays gated.
          # Parentheses are matched by depth so a nested var() does not close
          # the calc early.
          function strip_token_calc(value,   out, p, i, depth, start, inner, ch) {
            out = ""
            while ((p = index(value, "calc(")) > 0) {
              out = out substr(value, 1, p - 1)
              start = p + 5
              depth = 1
              for (i = start; i <= length(value); i++) {
                ch = substr(value, i, 1)
                if (ch == "(") depth++
                else if (ch == ")") { depth--; if (depth == 0) break }
              }
              inner = substr(value, start, i - start)
              if (index(inner, "var(") == 0) out = out "calc(" inner ")"
              value = substr(value, i + 1)
            }
            return out value
          }

          # True when the value carries a px length other than zero.
          function raw_px(value,   rest, n) {
            rest = strip_token_calc(value)
            while (match(rest, /([0-9]+(\.[0-9]+)?|\.[0-9]+)px/)) {
              n = substr(rest, RSTART, RLENGTH - 2) + 0
              if (n != 0) return 1
              rest = substr(rest, RSTART + RLENGTH)
            }
            return 0
          }

          # The seven spacing properties, and only those. A leading letter or
          # dash rules out both a custom property (--gap) and a different
          # property that merely ends the same way (grid-gap, scroll-padding).
          function spacing_prop(name) {
            return name ~ /^(padding|margin)(-[a-z]+)*$/ || name ~ /^(gap|row-gap|column-gap)$/
          }

          # Judges ONE complete declaration, however many lines it spanned.
          function judge(text, lineno, is_added, is_waived,   prop, value, shown) {
            if (!is_added || is_waived || index(text, ":") == 0) return
            prop = tolower(substr(text, 1, index(text, ":") - 1))
            value = tolower(substr(text, index(text, ":") + 1))
            sub(/^[ \t]+/, "", prop); sub(/[ \t]+$/, "", prop)
            if (!spacing_prop(prop) || !raw_px(value)) return
            shown = text
            gsub(/[ \t]+/, " ", shown); sub(/^ /, "", shown); sub(/ +$/, "", shown)
            printf "  %s:%d: %s\n", target, lineno, shown
          }

          # Feeds one stylesheet line to the declaration scanner, carrying an
          # unterminated declaration across lines the same way decomment()
          # carries an open comment: a value separated from its property by a
          # newline still belongs to that property. `;`, `{` and `}` all close
          # the open declaration; the verdict lands on the line it opened on,
          # and a `ds:ignore` on ANY of its lines waives it.
          function feed(lineno, raw,   code, p, seg, line_added, line_waived) {
            code = decomment(raw)
            gsub(/[{}]/, ";", code)
            line_added = (lineno in added)
            line_waived = (raw ~ /ds:ignore/)
            while ((p = index(code, ";")) > 0) {
              seg = substr(code, 1, p - 1)
              if (pending == "") pending_line = lineno
              judge(pending seg, pending_line,
                    pending_added || line_added, pending_waived || line_waived)
              code = substr(code, p + 1)
              pending = ""; pending_added = 0; pending_waived = 0
            }
            if (code ~ /[^ \t]/) {
              if (pending == "") pending_line = lineno
              pending = pending " " code
              if (line_added) pending_added = 1
              if (line_waived) pending_waived = 1
            }
          }

          # Pass 1: the diff — which NEW line numbers this branch adds.
          FNR == NR {
            if (/^@@/) {
              match($0, /\+[0-9]+/); ln = substr($0, RSTART + 1, RLENGTH - 1) + 0; next
            }
            if (/^\+\+\+/ || /^-/ || /^\\/) next
            if (/^\+/) { added[ln++] = 1; next }
            ln++
            next
          }

          # Pass 2: the stylesheet. Every line feeds the scanner so the comment
          # and declaration state stay correct; only the added ones can be
          # reported. A declaration left open at EOF is judged on what it has.
          { feed(FNR, $0) }

          END {
            if (pending != "")
              judge(pending, pending_line, pending_added, pending_waived)
          }
        ' target="$f" - "$REPO_ROOT/$f"
  )
  if [[ -n "$hits" ]]; then
    if [[ "$CSS_HEADER_DONE" -eq 0 ]]; then
      echo ""
      echo "FAIL: raw-px spacing in stylesheets (new code)"
      CSS_HEADER_DONE=1
    fi
    echo "$hits"
    EXIT=1
  fi
done

if [[ "$EXIT" == "0" ]]; then
  echo "PASS — no new raw-px spacing"
else
  echo ""
  echo "Use the --space-* scale (tokens.css) or a layout class instead of a raw"
  echo "px margin/padding/gap — e.g. className=\"filter-tabs\", the .card/.form-stack"
  echo "rhythm, style={{ marginTop: 'var(--space-3)' }}, or gap: var(--space-2)."
  echo "A genuine one-off is waived in-line, with a reason, on the offending line:"
  echo "  // ds:ignore <reason>      (.tsx)"
  echo "  /* ds:ignore <reason> */   (.css)"
fi

exit $EXIT
