#!/usr/bin/env bash
# test-secret-scan.sh — prove the secret gate still catches.
#
# A scan policy can only fail by being too permissive, and an over-broad
# allowlist is invisible: it reports the same "no leaks found" as a clean tree.
# So every allowlist in .gitleaks.toml that names a file is re-checked here —
# plant a credential-shaped token in that file, and the scan must still fail.
#
# The trap this exists for: a GLOBAL `[[allowlists]]` carrying `paths` makes
# gitleaks skip the whole file before it reads a line, so `condition = "AND"`
# never narrows it and the exemption quietly widens to everything in the file.
# Binding the allowlist to its rule with `targetRules` keeps the file scanned.
# Both scoped entries were written the wrong way first; only this caught it.
#
# The token is generated per run rather than written down: a literal here would
# be a credential-shaped string in the repo, which the gate would rightly flag.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

# The same pinned, checksum-verified scanner the gate itself runs — testing a
# different binary than the one under test would prove nothing about the gate.
# shellcheck source=scripts/gitleaks-pin.sh
. "$root/scripts/gitleaks-pin.sh"
gitleaks="$(gitleaks_bin)"

work="$(mktemp -d)"
backups="$(mktemp -d)"
trap 'rm -rf "$work" "$backups"' EXIT
git archive HEAD | tar -x -C "$work"

# 18 random bytes as 36 hex characters, matching the shape of a GitHub PAT.
# `od` is bounded by -N, so `tr` reads to EOF — piping into a `head` that exits
# early would SIGPIPE the producer, and `pipefail` would end the run at 141.
token="ghp_$(od -An -tx1 -N18 /dev/urandom | tr -d ' \n')"
failures=0

# expect <caught|missed> <path> <why> — plant the token in the export copy of
# <path>, scan, then restore the file so each case is judged alone.
expect() {
	local want="$1" target="$2" why="$3" file="$work/$2" got
	if [[ ! -f "$file" ]]; then
		printf '  FAIL  %s does not exist in the committed tree\n' "$target"
		failures=$((failures + 1))
		return
	fi
	# The backup lives OUTSIDE the scanned tree. Left beside the original it
	# would be scanned too, under a name no allowlist path matches — its own
	# findings would then turn every case green and the suite would pass
	# without testing anything.
	cp "$file" "$backups/orig"
	printf '\nconst plantedToken = "%s"\n' "$token" >>"$file"
	if "$gitleaks" dir "$work" --config "$root/.gitleaks.toml" --redact --no-banner >/dev/null 2>&1; then
		got="missed"
	else
		got="caught"
	fi
	cp "$backups/orig" "$file"

	if [[ "$got" == "$want" ]]; then
		printf '  ok    %-6s  %s\n' "$got" "$target"
	else
		printf '  FAIL  want %s, got %s — %s: %s\n' "$want" "$got" "$target" "$why"
		failures=$((failures + 1))
	fi
}

echo "test-secret-scan: planting a token the allowlists must not hide"

# The scanner runs at all: an ordinary product file carries no exemption.
expect caught "backend/internal/modules/people/person.go" \
	"no allowlist covers this file"

# Every file a scoped allowlist names stays scanned for everything else. These
# are the cases that regress silently when an exemption is written globally.
expect caught "backend/internal/modules/agents/tools_intents.go" \
	"the OpenAPIOp exemption must cover that line only"
expect caught "backend/internal/modules/agents/tools_confirm.go" \
	"the OpenAPIOp exemption must cover that line only"
# The OpenAPIOp exemption is scoped to the whole agents module, so a file that
# carries no exempted line at all must still be scanned — otherwise widening
# the path to the module would have quietly widened the exemption to it.
expect caught "backend/internal/modules/agents/registry.go" \
	"a module-scoped exemption must not exempt the module"
expect caught "frontend/src/screens/onboarding-conversation/connect-scene.tsx" \
	"the LinkedinStatus exemption must cover that secret only"

# Fixtures are exempt wholesale, and deliberately so: a test asserting that a
# credentialed URL is refused has to contain one. Asserted rather than assumed,
# so the trade-off is on the record instead of being something nobody noticed.
expect missed "backend/internal/shared/ports/websearch/sourcepolicy_test.go" \
	"test fixtures are exempt by design"

if [[ "$failures" -ne 0 ]]; then
	echo "test-secret-scan: FAIL ($failures)" >&2
	exit 1
fi
echo "test-secret-scan: ok"
