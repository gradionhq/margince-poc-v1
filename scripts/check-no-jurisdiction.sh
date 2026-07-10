#!/usr/bin/env bash
# Jurisdiction-isolation gate (adapted from the foundation skeleton). A
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
generated='_gen\.go$|\.gen\.go$'

# Drop `file:line:content` rows whose content is a pure comment line (//, /*,
# or a * continuation), leaving only lines that are actual code.
code_only() {
  awk '{ c=$0; sub(/^[^:]*:[^:]*:/,"",c); t=c; sub(/^[[:space:]]+/,"",t);
         if (t !~ /^(\/\/|\/\*|\*)/) print }'
}

# Named regulatory identifiers. Case-SENSITIVE with word boundaries: these are
# proper nouns with fixed spellings, and a case-insensitive match false-fires
# on incidental substrings (e.g. DATEV inside "UpdateVoice").
named='\b(XRechnung|ZUGFeRD|DATEV|GoBD|eIDAS|Impressum)\b'
hits="$(grep -rnE "$named" "$scan" --include='*.go' 2>/dev/null \
  | grep -vE "$seam" | grep -vE "$generated" | grep -v '_test.go' | code_only || true)"

# Conservative ISO-3166: a quoted UPPER-case alpha-2 only when it shares a line
# with a country-ish keyword, so incidental two-letter strings (HTTP verbs,
# enum codes) do not false-fire. The alpha-2 must be upper-case (no -i).
kw='[Cc]ountry|[Jj]urisdiction|[Ii][Ss][Oo][_-]?3166'
iso="($kw).*\"[A-Z]{2}\"|\"[A-Z]{2}\".*($kw)"
iso_hits="$(grep -rnE "$iso" "$scan" --include='*.go' 2>/dev/null \
  | grep -vE "$seam" | grep -vE "$generated" | grep -v '_test.go' | code_only || true)"

if [ -n "$hits" ] || [ -n "$iso_hits" ]; then
  echo "FAIL: jurisdiction-specific strings in core (move to internal/modules/de):"
  [ -n "$hits" ] && echo "$hits"
  [ -n "$iso_hits" ] && echo "$iso_hits"
  exit 1
fi

echo "OK: no jurisdiction strings in core"
