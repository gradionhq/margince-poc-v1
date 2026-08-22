#!/usr/bin/env bash
# Choke-point gate: three predicates this tree owns in exactly one place, each
# here because a hand-written second copy already shipped and was wrong.
#
#   1. SQLSTATE. The codes the stores branch on are named once, in
#      platform/database/storekit/sqlstate.go. A literal "23505" at a call
#      site is a second spelling of the same judgement, and the copies drift:
#      one path answers a dedupe hit as a conflict, its sibling as a 500.
#
#   2. CONSTRAINT DISCLOSURE. A CHECK breach is answered by
#      platform/httperr's constraint net, which deliberately names NO field —
#      the only thing that knows one at that depth is the constraint name, and
#      that is our schema. Five modules had each re-spelled the translation to
#      put that name in the caller's `field` slot; one of them also pre-empted
#      the net's 423 for a held activity, telling the caller to fix a value
#      when nothing they could send would work until the retention window
#      closed.
#
#   3. ISO-4217 SHAPE. `values.ValidCurrency` is the Go spelling of the
#      schema's currency CHECK. It had three private copies — a rune loop, a
#      regexp under a different name, and a verbatim copy of the values regexp
#      var under the SAME name.
#
# WHAT THIS GATE IS, AND IS NOT. It is a token scanner, not a proof. It catches
# the SHAPES that actually shipped here, cheaply, on every push. It does not
# understand Go: `scale := int64(100)` and then a divide by `scale` is a real
# instance of defect 1 that it will not see, and the same is true of any of the
# four expressed indirectly enough. Treat a green run as "the four known shapes
# are absent", never as "the invariant holds" — the invariants are held by
# values, storekit and httperr owning them, and by review; this only stops the
# regrowth we have already watched happen.
#
# It reads CODE, not prose: comment text is stripped before matching, so the
# explanations above (and the ones in the source that name each defect) do not
# fail the build while the same token in a literal still does. Tests are out of
# scope — they construct these errors and describe these defects on purpose.
#
# A genuine false positive — `protocolMinor / 100` is not money — is waived in
# source, on the offending line, with a reason:
#
#     x := protocolMinor / 100 // one-spelling-exempt: a protocol version, not money
#
# matching the `// rls-exempt: <reason>` escape scripts/check-rls-store-path.sh
# already uses. scripts/test-check-one-spelling.sh proves each arm fires, each
# arm's waiver works, and the named non-money lookalikes stay silent.
set -euo pipefail
cd "$(dirname "$0")/.."

# Every hand-written Go tree. extensions/ and fixtures/ are separate modules but
# the same product; backend/tools is a separate module too and its generators
# emit the code the product runs; backend/pkg is the PUBLISHED surface, where a
# second spelling is worse than internal because a fork inherits it.
#
# Overridable so scripts/test-check-one-spelling.sh can point the same regexes
# at a throwaway tree. It plants deliberate defects, and planting them in the
# real tree makes `make -j` a race — a concurrent one-spelling would read the
# probe as a finding, and gofmt/license/craft would read it as an unlicensed
# file. The default is the real answer; the override only moves where it looks.
# An overridden root is taken WHOLE, not word-split: the self-test hands this a
# mktemp path, and splitting one that contains a space would silently scan two
# directories that do not exist — a gate reading green over an empty universe.
if [[ -n "${ONE_SPELLING_SCAN:-}" ]]; then
  scan=("$ONE_SPELLING_SCAN")
else
  scan=(backend/internal backend/cmd backend/pkg backend/tools extensions fixtures)
fi
waiver='one-spelling-exempt:'

