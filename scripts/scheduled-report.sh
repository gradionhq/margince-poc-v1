#!/usr/bin/env bash
# scheduled-report.sh — turn a failing scheduled check into a durable issue.
#
# Called when a check in scheduled.yml OR main-health.yml failed. A red
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
#
# The lookup reads EVERY open issue, not a first page of them. A capped read
# answers "no issue with this title" the moment the tracker outgrows the cap —
# and because the cap pages newest-first, the issue it stops short of is
# precisely the long-lived one this dedupe exists to find. The tracker passing
# that mark is invisible from here: nothing fails, a second issue is simply
# filed under a title that already had one, and the discussion on the first is
# left behind. The oldest open match therefore wins: it is the one carrying
# whatever triage the title has already collected.
report() {
  local title="$1" label="$2" body="$3" existing
  existing="$(gh api --paginate "repos/$REPO/issues?state=open&per_page=100" |
    jq -rs --arg t "$title" '[.[][] | select(has("pull_request") | not)
      | select(.title == $t)] | min_by(.number) | .number // empty')"
  if [ -n "$existing" ]; then
    echo "already open as #$existing — commenting"
    gh issue comment "$existing" --repo "$REPO" \
      --body "Still failing on the $(date -u +%Y-%m-%d) scheduled run: $RUN_URL"
    return
  fi
  echo "filing: $title"
  gh issue create --repo "$REPO" --title "$title" --label "$label" --body "$body"
}

# Each report is attempted independently. Under `set -e` a bare call would abort
# the script, so a single `gh` hiccup on the first finding would silently suppress
# every later one — and a lane whose job is "say what is broken" must not report
# one failure and swallow two. Failures accumulate and surface at the end instead.
unreported=0

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

