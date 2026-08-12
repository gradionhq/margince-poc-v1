#!/usr/bin/env bash
# check-make-target-parity.sh — every backend target `make help` advertises is
# invocable from the repo root, as the help text promises it is.
#
# The root Makefile delegates to backend/ through a hand-maintained list of
# target names, and `make help` prints the backend targets underneath the line
# "each also runnable as `make <name>` from the repo root". A target added to
# backend/Makefile and left out of that list breaks the promise silently: the
# help still advertises it, and `make <name>` from the root answers
#
#   make: *** No rule to make target 'vuln'.  Stop.
#
# which is indistinguishable from a typo. That is not hypothetical. The
# scheduled govulncheck lane ran `make vuln` from the repo root, where the
# target did not exist, and the run failed at the step that was supposed to
# scan — so the lane reported red every day without ever scanning main, and the
# red read as the vulnerability it was meant to find.
#
# WHAT THIS CATCHES: a backend target advertised by `make help` that the root
# Makefile cannot resolve. That is the direction with teeth — it turns a
# documented command into an error, and a caller that is a CI step turns it
# into a gate that never runs.
#
# WHAT THIS DOES NOT CATCH, deliberately: whether a given caller invokes make
# from the right directory. Resolving that means teaching this script how
# GitHub Actions layers workflow, job and step `working-directory`, and a gate
# whose rules are fiddly is a gate that gets worked around rather than fixed.
# Making every advertised target resolve from the root removes the question for
# the direction that has actually failed.
#
# Root targets come from make's own database (`make -qpRr`) rather than a regex
# over the Makefile, so a target reachable by any route counts as reachable.
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
backend_makefile="$root_dir/backend/Makefile"

if [[ ! -f "$backend_makefile" ]]; then
  echo "check-make-target-parity: $backend_makefile not found" >&2
  exit 1
fi

# The backend's advertised surface, spelled the way `make -C backend help`
# spells it: a target whose rule line carries a `## ` description.
advertised="$(grep -hE '^[a-zA-Z][a-zA-Z0-9_-]*:.*## ' "$backend_makefile" | cut -d: -f1 | sort -u)"

# Question mode prints the rule database without running a recipe. It exits
# non-zero whenever the default goal is out of date, which says nothing about
# the dump, so the status is discarded and the emptiness check below is what
# decides whether the dump is usable.
root_targets="$(cd "$root_dir" && make -qpRr --no-print-directory 2>/dev/null || true)"
root_targets="$(printf '%s\n' "$root_targets" |
  grep -E '^[a-zA-Z][a-zA-Z0-9_.-]*:([^=]|$)' | cut -d: -f1 | sort -u)"

# Either list coming back empty would pass this gate while comparing nothing —
# the failure a parity check is most likely to fail by.
for pair in "advertised backend targets:$advertised" "root targets:$root_targets"; do
  if [[ -z "${pair#*:}" ]]; then
    echo "check-make-target-parity: extracted no ${pair%%:*} — the Makefile changed shape, so this gate was about to pass without comparing anything" >&2
    exit 1
  fi
done

missing="$(comm -23 <(printf '%s\n' "$advertised") <(printf '%s\n' "$root_targets"))"

if [[ -n "$missing" ]]; then
  while IFS= read -r target; do
    echo "UNREACHABLE FROM ROOT: \`make $target\` is advertised by \`make help\` but the root Makefile has no such target — add it to the delegation list in Makefile" >&2
  done <<<"$missing"
  exit 1
fi

echo "make target parity OK ($(printf '%s\n' "$advertised" | grep -c .) backend targets, all reachable from the repo root)"
