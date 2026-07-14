#!/usr/bin/env bash
# Jurisdiction-isolation gate. A
# fitness function for the pack boundary: country-specific regulatory
# identifiers must live in the jurisdiction seam, never in core. Core code
# that hard-codes a country string cannot be reused across jurisdictions and
# leaks one market's rules into everyone's build.
#
# This repo's seam is internal/modules/de (the German pack) plus
# internal/shared/ports/jurisdiction (the Tier-0 port); everything else under
# internal/ is core and must stay country-neutral. Generated contract code
# (*_gen.go / *.gen.go) and tests (which legitimately exercise pack behavior)
# are out of scope — this gate guards hand-written core source.
#
# The gate reads CODE, not documentation: a comment-only line is dropped
# before matching, so core may name the motivating statute for a GENERIC,
# parameterized mechanism (e.g. the retention floor whose day-count comes from
# policy) while a hard-coded jurisdiction string in a literal or identifier
# still fails the build.
set -euo pipefail
cd "$(dirname "$0")/.."

scan="backend/internal"
seam='/modules/de/|/ports/jurisdiction/'
# Anchored to the path segment of a `file:line:content` grep row (…_gen.go:NN),
# not `$` — which would match the END of the content, so generated files never
# got excluded (the whole point of this filter).
generated='(_gen|\.gen)\.go:[0-9]'

# Strip comments from `file:line:content` rows: drop whole-line comments (//,
# /*, * continuation) AND remove a trailing ` // …` line-comment from a code
# line, then the caller re-matches. This makes the gate read CODE only — a
# statute named in a comment (the header's promise) never fails the build, while
# the same token in a literal/identifier still does. The space before // avoids
# eating a `scheme://…` inside a string.
strip_comments() {
  awk '{
    if (match($0, /^[^:]+:[0-9]+:/)) { prefix=substr($0,1,RLENGTH); c=substr($0,RLENGTH+1) }
    else { prefix=""; c=$0 }
    t=c; sub(/^[[:space:]]+/,"",t)
    if (t ~ /^(\/\/|\/\*|\*)/) next
    sub(/[[:space:]]+\/\/.*$/,"",c)
    print prefix c
  }'
}

# Named regulatory identifiers. Case-SENSITIVE with word boundaries: these are
# proper nouns with fixed spellings, and a case-insensitive match false-fires
# on incidental substrings (e.g. DATEV inside "UpdateVoice").
named='\b(XRechnung|ZUGFeRD|DATEV|GoBD|eIDAS|Impressum)\b'
hits="$(grep -rnE "$named" "$scan" --include='*.go' 2>/dev/null \
  | grep -vE "$seam" | grep -vE "$generated" | grep -v '_test.go' \
  | strip_comments | grep -E "$named" || true)"

# Conservative ISO-3166: a quoted UPPER-case alpha-2 only when it shares a line
# with a country-ish keyword, so incidental two-letter strings (HTTP verbs,
# enum codes) do not false-fire. The alpha-2 must be upper-case (no -i).
kw='[Cc]ountry|[Jj]urisdiction|[Ii][Ss][Oo][_-]?3166'
iso="($kw).*\"[A-Z]{2}\"|\"[A-Z]{2}\".*($kw)"
iso_hits="$(grep -rnE "$iso" "$scan" --include='*.go' 2>/dev/null \
  | grep -vE "$seam" | grep -vE "$generated" | grep -v '_test.go' \
  | strip_comments | grep -E "$iso" || true)"

if [[ -n "$hits" ]] || [[ -n "$iso_hits" ]]; then
  echo "FAIL: jurisdiction-specific strings in core (move to internal/modules/de):"
  [[ -n "$hits" ]] && echo "$hits"
  [[ -n "$iso_hits" ]] && echo "$iso_hits"
  exit 1
fi

echo "OK: no jurisdiction strings in core"
