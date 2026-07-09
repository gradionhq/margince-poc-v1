#!/usr/bin/env bash
# Go file-length gate with a ratchet (ported from the foundation skeleton,
# waiver list added). A hand-written Go file above the cap is a god-file split
# candidate; generated (*_gen.go / *.gen.go) and test (*_test.go) files are
# exempt, as is cli/craft (vendored verbatim from the foundation, hash-pinned).
#
# The ratchet: scripts/go-file-length-waivers.txt records each pre-existing
# offender with its frozen line count. A waived file may shrink but never
# grow past its recorded count; once it drops to the cap or below, its entry
# must be REMOVED so the file is back under the hard cap for good.
set -euo pipefail
cd "$(dirname "$0")/.."

CAP="${GO_FILE_LINE_CAP:-500}"
WAIVERS="scripts/go-file-length-waivers.txt"

find backend dev -name "*.go" \
    ! -name "*_test.go" ! -name "*_gen.go" ! -name "*.gen.go" -exec wc -l {} + \
| awk -v cap="$CAP" -v waivers="$WAIVERS" '
BEGIN {
  while ((getline line < waivers) > 0) {
    if (line ~ /^[[:space:]]*#/ || line ~ /^[[:space:]]*$/) continue
    split(line, a, " ")
    waived[a[2]] = a[1] + 0
  }
  close(waivers)
}
$2 == "total" { next }
{
  lines = $1 + 0
  file = $2
  if (file in waived) {
    seen[file] = 1
    if (lines > waived[file]) {
      printf "FAIL: %s grew to %d lines (waiver froze it at %d) — shrink it, never grow it\n", file, lines, waived[file]
      fail = 1
    } else if (lines <= cap) {
      printf "FAIL: %s is down to %d lines (<= %d) — remove its stale entry from %s so the cap re-arms\n", file, lines, cap, waivers
      fail = 1
    }
  } else if (lines > cap) {
    printf "FAIL: %s is %d lines (> %d) — split into one-concept-per-file packages\n", file, lines, cap
    fail = 1
  }
}
END {
  for (f in waived) if (!(f in seen)) {
    printf "FAIL: waiver entry for missing file %s — remove it from %s\n", f, waivers
    fail = 1
  }
  if (fail) {
    printf "FAIL: go-file-length — the %d-LOC cap (ratcheted via %s)\n", cap, waivers
    exit 1
  }
  n = 0; for (f in waived) n++
  printf "OK: go-file-length — no hand-written Go file exceeds %d LOC (waivers: %d)\n", cap, n
}
'
