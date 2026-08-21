#!/usr/bin/env bash
# main-health-range.sh — the commits that could have broken main, and who wrote them.
#
# A health check that only says "main is red" leaves the same investigation the red
# check on somebody else's pull request already forced: bisect, or ask around. The
# range this prints is what turns that into a name and a commit.
#
# It is bounded by this workflow's last GREEN run, so it is an OVER-approximation:
# every commit that landed since main was last known good. That is deliberate.
# Naming a dozen commits, one of which is responsible, is useful; naming one by
# guessing is worse than naming none, because it sends the wrong person looking.
#
# WHAT THIS CATCHES: the window. Not the culprit.
#
# WHAT IT DOES NOT DO, deliberately: bisect. Halving the range means re-running the
# lane per step, and the lane is ~20 minutes — an hour of runners to save a reader
# thirty seconds of `git log`. The range plus the failing test name has been enough
# every time this has happened so far.
set -euo pipefail

# No apostrophes in these messages: inside ${VAR:?word} bash parses the word, so a
# lone ' opens a quote that never closes and the parse error surfaces dozens of
# lines later, pointing at innocent code.
: "${GH_TOKEN:?the range lookup needs a token to read the history of this workflow}"
: "${REPO:?REPO must name the repository}"
: "${GITHUB_OUTPUT:?this script writes its answer to the step output}"

# How many commits to name when there is no green run to bound the range — a first
# run, or a workflow renamed. Enough to be a lead, few enough to read.
FALLBACK_COMMITS=15

# The last run of THIS workflow that passed. `--status success` is the workflow
# conclusion, so a run whose report job filed an issue is not green and does not
# bound the range: the window stays open until main is actually fixed, which is the
# behaviour that matters when a breakage survives several health checks.
last_green="$(gh run list \
  --repo "$REPO" \
  --workflow main-health.yml \
  --status success \
  --limit 1 \
  --json headSha \
  --jq '.[0].headSha // empty' 2>/dev/null || true)"

range=""
if [ -n "$last_green" ] && git cat-file -e "$last_green^{commit}" 2>/dev/null; then
  range="$last_green..HEAD"
  bound="since the last green health check ($(git rev-parse --short "$last_green"))"
else
  # No green run on record, or its commit is not in this checkout's history —
  # a force-push, a renamed workflow, or the first ever run. Say which, rather
  # than printing a range that quietly means something else.
  range="-n $FALLBACK_COMMITS HEAD"
  bound="the last $FALLBACK_COMMITS commits (no green health check on record to bound the range)"
fi

# %h short sha, %an author, %s subject. git log takes the range unquoted in the
# fallback case because it is two arguments there, not one.
#
# The markdown backtick around the sha comes from printf rather than being typed:
# a literal backtick inside a double-quoted command substitution is still command
# substitution to bash, and the parse error it produces points at a line well past
# the one that caused it. Octal 140 is the character, with none of that.
bt="$(printf '\140')"
# shellcheck disable=SC2086 -- $range is deliberately word-split; see above.
commits="$(git log $range --no-merges --format='- %h %an — %s' | sed -E "s/^- ([0-9a-f]+) /- ${bt}\1${bt} /" || true)"

if [ -z "$commits" ]; then
  commits="_no commits in the range — the breakage predates it, or the range could not be computed._"
fi

count="$(printf '%s\n' "$commits" | grep -c '^- ' || true)"

# Written as a heredoc-delimited multiline output: a bare `name=value` truncates at
# the first newline, which would reduce the whole range to one commit and read as a
# confident single suspect.
{
  echo "suspects<<MAIN_HEALTH_RANGE"
  echo "Landed on \`main\` $bound — $count commit(s), one of which is the likely cause:"
  echo
  printf '%s\n' "$commits"
  echo "MAIN_HEALTH_RANGE"
} >>"$GITHUB_OUTPUT"

echo "range: $bound - $count commit(s)"