Reproduce locally with \`make vuln\`."\
    || unreported=1
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
above."\
    || unreported=1
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
\`make check-backend\` on \`main\`."\
    || unreported=1
fi

# Two findings, not one, because the job result cannot tell them apart: a failed
# checkout, a cold cache, a refused database or a hung seed all arrive here as
# `failure`. Filing "a budget is breaching" for those sends somebody bisecting a
# regression that never happened — which is the failure this whole lane keeps
# learning about. PERF_OUTCOME is set by the step from the harness's own breach
# message, so only a MEASURED breach is reported as one.
if [ "${PERF_RESULT:-}" = "failure" ] && [ "${PERF_OUTCOME:-}" != "breach" ]; then
  report "the weekly PERF-3/PERF-7 run could not complete" bug \
"\`make bench-perf-check\` failed on the weekly run of \`main\` WITHOUT reaching a
budget verdict: $RUN_URL

No budget is known to be breaching, and none is known to hold — the lane did not
get far enough to say. The step separates the two outcomes precisely so this
issue does not claim a regression that was never measured.

Likely causes, in the order they occur: the runner could not reach the Postgres
service, \`bench_db\` could not create \`margince_bench\`, or the SMB seed
outran \`go test\`'s 20m budget (which would have printed a goroutine dump — if
the job was killed at 30m instead, the runner was slower than the budget assumes
and the two timeouts need re-deriving, not raising).

Reproduce with \`make db-up && make bench-perf-check\`. The published budgets
page is untouched either way: this lane never sets MARGINCE_BENCH_RECORD."\
    || unreported=1
fi

if [ "${PERF_OUTCOME:-}" = "breach" ]; then
  report "a PERF-3/PERF-7 budget is breaching on main" bug \
"\`make bench-perf-check\` failed on the weekly run of \`main\`: $RUN_URL

This alarm runs the SMB tier only, and that shapes what the finding means: SMB
is the corpus most installations look like, while the PERF-7 SLO binds at
mid-market. So a breach here is worse than it sounds — the SMALLER corpus is
already over a bound the larger one has to meet. Mid-market is not measured by
any schedule; run \`make bench-perf\` by hand to see it.

No record was written — this lane never sets \`MARGINCE_BENCH_RECORD\`, so the
published page still shows the last number a human measured. Reproduce with
\`make db-up && make bench-perf-check\`, and publish a new number with
\`make bench-perf\` only once the breach is understood.

This budget carried no merge gate: PERF-3/PERF-7 left the integration lane
because a mid-market SLO gated on an SMB corpus renders \`inconclusive\`, never
\`within budget\`. So the breach may predate this run by up to a week, and
bisecting is the honest first move rather than assuming the newest commit."\
    || unreported=1
fi

if [ "${CLOCK_RESULT:-}" = "failure" ]; then
  report "the frontend suite's verdict depends on the calendar" bug \
"\`make fe-clock-drift\` failed on the scheduled run of \`main\`: $RUN_URL

The suite passes today and fails 200 days from now, which means at least one test
asserts against a fixture date the component compares to \`now\`. It will start
failing on its own, on a commit that touches nothing near it — that is #1977, and
it hid on \`main\` for a month, because the change classifier skips the frontend
jobs for a commit touching no frontend path and a skipped required check is the
same colour as a passing one.

Fix it at the fixture, not at the clock. Pin the clock for the file
(\`vi.setSystemTime\` in \`beforeEach\`, as \`connected-agents.test.tsx\` does) so the
test asserts the state its fixture describes — or move the fixture date far enough
out that it says \"effectively never\" and means it. A per-case stub is the third
copy of a guard the file wants once.

Reproduce locally with \`make fe-clock-drift\`, and read the failures as claims
about the fixtures rather than about the components."\
    || unreported=1
fi

if [ "${CACHE_RESULT:-}" = "failure" ]; then
  report "the Actions build-cache reaper is failing" bug \
"\`scripts/reap-build-caches.sh\` failed on the scheduled run: $RUN_URL

This one degrades quietly, which is why it is filed rather than left as a red
run. A repository gets 10 GB of Actions cache and one push to \`main\` writes
about 5 GB of Go build cache, so without the reaper the quota fills and GitHub
evicts least-recently-used — which reaches \`node-cache\`, \`gate-binaries\` and
\`setup-go\` first, because each is read once per run while a build cache is read
by every Go job. Nothing breaks; the cheap caches are evicted by the expensive
ones and several lanes just get slower, with no failure to point at.

The reaper is written to refuse rather than guess, so the likely cause is a
deliberate change it cannot interpret: the cache key shape moved. If
\`.github/actions/go-build-cache\` no longer ends its key with a commit sha, or
the \`go-build-\` prefix changed, teach the script the new shape —
\`scripts/test-reap-build-caches.sh\` covers both refusals.

Inspect without deleting anything: \`DRY_RUN=1 scripts/reap-build-caches.sh\`."\
    || unreported=1
fi

# --- main-health.yml -----------------------------------------------------------
#
# The two arms below are filed by the two-hourly health check rather than the daily
# lane, and they are the ones that carry a SUSPECT RANGE. That is the whole reason
# that workflow exists: a merge can land over a red `ci` here (a repository-role
# bypass is deliberate), so the question is never "how do we stop it" but "how
# quickly does somebody learn, and whose commit was it". Naming the window is what
# turns a red lane on an unrelated pull request into a fixable finding.
#
# MAIN_SUSPECTS is an over-approximation — every commit since the health check was
# last green. Printed as-is rather than narrowed: a guessed culprit sends the wrong
# person looking, which is worse than a dozen candidates and a failing test name.

if [ "${MAIN_GATES_RESULT:-}" = "failure" ]; then
  report "main is red: the backend gate fails on the tip" bug \
"\`make check-backend\` failed against \`main\` on the two-hourly health check:
$RUN_URL

This is the no-database half of the merge gate, re-run unconditionally. That
matters: the classifier that makes a pull request cheap is exactly what let a
docs-only commit report green over a broken tree, so \`main\`'s last-known-green
is not evidence \`main\` is green.

${MAIN_SUSPECTS:-_no suspect range was computed for this run._}

Reproduce locally on \`main\` with \`make check-backend\`."\
    || unreported=1
fi

if [ "${MAIN_INTEGRATION_RESULT:-}" = "failure" ]; then
  report "main is red: the integration lane fails on the tip" bug \
"The real-Postgres lane failed against \`main\` on the two-hourly health check:
$RUN_URL

Every schema fitness gate lives in this lane and nowhere else — an unratified
DELETE trigger on a sweep target, a foreign key with no visibility decision — and
that family is what has broken \`main\` repeatedly. The failing test names which
one; the run above has the shard log.

${MAIN_SUSPECTS:-_no suspect range was computed for this run._}

Reproduce locally with \`make db-up && make test-integration\` on \`main\`. Until
this is fixed, every other pull request inherits the failure through its merge
commit and reads as red for a reason its author did not cause."\
    || unreported=1
fi

if [ "$unreported" -ne 0 ]; then
  echo "FAIL: at least one finding could not be filed — the run above names what was broken, but an issue for it does not exist" >&2
  exit 1
fi
