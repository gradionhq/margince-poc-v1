#!/usr/bin/env bash
# test-secret-scan.sh — prove the secret gate still catches.
#
# A scan policy can only fail by being too permissive, and an over-broad
# allowlist is invisible: it reports the same "no leaks found" as a clean tree.
# So every allowlist in .gitleaks.toml that names a file is re-checked here —
# plant a credential-shaped token in that file, and the scan must still fail.
#
# TWO properties, and the second is the one a single plant cannot reach:
#
#   (a) an allowlist does not hide ANOTHER rule's finding. A global
#       `[[allowlists]]` carrying `paths` makes gitleaks skip the whole file
#       before it reads a line, so `condition = "AND"` never narrows it and the
#       exemption widens to everything in the file. `targetRules` is what keeps
#       the file scanned. Both scoped entries were written the wrong way first;
#       only this caught it.
#
#   (b) an allowlist does not hide ITS OWN rule's finding on a line the
#       allowlist regex does not match. Nothing about (a) implies this, and it
#       is the property the policy file's "the file stays scanned for
#       everything else" is read as promising. Measured against the pinned
#       scanner, three separate one-line omissions each widen a scoped
#       exemption to the whole file for its own rule: dropping
#       `condition = "AND"` (which then defaults to OR), dropping `regexes`,
#       and dropping `targetRules`.
#
# So the plants are DERIVED from the policy rather than listed here: every
# allowlist that names `targetRules` owes one plant per rule it targets, in a
# file its own `paths` cover, plus a foreign-rule plant for (a). A hand-kept
# list goes stale the first time somebody adds an allowlist — and an allowlist
# nobody planted against is exactly the one that is over-broad.
#
# The suite FAILS CLOSED. An allowlist that targets a rule with no plant recipe
# below, or names a path no tracked file matches, is an UNGATED allowlist and
# is reported as a failure rather than skipped.
#
# Every recipe is proved on a file no allowlist covers before it is trusted
# anywhere else: an assertion that a plant is still caught passes for free if
# the plant never tripped its rule at all.
#
# The tokens are generated per run rather than written down: a literal here
# would be a credential-shaped string in the repo, which the gate would rightly
# flag.
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

failures=0
fail() {
	printf '  FAIL  %s\n' "$1"
	failures=$((failures + 1))
}

# `od` is bounded by -N, so `tr` reads to EOF — piping into a `head` that exits
# early would SIGPIPE the producer, and `pipefail` would end the run at 141.
hex() { od -An -tx1 -N"$1" /dev/urandom | tr -d ' \n'; }

# plant_line <rule-id> — a line that trips EXACTLY that rule, or exit 1 for a
# rule this suite does not know how to plant for. Each shape was measured
# against the pinned scanner; the sanity pass below re-measures it every run,
# so a rule set that moves under us is a failure here rather than a silent
# weakening of every assertion that follows.
plant_line() {
	case "$1" in
	generic-api-key) printf 'const plantedApiKey = "%s"\n' "$(hex 24)" ;;
	linkedin-client-id) printf 'const planted_linkedin_client_id = "%s"\n' "$(hex 7)" ;;
	github-pat) printf 'const plantedToken = "ghp_%s"\n' "$(hex 18)" ;;
	*) return 1 ;;
	esac
}

# The rule every allowlist here is foreign to, so it carries property (a) for
# each of them: no allowlist in the policy targets it.
FOREIGN_RULE="github-pat"

