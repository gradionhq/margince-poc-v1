#!/usr/bin/env bash
# Environment-read gate with a ratchet (plus a waiver list). OPS-CFG-2: config
# is read ONCE at the composition root into a typed, validated struct, and
# everything below it receives its slice by dependency injection rather than
# reaching for the environment itself. A read under backend/internal is
# therefore a package deciding its own configuration, which makes that
# configuration invisible to the process that is supposed to own it — and
# untestable without mutating the environment.
#
# Exempt, for reasons that are categorical rather than historical:
#   - cmd/**            the composition roots; reading the environment is their job
#   - *_test.go         a test may stage its own environment
#   - //go:build integration
#                       the real-infra lane's harnesses (apptest, testdb,
#                       budgettest, the integration harness) read MARGINCE_TEST_*
#                       to find the cluster they were pointed at; they ARE the
#                       composition root of that lane
#   - platform/config   the seam itself: the ONE os.Getenv the product holds, so
#                       that every package below the root can take a Lookup
#                       instead of reaching for the environment. Exempting it is
#                       what makes the rule satisfiable at all
#   - platform/cliflags the flag-to-environment binder itself. It reads the
#                       environment on behalf of a composition root that asked
#                       it to, which is the mechanism this rule presumes, not a
#                       violation of it
#
# The ratchet: scripts/env-read-waivers.txt records each pre-existing offender
# with its frozen count of reads. A waived file may shrink but never grow; once
# it reaches zero, its entry must be REMOVED so the file is covered for good.
set -euo pipefail
cd "$(dirname "$0")/.."

WAIVERS="scripts/env-read-waivers.txt"

# Files behind the integration tag are the test lane's own composition roots.
# Detected by the tag rather than by directory name, so a harness that moves
# does not silently lose its exemption — and a product file cannot gain one by
# being named like a test.
tagged_integration() {
    head -20 "$1" | grep -q '^//go:build .*\binteg''ration\b'
}

# Every way the standard library hands out the environment, not just the one
# spelling in the tree today: a gate that matches os.Getenv alone is satisfied
# by rewriting the call as os.LookupEnv, which reads the same value.
#
# The leading boundary keeps the match to the `os` package itself. Without it a
# receiver whose name merely ENDS in os — myos.Getenv(, _os.LookupEnv( — is read
# as a violation, and a gate that cries wolf gets waived rather than fixed.
ENV_READ='(^|[^[:alnum:]_.])os\.(Getenv|LookupEnv|Environ)\('

report=""
while IFS= read -r file; do
    case "$file" in
        backend/internal/platform/config/*) continue ;;
        backend/internal/platform/cliflags/*) continue ;;
    esac
    tagged_integration "$file" && continue
    # -o counts CALLS, not matching lines: two reads on one physical line are
    # two reads, and a frozen count must not be satisfiable by joining them.
    count=$(grep -oE "$ENV_READ" "$file" | grep -c '' || true)
    report="${report}${count} ${file}
"
done < <(grep -rlE "$ENV_READ" backend/internal --include='*.go' | grep -v '_test\.go$' | sort)

printf '%s' "$report" | awk -v waivers="$WAIVERS" '
BEGIN {
  while ((getline line < waivers) > 0) {
    if (line ~ /^[[:space:]]*#/ || line ~ /^[[:space:]]*$/) continue
    split(line, a, " ")
    waived[a[2]] = a[1] + 0
  }
  close(waivers)
}
NF == 0 { next }
{
  reads = $1 + 0
  file = $2
  if (file in waived) {
    seen[file] = 1
    if (reads > waived[file]) {
      printf "FAIL: %s now makes %d environment reads (waiver froze it at %d) — OPS-CFG-2 wants fewer, never more\n", file, reads, waived[file]
      fail = 1
    }
  } else {
    printf "FAIL: %s reads the environment (%d call(s)) below the composition root — OPS-CFG-2: take the value as a parameter and let cmd/ resolve it\n", file, reads
    fail = 1
  }
}
END {
  for (f in waived) if (!(f in seen)) {
    printf "FAIL: waiver entry for %s, which no longer reads the environment — delete its line from %s so the rule re-arms\n", f, waivers
    fail = 1
  }
  if (fail) {
    printf "FAIL: env-reads — configuration belongs to the composition root (ratcheted via %s)\n", waivers
    exit 1
  }
  n = 0; for (f in waived) n++
  printf "OK: env-reads — no new environment read below the composition root (waivers: %d)\n", n
}
'
