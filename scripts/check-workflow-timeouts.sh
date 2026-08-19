#!/usr/bin/env bash
# check-workflow-timeouts.sh — every CI job carries a wall-clock ceiling.
#
# A job with no `timeout-minutes` inherits GitHub's default of SIX HOURS. That
# is not a bound, it is an outage: a required check that hangs holds the merge
# for a working day, and while it hangs it is indistinguishable from a queue
# backlog, so nobody even reads it as a failure.
#
# The case that motivated this: a stalled dependency download in the `uat` job
# sat in_progress for 2h20m against a lane that normally finishes in five
# minutes, and had to be cancelled by hand (#1836). Nothing in the product was
# wrong; nothing reported anything either.
#
# Derived from the tree rather than from a list of job names, so a job added
# later is covered the day it is committed — which is why this is a script and
# not a one-time edit.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

report="$(python3 scripts/lib/workflow_timeouts.py .github/workflows/*.yml)"
if [ -n "$report" ]; then
  printf '%s\n' "$report" >&2
  echo "FAIL: the job(s) above would inherit GitHub's six-hour default — add timeout-minutes" >&2
  exit 1
fi
echo "OK: every workflow job carries a timeout-minutes ceiling"