# The gate reads CODE, so the whole tree is stripped of comments ONCE, into a
# `file:line:content` corpus the three arms then grep. Stripping after grep
# cannot work: grep hands on only the lines that already matched, so the line
# that OPENED a multi-line block comment never arrives and its interior looks
# like code. scripts/test-check-one-spelling.sh plants a three-line block
# comment holding two banned tokens; that case is what forced this shape.
#
# Dropped: a whole-line comment, a trailing ` // …`, an inline /* … */ span,
# and the interior of a multi-line block. A line carrying the waiver marker is
# dropped whole.
#
# The residue, stated rather than hidden: a `/*` inside a STRING literal opens a
# block this cannot tell from a real one, and everything to the next `*/` stops
# being scanned. That is a false NEGATIVE in code no author writes by accident,
# and the alternative is lexing Go in awk. The space before // avoids eating a
# `scheme://…` inside a string.
corpus="$(mktemp)"
trap 'rm -f "$corpus"' EXIT
find "${scan[@]}" -type f -name '*.go' \
     ! -name '*_test.go' ! -name '*_gen.go' ! -name '*.gen.go' -print0 \
  | xargs -0 awk -v waiver="$waiver" '
      FNR == 1 { inblock = 0 }
      {
        c = $0
        if (index(c, waiver) > 0) next
        if (inblock) { if (match(c, /\*\//)) { inblock = 0; c = substr(c, RSTART + RLENGTH) } else next }
        t = c; sub(/^[[:space:]]+/, "", t)
        if (t ~ /^(\/\/|\*)/) next
        while (match(c, /\/\*[^*]*\*+([^\/*][^*]*\*+)*\//)) { c = substr(c, 1, RSTART - 1) substr(c, RSTART + RLENGTH) }
        if (match(c, /\/\*/)) { inblock = 1; c = substr(c, 1, RSTART - 1) }
        sub(/[[:space:]]+\/\/.*$/, "", c)
        print FILENAME ":" FNR ":" c
      }' > "$corpus"

# scan_for <regex> [exclude-path-regex]: matching CODE rows, or nothing.
scan_for() {
  grep -nE "$1" "$corpus" 2>/dev/null | cut -d: -f2- \
    | { [[ -n "${2:-}" ]] && grep -vE "$2" || cat; } || true
}

failed=0
report() {
  printf 'FAIL: %s\n%s\n\n' "$1" "$2"
  failed=1
}

# 1. A SQLSTATE spelled at a call site.
# The code list is READ from sqlstate.go, not re-typed: a list typed here is
# itself a second spelling of that census, and the sixth code somebody adds to
# storekit would be silently un-gated by the gate that exists to stop exactly
# that.
sqlstate_src='backend/internal/platform/database/storekit/sqlstate.go'
codes="$(grep -oE '"[0-9]{2}[0-9A-Z]{3}"' "$sqlstate_src" | tr -d '"' | sort -u | paste -sd'|' -)"
[[ -n "$codes" ]] || { echo "FAIL: no SQLSTATE constants found in $sqlstate_src — this gate is reading the wrong file"; exit 1; }
sqlstate="\"($codes)\""
sqlstate_hits="$(scan_for "$sqlstate" 'storekit/sqlstate\.go')"
[[ -n "$sqlstate_hits" ]] && report \
  "SQLSTATE literal outside storekit (use storekit.UniqueViolation / IsForeignKeyViolation / CheckViolation / ExclusionViolation / IsQueryCanceled):" \
  "$sqlstate_hits"

# 2. The wire code the deleted copies used. A path that can name the caller's
#    OWN field still refuses with a specific code (invalid_date_range,
#    amount_currency_pair, project_link_exists); this catches the generic
#    re-spelling of httperr's net, which is the one that carried our
#    constraint name out.
disclosure='"constraint_violated"'
disclosure_hits="$(scan_for "$disclosure")"
[[ -n "$disclosure_hits" ]] && report \
  "a hand-rolled CHECK-to-422 translation (let httperr's constraint net answer, or refuse earlier naming the caller's own field with its own code):" \
  "$disclosure_hits"

# 3. The ISO-4217 shape, spelled again — the REGEXP only. The copies also
#    included a `r < 'A' || r > 'Z'` rune loop, and that is deliberately not
#    matched: it is the shape any string scanner uses, and matching it refuses
#    an alphanumeric word splitter (compose/documentextractvalid.go) that has
#    nothing to do with currency. A gate whose escape hatch gets used routinely
#    stops being read; a narrower one that never cries wolf keeps being read.
currency='\^\[A-Z\]\{3\}\$'
currency_hits="$(scan_for "$currency" 'shared/kernel/values/')"
[[ -n "$currency_hits" ]] && report \
  "a private ISO-4217 shape check (use values.ValidCurrency, so Go and the schema's CHECK admit the same set):" \
  "$currency_hits"

[[ $failed -eq 1 ]] && exit 1
echo "OK: one spelling for SQLSTATE, CHECK refusals and ISO-4217"
