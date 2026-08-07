#!/usr/bin/env bash
# Design-system type gate: NEW code should not hand-set the type scale with
# raw literals. Use the --text-*/--w-*/--track-* scale (src/design-system/
# tokens.css) so the same size/weight/tracking reads the same everywhere —
# the drift this catches is the same "spacing not good" failure mode, just
# for type: 13 vs 14 vs 15px for the same body copy, 500 vs 600 for the same
# emphasis. A waiver REQUIRES a reason after the `ds:ignore` token — a bare
# `// ds:ignore` or `/* ds:ignore */` does not waive anything, it just fails
# like any other unwaived hit.
#
# Two arms, one bar:
#   *.tsx — inline React style props set to a bare number or a raw string
#           literal: fontSize / fontWeight / letterSpacing. A `var(--…)`
#           string value is fine.
#   *.css — declarations of font-size, font-weight and letter-spacing whose
#           value carries a raw length/number instead of a token. A
#           clamp()/calc() built entirely on --text-* tokens is fine — it is
#           arithmetic on the scale, not a size value of its own — but only
#           that expression is exempt, not the rest of the value.
#           font-size: inherit / 0, font-weight: normal / bold / inherit and
#           letter-spacing: normal / 0 are all fine; they name no size of
#           their own.
#
# Unlike check-ds-spacing.sh, src/design-system/ is NOT exempt from the
# *.css arm: the type scale is meant to be consumed by atoms and composed
# components too, not just screens, so an atom reaching past the tokens is
# exactly the drift this gate exists to catch. tokens.css itself IS exempt —
# it defines the scale rather than consuming it.
#
# DIFF-SCOPED, by design: it inspects only the lines THIS branch adds versus
# the merge-base with origin/main. The large pre-existing backlog of raw
# type literals is NOT gated — write it right the first time, exactly like
# the craft pre-push hook. A genuine one-off is waived in-line with a
# reason, in the file's own comment syntax: `// ds:ignore <reason>` in .tsx,
# `/* ds:ignore <reason> */` in .css. The reason is not optional — a bare
# token with nothing (or only whitespace) after it does not waive.
#
# Usage: frontend/scripts/check-ds-type.sh   (wired into `make frontend-check`)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR" && git rev-parse --show-toplevel 2>/dev/null || true)"

if [[ -z "$REPO_ROOT" ]]; then
  echo "==> DS type check: not a git checkout — skipped"
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
  echo "==> DS type check: no origin/main baseline — skipped"
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

# Read-loop rather than mapfile — the CI/dev host ships bash 3.2 (no mapfile),
# same portability constraint as check-ds-purity.sh.
CHANGED_TSX=()
while IFS= read -r f; do
  [[ -n "$f" ]] && CHANGED_TSX+=("$f")
done < <(
  git -C "$REPO_ROOT" diff --name-only --diff-filter=d "$BASE" -- 'frontend/src/**/*.tsx' 'frontend/src/*.tsx' 2>/dev/null || true
  untracked 'frontend/src/**/*.tsx' 'frontend/src/*.tsx'
)

CHANGED_CSS=()
while IFS= read -r f; do
  [[ -z "$f" ]] && continue
  [[ "$f" == frontend/src/design-system/tokens.css ]] && continue
  CHANGED_CSS+=("$f")
