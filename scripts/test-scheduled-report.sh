#!/usr/bin/env bash
# test-scheduled-report.sh — prove scheduled-report.sh files ONE issue per
# failing check, however long the tracker has grown.
#
# It runs beside the reporter in the scheduled lane for the same reason
# `test-reap-build-caches.sh` runs beside the reaper: the reporter can only be
# observed succeeding, and "it filed something" is not evidence it filed the
# right thing. A dedupe that reads a first page of open issues reports success
# on the day it starts duplicating — the tracker outgrows the page, the
# long-lived issue falls off the end of it, and a second one is filed under a
# title that already had one.
#
# `gh` is stubbed on PATH, so no case reaches the network or the tracker.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stub_dir="$(mktemp -d)"
trap 'rm -rf "$stub_dir"' EXIT
failures=0

# The stub answers the open-issue read from OPEN_TITLES — one `number<TAB>title`
# per line — as the paginated API answers it: a stream of JSON arrays, newest
# first, one per page. Everything the reporter then does to the tracker is
# recorded rather than performed, so a case asserts on the action taken.
cat >"$stub_dir/gh" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
case "$1 ${2:-}" in
"api --paginate")
	# Paginate at 100 so a case can put its match beyond the first page, which
	# is the whole failure being tested.
	awk -F'\t' '
		{ items[NR] = "{\"number\":" $1 ",\"title\":\"" $2 "\"}" }
		END {
			if (NR == 0) { print "[]"; exit }
			for (i = 1; i <= NR; i++) {
				page = page (page == "" ? "" : ",") items[i]
				if (i % 100 == 0) { print "[" page "]"; page = "" }
			}
			if (page != "") print "[" page "]"
		}' <<<"$OPEN_TITLES"
	;;
"issue comment") echo "comment $3" >>"$ACTION_LOG" ;;
"issue create")
	for i in $(seq 1 $#); do
		if [ "${!i}" = "--title" ]; then j=$((i + 1)); echo "create ${!j}" >>"$ACTION_LOG"; fi
		# The body is recorded, not just the title: an arm that files the right
		# issue with the wrong contents is indistinguishable from a working one
		# unless something reads what it wrote.
		if [ "${!i}" = "--body" ] && [ -n "${BODY_LOG:-}" ]; then j=$((i + 1)); printf '%s' "${!j}" >>"$BODY_LOG"; fi
	done
	;;
*) echo "unexpected gh call: $*" >&2; exit 1 ;;
esac
STUB
chmod +x "$stub_dir/gh"
export PATH="$stub_dir:$PATH"

readonly GATE_TITLE="SonarCloud quality gate is not green on main"

# expect <name> <expected-actions> <open-issues-tsv>
#
# Only the quality gate is reported failing, so the expected action is exactly
# one line naming what the reporter did about GATE_TITLE.
expect() {
	local name="$1" want="$2" open="$3" out status got
	export ACTION_LOG="$stub_dir/actions"
	: >"$ACTION_LOG"
	set +e
	out="$(OPEN_TITLES="$open" GH_TOKEN=stub REPO=owner/repo RUN_URL=https://example.test/run/1 \
		GATE_RESULT=failure GATE_STATUS=ERROR \
		"$root/scripts/scheduled-report.sh" 2>&1)"
	status=$?
	set -e
	got="$(paste -sd, - <"$ACTION_LOG")"

	if [ "$status" -ne 0 ] || [ "$got" != "$want" ]; then
		echo "FAIL: $name"
		echo "  exit    want 0 got $status"
		echo "  actions want '$want' got '$got'"
		printf '  output: %s\n' "$out" | head -5
		failures=$((failures + 1))
	else
		echo "ok: $name"
	fi
}

# A tracker of `n` open issues, none of them the reported one, newest first.
noise() {
	local n="$1" i
	for ((i = n; i >= 1; i--)); do printf '%s\t%s\n' "$i" "an unrelated finding $i"; done
}

expect "an empty tracker gets the issue" \
	"create $GATE_TITLE" ""

expect "an open issue under the same title is commented, not re-filed" \
	"comment 40" \
	"$(printf '99\ta later finding\n40\t%s\n' "$GATE_TITLE")"

# The defect: 150 open issues, the match at the far end. A read capped at the
# first 100 files a duplicate here and calls it a clean run.
expect "an open issue past the first page is still found" \
	"comment 7" \
	"$(noise 150 | sed "s/^7\t.*/7\t$GATE_TITLE/")"

# Two issues already carry the title — the state the capped read produced. The
# repeat belongs on the one holding the triage, which is the older number.
expect "duplicates already filed: the oldest keeps the discussion" \
	"comment 12" \
	"$(printf '300\t%s\n12\t%s\n' "$GATE_TITLE" "$GATE_TITLE")"

# Exact titles only: a near miss is a different finding, and folding it into
# this one would hide it behind an issue nobody opened for it.
expect "a similar title is a different finding" \
	"create $GATE_TITLE" \
	"$(printf '55\tSonarCloud quality gate is not green on staging\n')"

# --- main-health arms ----------------------------------------------------------
#
# The health check's whole value is the SUSPECT RANGE it carries: "main is red" was
# already knowable from any other pull request going red. So the assertion is not
# just that an issue is filed — it is that the range reaches the body. A reporter
# that filed the issue and dropped MAIN_SUSPECTS would look identical from the
# outside and be worth nothing.

readonly HEALTH_TITLE="main is red: the integration lane fails on the tip"
readonly SUSPECTS="- \`deadbeef\` Some Author — the commit that did it"

# expect_health <name> <expected-actions> <suspects>
expect_health() {
	local name="$1" want="$2" suspects="$3" out status got body
	export ACTION_LOG="$stub_dir/actions"
	export BODY_LOG="$stub_dir/body"
	: >"$ACTION_LOG"
	: >"$BODY_LOG"
	set +e
	out="$(OPEN_TITLES="" GH_TOKEN=stub REPO=owner/repo RUN_URL=https://example.test/run/1 \
		MAIN_INTEGRATION_RESULT=failure MAIN_SUSPECTS="$suspects" \
		"$root/scripts/scheduled-report.sh" 2>&1)"
	status=$?
	set -e
	got="$(paste -sd, - <"$ACTION_LOG")"
	body="$(cat "$BODY_LOG" 2>/dev/null || true)"

	if [ "$status" -ne 0 ] || [ "$got" != "$want" ]; then
		echo "FAIL: $name"
		echo "  exit    want 0 got $status"
		echo "  actions want '$want' got '$got'"
		printf '  output: %s\n' "$out" | head -5
		failures=$((failures + 1))
		return
	fi
	if [ -n "$suspects" ] && ! printf '%s' "$body" | grep -qF -- "deadbeef"; then
		echo "FAIL: $name — the issue was filed but the suspect range never reached its body"
		failures=$((failures + 1))
		return
	fi
	echo "ok: $name"
}

expect_health "a red integration lane on main is filed with its suspect range" \
	"create $HEALTH_TITLE" "$SUSPECTS"

# No range computed is a degraded report, not a silent one: the issue still has to
# exist, because "main is red" is worth filing even when the window is unknown.
expect_health "a red lane with no range still files" \
	"create $HEALTH_TITLE" ""

if [ "$failures" -ne 0 ]; then
	echo "FAIL: $failures case(s)" >&2
	exit 1
fi
echo "OK: scheduled-report files one issue per check"
