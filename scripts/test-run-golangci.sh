#!/usr/bin/env bash
# test-run-golangci.sh — prove run-golangci.sh separates a finding in this
# checkout from one golangci's cache remembers from another worktree.
#
# The guard exists because a stale-cache run reads exactly like a real failure
# (issue #1378), so the way it fails is a guard that reads exactly like a
# working one: a check that flagged EVERY run stale, or none, passes any test
# that only looks at the case it was written for. Both directions are asserted
# here, and the case that separates them is deliberately awkward — `cli/craft`
# reports as `../cli/craft/...`, a path that leads with `..` and is entirely
# inside the repo. A guard that looked for `../` instead of resolving the path
# would pass every other case in this file and quarantine the whole module.
#
# golangci-lint is stubbed on PATH, so no case runs a linter or touches the real
# analysis cache — the fixtures are its text output, which is the only input the
# guard reads.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
stub_dir="$(mktemp -d)"
trap 'rm -rf "$stub_dir"' EXIT
failures=0

# The stub prints the fixture output and exits with the fixture status, which is
# everything run-golangci.sh consumes from the real binary.
cat >"$stub_dir/golangci-lint" <<'STUB'
#!/usr/bin/env bash
printf '%s' "$STUB_OUT"
exit "$STUB_EXIT"
STUB
chmod +x "$stub_dir/golangci-lint"

# expect <name> <want-exit> <want-stale: yes|no> <stub-exit> <stub-output>
#
# Every case runs from cli/craft with the config in backend/, the awkward
# geometry the real gate uses: the module is a sibling of the config's
# directory, so its own files report with a leading `../`.
expect() {
	local name="$1" want_exit="$2" want_stale="$3" stub_exit="$4" stub_out="$5"
	local out status stale=no

	set +e
	out="$(cd "$root/cli/craft" &&
		GOLANGCI_LINT="$stub_dir/golangci-lint" STUB_EXIT="$stub_exit" STUB_OUT="$stub_out" \
			"$root/scripts/run-golangci.sh" run --config "$root/backend/.golangci.yml" ./... 2>&1)"
	status=$?
	set -e

	case "$out" in *"STALE CACHE"*) stale=yes ;; esac

	if [ "$status" -ne "$want_exit" ] || [ "$stale" != "$want_stale" ]; then
		echo "FAIL: $name"
		echo "  exit  want $want_exit got $status"
		echo "  stale want $want_stale got $stale"
		printf '  output: %s\n' "$out" | head -20
		failures=$((failures + 1))
	else
		echo "ok: $name"
	fi
}

# A run with nothing to say stays a pass. The guard reads the same output every
# run produces, so "flags a clean run" is a live way for it to be wrong.
expect "clean run passes" 0 no 0 '0 issues.
'

# The module's OWN findings, at the path golangci really reports them from:
# relative to the config's directory, so up out of backend/ and back down. This
# is the case a naive `starts with ../` test gets wrong.
expect "a finding in this checkout is a finding" 1 no 1 '../cli/craft/main.go:100:2: use of `fmt.Println` forbidden because "use slog, not fmt.Print*" (forbidigo)
1 issues:
* forbidigo: 1
'

# Findings inside the config's own module report with no prefix at all.
expect "a finding under the config directory is a finding" 1 no 1 'tools/gen-jobs/main.go:71:2: use of `fmt.Printf` forbidden because "use slog, not fmt.Print*" (forbidigo)
'

# The reported case: a worktree that was removed minutes earlier, whose paths
# climb one level further than any real one can.
expect "a finding from a sibling worktree is quarantined" 40 yes 1 '../../margince-next-erase/backend/tools/gen-jobs/main.go:71:2: use of `fmt.Printf` forbidden because "use slog, not fmt.Print*" (forbidigo)
../../margince-next-erase/cli/craft/main.go:100:2: use of `fmt.Println` forbidden because "use slog, not fmt.Print*" (forbidigo)
2 issues:
* forbidigo: 2
'

