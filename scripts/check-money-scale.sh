#!/usr/bin/env bash
# Money-scale gate: an amount in minor units is converted by the one owner of
# the ISO minor-unit table, never by a hard-coded power of ten.
#
# WHY IT SPANS TWO LANGUAGES. This is the only gate in the tree that scans Go
# and TypeScript together, and it has to. The scale is a contract BETWEEN them:
# the browser sends `amount_minor` and the server renders it. When only one side
# was currency-aware the two disagreed, and the disagreement was invisible
# precisely because it was symmetric — the frontend stored a dong price times a
# hundred and displayed it divided by a hundred, so the screen agreed with
# itself and only the record was wrong. A Go-only gate would have called that
# tree clean.
#
# WHAT WAS WRONG. `/ 100` is right for the euro and wrong for every currency
# that is not two-decimal. VND, JPY and KRW have no minor unit at all — the
# integer IS the amount — and KWD has three. Four Go sites divided by a hundred
# (three prose surfaces, byte-identical, plus the offer PDF a buyer signs) and
# eleven TypeScript sites multiplied by one. A ₫18,000,000 offer printed
# "180000.00 VND".
#
# THE OWNERS. Go: internal/shared/kernel/values (MajorUnits, WholeMajorUnits,
# MinorUnits, MinorUnitDigits). TypeScript: src/format/minorunits
# (toMinorUnits, toMajorUnits, minorUnitDigits), which reads the count from Intl
# rather than keeping a second table.
#
# WHAT THIS GATE IS NOT. A token scanner, not a proof. `scale := 100` and then a
# divide by `scale` is a real instance it will not see, and so is any of it
# expressed indirectly enough. A green run means the known shape is absent.
#
# It reads CODE, not comments, and a genuine false positive — `protocolMinor /
# 100` is a version, not money — is waived on the line:
#
#     x := protocolMinor / 100 // money-scale-exempt: a protocol version, not money
#
# scripts/test-check-money-scale.sh proves each language's arm fires, that the
# waiver works and is line-scoped, and that comments are not code.
set -euo pipefail
cd "$(dirname "$0")/.."

waiver='money-scale-exempt:'
IFS=' ' read -r -a go_scan <<< "${MONEY_SCALE_GO_SCAN:-backend/internal backend/cmd backend/pkg backend/tools extensions fixtures}"
IFS=' ' read -r -a ts_scan <<< "${MONEY_SCALE_TS_SCAN:-frontend/src}"

# What a finding is, now that the unit of judgement is a STATEMENT: the
# statement mentions a minor-unit amount AND applies an integer power of ten.
#
# Two conditions rather than one adjacency pattern, because the write direction
# does not put them next to each other — `amountMinor = Math.round(major * 100)`
# has the name at one end and the multiply at the other, and an adjacency
# matcher reads it as clean. That is the whole half a Go-only, line-scoped gate
# missed.
#
# The power of ten must be an INTEGER. Money is an integer count of minor units,
# so `/ 100.0` is a rate — the weighted-pipeline SQL divides by a percentage and
# has nothing to do with the minor unit.
#
# The pairing is bounded by the statement, and the statement by four lines, so
# "somewhere in this file" is never enough to fire.
names='[Mm]inor[A-Za-z_]*'
powers='[/*%][[:space:]]*1000?0?([^0-9.]|$)'

# strip <files…>: emit `file:line:statement` with comments removed and
# CONTINUATION LINES JOINED, so a wrapped expression is judged whole.
#
# The join is not tidiness. The defect's real shape puts the amount and the
# power of ten in one expression, and a formatter routinely breaks that across
# lines:
#
#     const minor = Math.round(
#       Number(amount) * 100,
#     );
#
# A line-scoped matcher reads the multiply and the `minor` as unrelated and
# reports nothing. scripts/test-check-money-scale.sh plants exactly that and it
# is what forced this — the first version of this gate missed the whole write
# direction, which is the half a Go-only gate could not see either.
#
# Lines accumulate while brackets are open, and the buffer is reported at the
# line it STARTED on. Comments go first: a whole-line comment, a trailing one,
# an inline /* … */, and the interior of a multi-line block, which needs
# per-file state.
#
# The residue, stated rather than hidden: a `/*` or an unbalanced bracket inside
# a STRING literal confuses the accumulator, and a `//` inside one truncates the
# line. Both are false NEGATIVES — the gate under-reports rather than crying
# wolf, which is the direction a scanner should fail in.
strip() {
  xargs -0 awk -v waiver="$waiver" '
    function opens(s,   n, t) { t = s; n = gsub(/[([]/, "", t); return n }
    function closes(s,  n, t) { t = s; n = gsub(/[)\]]/, "", t); return n }
    function flush() { if (buf != "") print FILENAME ":" start ":" buf; buf = ""; depth = 0; lines = 0 }
    FNR == 1 { flush(); inblock = 0 }
    {
      c = $0
      if (index(c, waiver) > 0) { flush(); next }
      if (inblock) { if (match(c, /\*\//)) { inblock = 0; c = substr(c, RSTART + RLENGTH) } else next }
      t = c; sub(/^[[:space:]]+/, "", t)
      if (t ~ /^(\/\/|\*)/) next
      while (match(c, /\/\*[^*]*\*+([^\/*][^*]*\*+)*\//)) { c = substr(c, 1, RSTART - 1) substr(c, RSTART + RLENGTH) }
      if (match(c, /\/\*/)) { inblock = 1; c = substr(c, 1, RSTART - 1) }
      sub(/[[:space:]]+\/\/.*$/, "", c)
      if (t == "") { flush(); next }
      if (buf == "") start = FNR
      buf = buf " " c
      lines++
      depth += opens(c) - closes(c)
      # Bounded at four lines. A wrapped expression is two to four; a `const (`
      # block is thirty, and joining one turns every unrelated pairing inside it
      # into a finding — measured on compose/report.go, whose const block holds
      # `amount_minor` and a `/ 100.0` thirty lines apart with nothing to do
      # with each other. A blank line ends a statement too.
      if (depth <= 0 || lines >= 4) flush()
    }
    END { flush() }'
}

go_hits="$(find "${go_scan[@]}" -type f -name '*.go' \
             ! -name '*_test.go' ! -name '*_gen.go' ! -name '*.gen.go' -print0 2>/dev/null \
           | strip | grep -E "$names" | grep -E "$powers" \
           | grep -vE 'shared/kernel/values/' || true)"

ts_hits="$(find "${ts_scan[@]}" -type f \( -name '*.ts' -o -name '*.tsx' \) \
             ! -name '*.test.ts' ! -name '*.test.tsx' ! -name 'schema.d.ts' -print0 2>/dev/null \
           | strip | grep -E "$names" | grep -E "$powers" \
           | grep -v 'format/minorunits' || true)"

if [[ -n "$go_hits" || -n "$ts_hits" ]]; then
  echo "FAIL: an amount in minor units is scaled by a hard-coded power of ten."
  echo "      A currency with no minor unit (VND, JPY, KRW) is then understated a hundredfold,"
  echo "      and a three-decimal one (KWD) is overstated tenfold."
  [[ -n "$go_hits" ]] && { echo "  Go — use values.MajorUnits / WholeMajorUnits / MinorUnits:"; echo "$go_hits"; }
  [[ -n "$ts_hits" ]] && { echo "  TypeScript — use format/minorunits toMinorUnits / toMajorUnits:"; echo "$ts_hits"; }
  exit 1
fi

echo "OK: one money scale, in Go and in TypeScript"
