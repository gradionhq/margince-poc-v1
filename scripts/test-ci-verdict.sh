#!/usr/bin/env bash
# test-ci-verdict.sh — prove the single required check refuses a skip on the
# merge queue and admits one on a pull request.
#
# This runs beside the verdict for the reason `test-scheduled-report.sh` runs
# beside the reporter: the verdict can only be observed exiting 0, and "the check
# was green" is precisely the signal that was already untrustworthy. The case
# that matters most is the one that must FAIL — a skipped job on `merge_group` —
# because GitHub counts a skipped required check as a passing one, and that is
# the hole this script exists to close. A verdict that admitted a skip there
# would look identical to a working one on every dashboard.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
verdict="$root/scripts/ci-verdict.sh"
failures=0

# case_is <name> <event> <needs-json> <expected-exit> [substring the output must carry]
case_is() {
	local name="$1" event="$2" needs="$3" want="$4" must_say="${5:-}"
	local out status=0
	out="$(CI_VERDICT_EVENT="$event" CI_VERDICT_NEEDS="$needs" "$verdict" 2>&1)" || status=$?
	if [ "$status" -ne "$want" ]; then
		echo "FAIL: $name — expected exit $want, got $status" >&2
		printf '%s\n' "$out" >&2
		failures=$((failures + 1))
		return
	fi
	if [ -n "$must_say" ] && ! printf '%s' "$out" | grep -qF -- "$must_say"; then
		echo "FAIL: $name — exited $want but never named '$must_say'" >&2
		printf '%s\n' "$out" >&2
		failures=$((failures + 1))
		return
	fi
	echo "ok: $name"
}

all_green='{"dco":{"result":"success"},"integration":{"result":"success"}}'
one_skip='{"dco":{"result":"success"},"integration":{"result":"skipped"}}'
one_fail='{"dco":{"result":"success"},"integration":{"result":"failure"}}'
one_cancel='{"dco":{"result":"success"},"integration":{"result":"cancelled"}}'

case_is "queue, all green" merge_group "$all_green" 0
case_is "pr, all green" pull_request "$all_green" 0

# The whole point. A skip is a verdict about nothing.
case_is "queue rejects a skip" merge_group "$one_skip" 1 "integration"
case_is "pr admits a skip" pull_request "$one_skip" 0

case_is "queue rejects a failure" merge_group "$one_fail" 1 "integration"
case_is "pr rejects a failure" pull_request "$one_fail" 1 "integration"

# A cancelled job proved nothing either, on either event.
case_is "queue rejects a cancellation" merge_group "$one_cancel" 1 "integration"
case_is "pr rejects a cancellation" pull_request "$one_cancel" 1 "integration"

# A verdict over zero jobs is the failure mode this repo already guards against
# in check-ci-doc-parity.sh: it passes while comparing nothing.
case_is "empty needs fails" merge_group "" 1 "CI_VERDICT_NEEDS"
case_is "empty object fails" merge_group '{}' 1 "zero job results"
case_is "empty event fails" "" "$all_green" 1 "CI_VERDICT_EVENT"

if [ "$failures" -ne 0 ]; then
	echo "FAIL: $failures case(s) failed" >&2
	exit 1
fi
echo "OK: ci-verdict holds the line on skips"