# Same entry, absolute rather than relative: `run.relative-path-mode` is
# configurable and a golangci upgrade could change its default, which would slip
# every foreign path past a guard that only understood the relative spelling.
# The case the guard was blind to, and the one this repo actually produces:
# EnterWorktree puts a worktree under .claude/worktrees/, which resolves INSIDE
# the root. A guard that asked only "is this path outside the checkout" read a
# parallel session's cached findings as this checkout's own — under module names
# that do exist here, which is the whole failure #1378 describes.
expect "a finding from a worktree INSIDE this repo is quarantined" 40 yes 1 '../.claude/worktrees/feat+desktop-bundle/extensions/zalo-oa/oauth.go:33:2: G101: Potential hardcoded credentials (gosec)
1 issues:
* gosec: 1'

expect "an absolute path outside the checkout is quarantined" 40 yes 1 '/somewhere/else/margince-next-sed/backend/tools/extmigrategate/main.go:114:4: use of `fmt.Println` forbidden because "use slog, not fmt.Print*" (forbidigo)
'

# Mixed: the poisoned entry does not excuse the gate from reporting, and the
# stale verdict wins because the run could not apply this checkout's waivers to
# either finding.
expect "one foreign path taints the run" 40 yes 1 '../cli/craft/main.go:100:2: use of `fmt.Println` forbidden because "use slog, not fmt.Print*" (forbidigo)
../../margince-next-erase/cli/craft/gate/marker.go:81:18: G304: Potential file inclusion via variable (gosec)
'

# `--color always` survives a pipe and a caller may pass it. The escape codes
# carry no space, so they ride into the path unless they are stripped first —
# and then EVERY path resolves to somewhere that is not this checkout.
esc=$'\033'
expect "a coloured finding in this checkout is a finding" 1 no 1 \
	"${esc}[1m../cli/craft/main.go:100:2:${esc}[0m use of \`fmt.Println\` forbidden ${esc}[90m(forbidigo)${esc}[0m
"
expect "a coloured foreign path is still quarantined" 40 yes 1 \
	"${esc}[1m../../margince-next-erase/cli/craft/main.go:100:2:${esc}[0m use of \`fmt.Println\` forbidden ${esc}[90m(forbidigo)${esc}[0m
"

# A poisoned entry for a large package names far more files than a reader needs
# to see. The list is capped, and a cap that does not announce itself reads as
# the whole set.
many=""
for i in 1 2 3 4 5 6 7 8 9 10 11 12; do
	many="$many../../margince-next-erase/backend/tools/gen-$i/main.go:1:1: use of \`fmt.Printf\` forbidden (forbidigo)
"
done
capped_out="$(cd "$root/cli/craft" &&
	GOLANGCI_LINT="$stub_dir/golangci-lint" STUB_EXIT=1 STUB_OUT="$many" \
		"$root/scripts/run-golangci.sh" run --config "$root/backend/.golangci.yml" ./... 2>&1 || true)"
for needle in "reported 12 file(s)" "and 2 more"; do
	case "$capped_out" in
	*"$needle"*) echo "ok: the capped list says '$needle'" ;;
	*)
		echo "FAIL: the capped list dropped '$needle'"
		failures=$((failures + 1))
		;;
	esac
done

# The quarantine keeps the evidence: a reader who does not believe the verdict
# has to be able to see the paths it read it from.
stale_out="$(cd "$root/cli/craft" &&
	GOLANGCI_LINT="$stub_dir/golangci-lint" STUB_EXIT=1 \
		STUB_OUT='../../margince-next-erase/cli/craft/main.go:100:2: use of `fmt.Println` forbidden (forbidigo)
' "$root/scripts/run-golangci.sh" run --config "$root/backend/.golangci.yml" ./... 2>&1 || true)"
for needle in "margince-next-erase/cli/craft/main.go" "cache clean" "go env GOPATH"; do
	case "$stale_out" in
	*"$needle"*) echo "ok: the diagnosis carries '$needle'" ;;
	*)
		echo "FAIL: the diagnosis dropped '$needle'"
		failures=$((failures + 1))
		;;
	esac
done

if [ "$failures" -ne 0 ]; then
	echo "FAIL: $failures case(s) — run-golangci.sh does not separate a stale-cache run from a real one"
	exit 1
fi

echo "OK: run-golangci.sh tells a finding in this checkout from one cached against another"