# expect <caught|missed> <path> <rule-id> <why> — plant a token of <rule-id> in
# the export copy of <path>, scan, then restore the file so each case is judged
# alone.
expect() {
	local want="$1" target="$2" rule="$3" why="$4" file="$work/$2" got line
	if [[ ! -f "$file" ]]; then
		fail "$target does not exist in the committed tree"
		return
	fi
	if ! line="$(plant_line "$rule")"; then
		fail "no plant recipe for rule '$rule' — $target is ungated. Add one to plant_line()."
		return
	fi
	# The backup lives OUTSIDE the scanned tree. Left beside the original it
	# would be scanned too, under a name no allowlist path matches — its own
	# findings would then turn every case green and the suite would pass
	# without testing anything.
	cp "$file" "$backups/orig"
	printf '\n%s' "$line" >>"$file"
	if "$gitleaks" dir "$work" --config "$root/.gitleaks.toml" --redact --no-banner >/dev/null 2>&1; then
		got="missed"
	else
		got="caught"
	fi
	cp "$backups/orig" "$file"

	if [[ "$got" == "$want" ]]; then
		printf '  ok    %-6s  %-18s  %s\n' "$got" "$rule" "$target"
	else
		fail "want $want, got $got — $target vs $rule: $why"
	fi
}

# --- the policy, read rather than restated -----------------------------------
# One record per allowlist field: <index>|desc|<text>, <index>|rule|<rule id>,
# <index>|path|<path regex>. A global allowlist (no targetRules) emits no rule
# record, which is how the loops below tell the two kinds apart.
read_policy() {
	awk '
		# Values are TOML basic strings ("...") or literal strings (\x27\x27\x27...\x27\x27\x27),
		# and an array may span lines, so a value is buffered until its bracket
		# closes and then split on its own delimiter — exactly, rather than
		# matched with a regex a quote inside a pattern would break.
		function flush(   n, i, parts, sep, kind) {
			if (key == "") return
			sep = (index(buf, "\x27\x27\x27") > 0) ? "\x27\x27\x27" : "\""
			kind = (key == "targetRules") ? "rule" : (key == "paths" ? "path" : "desc")
			n = split(buf, parts, sep)
			for (i = 2; i <= n; i += 2) print idx "|" kind "|" parts[i]
			key = ""; buf = ""
		}
		/^\[\[allowlists\]\]/ { flush(); idx++; next }
		/^\[/                 { flush(); idx = 0; next }
		idx == 0              { next }
		{
			if (key == "") {
				if ($0 !~ /^(description|targetRules|paths)[ \t]*=/) next
				key = $0; sub(/[ \t]*=.*/, "", key)
				buf = $0; sub(/^[^=]*=[ \t]*/, "", buf)
			} else {
				buf = buf "\n" $0
			}
			if (buf !~ /^\[/ || buf ~ /\]/) flush()
		}
		END { flush() }
	' "$root/.gitleaks.toml"
}

POLICY="$(read_policy)"
if [[ -z "$POLICY" ]]; then
	echo "test-secret-scan: .gitleaks.toml declares no allowlists this suite can read — it is gating nothing" >&2
	exit 1
fi

field() { printf '%s\n' "$POLICY" | awk -F'|' -v i="$1" -v k="$2" '$1 == i && $2 == k { print $3 }'; }
indices() { printf '%s\n' "$POLICY" | awk -F'|' '{ print $1 }' | sort -un; }

# A global allowlist — one with no `targetRules` — waves its files through for
# EVERY rule, which is the anti-pattern .gitleaks.toml opens by warning against.
# That makes it a CLOSED set, and a closed set has to be enumerated rather than
# derived: deriving it from the policy would mean the policy grants itself the
# exemption, and dropping `targetRules` from a scoped allowlist would widen it to
# a whole file with nothing here to notice. Measured: that omission alone hides a
# planted token of the allowlist's own rule.
SANCTIONED_GLOBAL="Fabricated fixtures in tests and Storybook stories"

for i in $(indices); do
	[[ -n "$(field "$i" rule)" ]] && continue
	desc="$(field "$i" desc)"
	if [[ "$desc" != "$SANCTIONED_GLOBAL" ]]; then
		fail "allowlist '$desc' names no targetRules, so it exempts its files from EVERY rule. Scope it with targetRules, or add it to SANCTIONED_GLOBAL here and argue for it in the pull request."
	fi
done

# The paths of every GLOBAL allowlist, as one alternation. A file matching one
# of these is exempt wholesale, so it can never carry a "still caught" plant —
# picking one as a subject would assert the opposite of what it proves.
global_paths=""
for i in $(indices); do
	[[ -n "$(field "$i" rule)" ]] && continue
	while IFS= read -r p; do
		[[ -z "$p" ]] && continue
		global_paths="${global_paths:+$global_paths|}$p"
	done < <(field "$i" path)
done

# subject_for <path regex> — a tracked file the regex covers and no global
# allowlist exempts. gitleaks matches `paths` with Go's RE2 against the path
# relative to the scan root; grep -E is the closest thing a POSIX shell has, so
# a pattern the two read differently shows up as "no tracked file matches",
# which is a loud failure and not a silent skip.
subject_for() {
	local matched
	matched="$(git ls-files | { grep -E -- "$1" || true; })"
	[[ -n "$global_paths" ]] && matched="$(printf '%s\n' "$matched" | { grep -E -v -- "$global_paths" || true; })"
	printf '%s\n' "$matched" | awk 'NF { print; exit }'
}

echo "test-secret-scan: planting a token the allowlists must not hide"

# --- the baseline the whole suite rests on ------------------------------------
# `expect` judges a scan of the WHOLE tree, so ANY finding anywhere makes it
# report "caught". If the committed tree does not scan clean, every "caught"
# assertion below passes without the plant having done anything, and the suite
# reads green while gating nothing.
if ! "$gitleaks" dir "$work" --config "$root/.gitleaks.toml" --redact --no-banner >/dev/null 2>&1; then
	echo "test-secret-scan: the committed tree does not scan clean, so a planted" >&2
	echo "  token cannot be told from what is already there — every 'caught' case" >&2
	echo "  below would pass for free. Run 'make secret-scan' and resolve that first." >&2
	exit 1
fi


# --- 0. the recipes themselves ------------------------------------------------
# A control file no allowlist covers. Every rule any allowlist targets must be
# plantable and caught HERE first: without this, "still caught" below could be
# reporting a plant that never tripped anything.
CONTROL="backend/internal/modules/people/person.go"
RULES="$(
	{
		printf '%s\n' "$FOREIGN_RULE"
		for i in $(indices); do field "$i" rule; done
	} | sort -u
)"
for rule in $RULES; do
	expect caught "$CONTROL" "$rule" "no allowlist covers this file, so every rule must fire here"
