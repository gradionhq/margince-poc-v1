#!/usr/bin/env bash
# ci-verdict.sh — one required check stands for the whole lane, and a SKIPPED job
# is not a passing one when the queue is what is asking.
#
# GitHub counts a skipped required check as PASSING. The diff classifier skips
# whole jobs, so a commit matching no scope reported green having run nothing —
# and that is how a broken tree twice got recorded as gated on main. Collapsing
# nine required contexts into one job would inherit that hole unless the
# aggregate refuses a skip, so this refuses it.
#
# WHAT THIS CATCHES: on `merge_group`, where the verdict IS the merge gate, any
# job that did not SUCCEED — skipped, failed, cancelled alike. Nothing merges on
# unproven ground.
#
# WHAT THIS ADMITS, deliberately: a skip on a pull request. There the classifier
# is doing its job — diff-scoping is honest for author feedback — and the
# merge_group run is what covers the ground a PR lane skipped. Refusing skips on
# both events would mean every docs PR booting twelve Postgres shards, which is
# the cost that produced the classifier in the first place.
#
# Reads CI_VERDICT_NEEDS (a `toJSON(needs)` object) and CI_VERDICT_EVENT
# (github.event_name). Env rather than argv: the JSON is multi-line, and quoting
# it through a shell argument is how a gate ends up judging an empty string.
set -euo pipefail

needs_json="${CI_VERDICT_NEEDS:-}"
event="${CI_VERDICT_EVENT:-}"

if [ -z "$event" ]; then
	echo "FAIL: CI_VERDICT_EVENT is empty, so the verdict cannot tell whether a skip is admissible. Pass \${{ github.event_name }}." >&2
	exit 1
fi

if [ -z "$needs_json" ]; then
	echo "FAIL: CI_VERDICT_NEEDS is empty — there are no job results to judge. Pass \${{ toJSON(needs) }}. A verdict over zero jobs is not a pass." >&2
	exit 1
fi

# Derived from the object rather than a list maintained here, so a job added to
# the aggregate's `needs:` is judged the day it is added.
results="$(printf '%s' "$needs_json" | jq -r 'to_entries[] | "\(.key)\t\(.value.result)"')"
count="$(printf '%s\n' "$results" | grep -c . || true)"

if [ "$count" -eq 0 ]; then
	echo "FAIL: parsed zero job results out of CI_VERDICT_NEEDS — the aggregate's needs: list is empty or the payload changed shape, so this gate was about to pass without judging anything." >&2
	exit 1
fi

case "$event" in
merge_group) admissible="success" ;;
*) admissible="success skipped" ;;
esac

failures=0
while IFS=$'\t' read -r job result; do
	[ -n "$job" ] || continue
	case " $admissible " in
	*" $result "*) continue ;;
	esac
	if [ "$result" = skipped ]; then
		echo "FAIL: $job was SKIPPED, and on $event every job must run. A skipped gate is indistinguishable from a passing one — run it, or take it out of the aggregate's needs: list." >&2
	else
		echo "FAIL: $job reported '$result'." >&2
	fi
	failures=$((failures + 1))
done <<<"$results"

if [ "$failures" -ne 0 ]; then
	echo "FAIL: $failures of $count job(s) did not pass on $event." >&2
	exit 1
fi

echo "OK: all $count job(s) passed on $event"
