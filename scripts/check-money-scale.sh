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
# (toMinorUnits, toMajorUnits, minorUnitDigits), which MIRRORS the Go table
# rather than asking Intl.
#
# It asked Intl in the first version of this change, and that was the same
# defect one currency over: Intl follows CLDR, which records how a currency is
# USED, and the server follows ISO 4217, which records what the standard
# ASSIGNS. They disagree on ten codes, two of them ordinary spendable money.
# backend/frontendminorunits_test.go holds the mirror in both directions.
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
# Resolved BEFORE the cd, so the library is found however the script is invoked.
COMMENT_SCAN="$(cd "$(dirname "$0")" && pwd)/lib-commentscan.awk"
STRIP_PROG="$(cd "$(dirname "$0")" && pwd)/money-scale-strip.awk"
cd "$(dirname "$0")/.."
for lib in "$COMMENT_SCAN" "$STRIP_PROG"; do
  [[ -f "$lib" ]] || { echo "FAIL: $lib is missing — this gate cannot read code without it"; exit 1; }
done

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
names='[Mm]inor[A-Za-z_]*|MINOR[A-Z_]*'
# Spelled as an ALTERNATION rather than the interval `10{1,4}`.
#
# The powers are matched inside awk, and ERE interval support is the one thing
# awk implementations still differ on — mawk 1.3.3, which shipped as the default
# /usr/bin/awk on Debian and Ubuntu for years, does not have it. It would not
# error: the pattern simply never matches and this gate reports OK over every
# hard-coded scale in the tree. A construct whose failure mode is silent
# universal success is not worth the four characters it saves.
#
# scripts/test-check-money-scale.sh is the belt to this brace: its `fires` cases
# run the real gate, so on any awk where the pattern stopped matching the tests
# go red rather than the gate going quietly green.
powers='[/*%][[:space:]]*(10|100|1000|10000|1_000|10_000)([^0-9._]|$)'

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
  xargs -0 awk -f "$COMMENT_SCAN" -f "$STRIP_PROG" -v waiver="$waiver"
}

# A scan root that does not exist makes find print an error and match nothing,
# and the gate would then say OK having inspected that language not at all —
# green over an empty universe, which is the failure a scanner must never have.
for root in "${go_scan[@]}" "${ts_scan[@]}"; do
  [[ -d "$root" ]] || { echo "FAIL: scan root $root does not exist — this gate would inspect nothing and say OK"; exit 1; }
done

# hits <names-regex> <powers-regex>: the `file:line:statement` rows whose
# STATEMENT carries both a minor-unit name and an integer power of ten.
#
# Matched against the text after `file:line:`, never the whole row: a repository
# path containing "minor" would otherwise make every `/100` beneath it a
# finding. The gate's subject is an identifier in source, not a filename.
hits() {
  awk -F: -v names="$1" -v powers="$2" '{
    stmt = $0
    sub(/^[^:]+:[0-9]+:/, "", stmt)
    if (!(stmt ~ names) || !(stmt ~ powers)) next
    # One spelling carries a "10" and is not a hard-coded scale, so it is
    # removed before the statement is judged again:
    #
    #   10 ** digits   the SANCTIONED form: a power raised to the currency
    #                  digit count is exactly what the owners compute with
    probe = stmt
    gsub(/10[[:space:]]*\*\*/, " ", probe)
    # A grouped literal such as 10_000 is NOT stripped any more. It was, to
    # spare the commissions basis-point divisor, and that blanket also hid
    # `kwdMinor / 1_000`: a genuine hard-coded scale for a three-digit
    # currency, written in the house style for grouped literals. The bps line
    # carries an in-source waiver now, stating its reason where a reader of
    # that line meets it.
    if (probe ~ powers) print
  }'
}

go_hits="$(find "${go_scan[@]}" -type f -name '*.go' \
             ! -name '*_test.go' ! -name '*_gen.go' ! -name '*.gen.go' -print0 2>/dev/null \
           | strip | hits "$names" "$powers" \
           | grep -vE 'shared/kernel/values/' || true)"

ts_hits="$(find "${ts_scan[@]}" -type f \( -name '*.ts' -o -name '*.tsx' \) \
             ! -name '*.test.ts' ! -name '*.test.tsx' ! -name 'schema.d.ts' -print0 2>/dev/null \
           | strip | hits "$names" "$powers" \
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
