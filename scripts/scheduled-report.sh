#!/usr/bin/env bash
# scheduled-report.sh — turn a failing scheduled check into a durable issue.
#
# Called only when something in .github/workflows/scheduled.yml failed. A red
# scheduled run notifies essentially nobody by default, which is the whole reason
# these checks moved off the PR path in the first place: nothing was going to make
# someone look. An issue is the artifact that survives until it is closed.
#
# One OPEN issue per check, keyed on an exact title. A daily re-file would bury
# the original and whatever discussion it collected, so a repeat comments on the
# existing issue instead — "still red" costs no second triage.
set -euo pipefail

: "${GH_TOKEN:?the reporting step needs a token to file issues}"
: "${REPO:?REPO must name the repository to file against}"
: "${RUN_URL:?RUN_URL must link the run that produced this verdict}"

# report <title> <label> <body>
report() {
  local title="$1" label="$2" body="$3" existing
  existing="$(gh issue list --repo "$REPO" --state open --limit 100 --json number,title |
    jq -r --arg t "$title" 'map(select(.title == $t)) | .[0].number // empty')"
  if [ -n "$existing" ]; then
    echo "already open as #$existing — commenting"
    gh issue comment "$existing" --repo "$REPO" \
      --body "Still failing on the $(date -u +%Y-%m-%d) scheduled run: $RUN_URL"
    return
  fi
  echo "filing: $title"
  gh issue create --repo "$REPO" --title "$title" --label "$label" --body "$body"
}

if [ "${VULN_RESULT:-}" = "failure" ]; then
  report "govulncheck reports a vulnerability reachable from main" security \
"\`make vuln\` failed on the scheduled run of \`main\`: $RUN_URL

This is the finding a pull-request scan cannot produce. govulncheck answers
against a vulnerability database that changes daily, so a per-PR run proves the
tree was clean **the day it merged** — not today. A vulnerability disclosed after
a merge is only ever found by this lane.

govulncheck reports **reachability**, not mere presence: the affected code is on a
path this module actually calls. That is a stronger signal than a Dependabot
alert on the same package, and it is worth acting on rather than deferring.

Reproduce locally with \`make vuln\`."
fi

if [ "${GATE_RESULT:-}" = "failure" ]; then
  report "SonarCloud quality gate is not green on main" bug \
"The quality gate on \`main\` read \`${GATE_STATUS:-unknown}\` on the scheduled
run: $RUN_URL

\`SonarCloud Code Analysis\` is deliberately **not** a required pull-request check
— that was traded for merge speed during heavy development, to be re-required
once there are production releases. The trade only holds while someone still
reads the gate, so this job reads it and files here when it goes red.

A \`NONE\` status is reported as a failure on purpose: it means no analysis is
attached to \`main\` at all, which looks identical to green on every dashboard.

The failing conditions are printed in the \`quality-gate\` job log of the run
above."
fi

if [ "${LANE_RESULT:-}" = "failure" ]; then
  report "the backend merge gate is red on main" bug \
"\`make check-backend\` failed on the scheduled run of \`main\`: $RUN_URL

Worth reading before assuming the last green run means anything. \`main\`'s
last-known-green is not evidence \`main\` is green: a docs-only commit landing
after a breaking one matches no classifier scope, so every code gate skips and
the run reports green over a broken tree. That has happened more than once, which
is why this lane re-runs the gate unconditionally rather than trusting the last
push's verdict.

So the breakage may predate the most recent commit. Reproduce locally with
\`make check-backend\` on \`main\`."
fi