done

# --- 1. every scoped allowlist, against its own rule and a foreign one --------
# (b) is the own-rule plant: the line does not match the allowlist's regex, so
# the exemption must not reach it. (a) is the foreign-rule plant: the file must
# stay scanned for every rule the allowlist does not name.
for i in $(indices); do
	rules="$(field "$i" rule)"
	[[ -z "$rules" ]] && continue
	desc="$(field "$i" desc)"
	while IFS= read -r pattern; do
		[[ -z "$pattern" ]] && continue
		subject="$(subject_for "$pattern")"
		if [[ -z "$subject" ]]; then
			fail "allowlist '$desc' names a path no tracked file matches: $pattern — nothing gates it"
			continue
		fi
		for rule in $rules $FOREIGN_RULE; do
			expect caught "$subject" "$rule" \
				"the '$desc' exemption must cover the line it names, not the file"
		done
	done < <(field "$i" path)
done

# --- 2. the wholesale exemptions, asserted rather than assumed ----------------
# Fixtures are exempt in full, and deliberately so: a test asserting that a
# credentialed URL is refused has to contain one. Stated here so the trade-off
# is on the record instead of being something nobody noticed.
expect missed "backend/internal/shared/ports/websearch/sourcepolicy_test.go" "$FOREIGN_RULE" \
	"test fixtures are exempt by design"

if [[ "$failures" -ne 0 ]]; then
	echo "test-secret-scan: FAIL ($failures)" >&2
	exit 1
fi
echo "test-secret-scan: ok"