done < <(
  git -C "$REPO_ROOT" diff --name-only --diff-filter=d "$BASE" -- 'frontend/src/**/*.css' 'frontend/src/*.css' 2>/dev/null || true
  untracked 'frontend/src/**/*.css' 'frontend/src/*.css'
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
  echo "==> DS type check: no changed frontend *.tsx or *.css — nothing to gate"
  exit 0
fi

echo "==> DS type check (${#CHANGED_TSX[@]} changed *.tsx, ${#CHANGED_CSS[@]} changed *.css vs ${BASE:0:12})"

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
          # True only when ds:ignore carries an actual reason after it — a
          # bare token, or one followed by nothing but whitespace, does not
          # waive the line.
          function waived(line,   p, rest) {
            p = index(line, "ds:ignore")
            if (p == 0) return 0
            rest = substr(line, p + 9)
            gsub(/^[ \t]+/, "", rest); gsub(/[ \t]+$/, "", rest)
            return length(rest) > 0
          }
          /^@@/ {
            match($0, /\+[0-9]+/); ln = substr($0, RSTART + 1, RLENGTH - 1) + 0; next
          }
          /^\+\+\+/ || /^-/ || /^\\/ { next }
          /^\+/ {
            line = substr($0, 2)
            if (!waived(line) && line ~ /(fontSize|fontWeight|letterSpacing)[[:space:]]*:[[:space:]]*[-0-9\x27"]/ && line !~ /(fontSize|fontWeight|letterSpacing)[[:space:]]*:[[:space:]]*[\x27"]?var\(--/)
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
      echo "FAIL: raw type literals in inline styles (new code)"
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

          # Drops every clamp()/calc() built ENTIRELY on --text-* tokens: the
          # expression is arithmetic on the scale, not a size value of its
          # own. ONLY the expression is exempt — whatever else the value
          # carries stays gated. Parentheses are matched by depth so a
          # nested var() does not close the wrapper early.
          function strip_token_fn(value, fname,   out, p, i, depth, start, inner, ch) {
            out = ""
            while ((p = index(value, fname "(")) > 0) {
              out = out substr(value, 1, p - 1)
              start = p + length(fname) + 1
              depth = 1
              for (i = start; i <= length(value); i++) {
                ch = substr(value, i, 1)
                if (ch == "(") depth++
                else if (ch == ")") { depth--; if (depth == 0) break }
              }
              inner = substr(value, start, i - start)
              if (inner ~ /var\(--text-/ && inner !~ /[0-9](px|rem|em)/) out = out fname "(" inner ")"
              value = substr(value, i + 1)
            }
            return out value
          }

          function strip_token_calc(value) {
            value = strip_token_fn(value, "clamp")
            value = strip_token_fn(value, "calc")
            return value
          }

          # True when the value carries a raw px/rem/em length. No \b — the
          # awk on the CI/dev host (the One True Awk) does not support it.
          function raw_length(value,   rest) {
            rest = strip_token_calc(value)
            return rest ~ /([0-9]+(\.[0-9]+)?|\.[0-9]+)(px|rem|em)/
          }

          # True when the value carries a bare number (font-weight, or a
          # unitless letter-spacing, which CSS treats as px).
          function raw_number(value,   rest) {
            rest = strip_token_calc(value)
            gsub(/var\([^)]*\)/, "", rest)
            return rest ~ /(^|[^0-9.])[0-9]+(\.[0-9]+)?([^0-9a-z%]|$)/
          }

          # True only when ds:ignore carries an actual reason after it, up to
          # the closing "*/" — a bare token, or one followed by nothing but
          # whitespace before "*/", does not waive the line. Stripping to the
          # comment terminator matters: naively checking "any non-space
          # follows" would wrongly accept `/* ds:ignore */` because "*/"
          # itself is non-space.
          function waived_css(line,   p, q, rest) {
            p = index(line, "ds:ignore")
            if (p == 0) return 0
            rest = substr(line, p + 9)
            q = index(rest, "*/")
            if (q > 0) rest = substr(rest, 1, q - 1)
            gsub(/^[ \t]+/, "", rest); gsub(/[ \t]+$/, "", rest)
            return length(rest) > 0
          }

          # Judges ONE complete declaration, however many lines it spanned.
          function judge(text, lineno, is_added, is_waived,   prop, value, shown) {
            if (!is_added || is_waived || index(text, ":") == 0) return
            prop = tolower(substr(text, 1, index(text, ":") - 1))
            value = tolower(substr(text, index(text, ":") + 1))
            sub(/^[ \t]+/, "", prop); sub(/[ \t]+$/, "", prop)
            sub(/^[ \t]+/, "", value); sub(/[ \t]+$/, "", value)

            if (prop == "font-size") {
              if (value == "inherit" || value == "0") return
              if (index(value, "var(--text-") > 0 && !raw_length(value)) return
              if (!raw_length(value)) return
            } else if (prop == "font-weight") {
              if (value == "normal" || value == "bold" || value == "inherit") return
              if (index(value, "var(--w-") > 0 && !raw_number(value)) return
              if (!raw_number(value)) return
            } else if (prop == "letter-spacing") {
              if (value == "normal" || value == "0") return
              if (index(value, "var(--track-") > 0 && !raw_length(value) && !raw_number(value)) return
              if (!raw_length(value) && !raw_number(value)) return
            } else {
              return
            }

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
            line_waived = waived_css(raw)
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
      echo "FAIL: raw type literals in stylesheets (new code)"
      CSS_HEADER_DONE=1
    fi
    echo "$hits"
    EXIT=1
  fi
done

if [[ "$EXIT" == "0" ]]; then
  echo "PASS — no new raw type literals"
else
  echo ""
  echo "Use the --text-*/--w-*/--track-* scale (tokens.css) instead of a raw"
  echo "font-size/font-weight/letter-spacing — e.g. font-size: var(--text-sm),"
  echo "style={{ fontWeight: 'var(--w-semibold)' }}."
  echo "A genuine one-off is waived in-line, with a reason, on the offending line:"
  echo "  // ds:ignore <reason>      (.tsx)"
  echo "  /* ds:ignore <reason> */   (.css)"
fi

exit $EXIT
