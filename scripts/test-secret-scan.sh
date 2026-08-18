#!/usr/bin/env bash
# test-secret-scan.sh — prove the secret gate still catches.
#
# A scan policy can only fail by being too permissive, and an over-broad
# allowlist is invisible: it reports the same "no leaks found" as a clean tree.
# So every allowlist in .gitleaks.toml is re-checked here — plant a credential
# in a file it covers, and the scan must still fail.
#
# TWO properties, and the second is the one a single plant cannot reach:
#
#   (a) an allowlist does not hide ANOTHER rule's finding. A global
#       `[[allowlists]]` carrying `paths` makes gitleaks skip the whole file
#       before it reads a line, so `condition` never narrows it and the
#       exemption widens to everything in the file. `targetRules` is what keeps
#       the file scanned. Both scoped entries were written the wrong way first.
#
#   (b) an allowlist does not hide ITS OWN rule's finding on a line its regex
#       does not match. Nothing about (a) implies this, and it is the property
#       "the file stays scanned for everything else" is read as promising.
#       Measured: dropping `targetRules`, `regexes`, or `condition = "AND"`
#       each widens a scoped exemption to the whole file for its own rule.
#
# The plants are DERIVED from the policy rather than listed. A hand-kept list
# goes stale the first time somebody adds an allowlist, and an allowlist nobody
# planted against is exactly the one that is over-broad.
#
# THE SHAPE GATE IS THE LOAD-BEARING PART. Deriving from a file this script
# hand-parses is only safe if anything it cannot fully read is REFUSED, so the
# policy is held to a narrow committed form: no indentation, no tables besides
# [extend] and [[allowlists]], one quoting style per key, and a `paths` on every
# entry. Each rejected shape below was demonstrated to hide a real credential
# while this suite printed ok, so every one of them is a fixed bug rather than
# fastidiousness. A coverage ledger backs the gate up: an allowlist that reaches
# the end of the run with no plant against it fails, whatever went wrong.
#
# The tokens are generated per run rather than written down — a literal here
# would be a credential-shaped string in the repo — and each is PROVED to trip
# its rule before it is trusted: a random high-entropy string lands under the
# generic-api-key floor often enough to redden roughly one run in fifty.
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
probe="$(mktemp -d)"
trap 'rm -rf "$work" "$backups" "$probe"' EXIT
git archive HEAD | tar -x -C "$work"

failures=0
fail() {
	printf '  FAIL  %s\n' "$1"
	failures=$((failures + 1))
}

# The separator between a parsed field and its value. A unit separator, not a
# pipe: `|` is ordinary inside a path regex — '''(^|/)vendor/''' — and a
# delimiter that can occur in the data truncates it into a different, usually
# invalid, pattern.
SEP=$(printf '\037')

# --- 0. the policy is in the form this script can actually read ---------------
# Refuse first, parse second.
shape_gate() {
	local policy="$root/.gitleaks.toml" line n bad=0
	note() {
		fail "$1"
		bad=1
	}

	# TOML tolerates leading whitespace on a table header and on a key; the
	# parser below anchors at column 0. An indented `[[allowlists]]` was
	# demonstrated to exempt a whole directory while this suite printed ok.
	if grep -nE '^[ \t]+(\[|(description|targetRules|paths|regexes|condition|regexTarget|stopwords)[ \t]*=)' "$policy"; then
		note "the lines above are indented. Every table header and key in .gitleaks.toml sits at column 0 — an indented one is valid TOML this suite cannot see."
	fi

	# Only two table kinds. A `[[rules]]` block carries a nested allowlist of
	# its own, which is an exemption channel nothing here reads.
	while IFS= read -r line; do
		case "$line" in
		"[extend]" | "[[allowlists]]") ;;
		*) note "unsupported table '$line' in .gitleaks.toml. This suite gates [[allowlists]] only; a [[rules]] block carries exemptions of its own that nothing here would plant against." ;;
		esac
	done < <(grep -E '^\[' "$policy")

	# [extend] turns rules ON. `disabledRules` turns whole detectors OFF, which
	# is an exemption with no allowlist to plant against.
	n="$(grep -cE '^useDefault[ \t]*=[ \t]*true$' "$policy" || true)"
	if [[ "$n" -ne 1 ]]; then
		note ".gitleaks.toml must carry exactly one 'useDefault = true'; found $n."
	fi
	if grep -nE '^(disabledRules|stopwords)[ \t]*=' "$policy"; then
		note "the keys above disable detection outside any allowlist, so nothing here plants against them."
	fi

	# One quoting style per key, because the array reader splits on ONE
	# delimiter: a mixed-quote array silently drops the entries in the other
	# style, and a dropped path owes no plant.
	if grep -nE "^(description|targetRules)[ \t]*=.*'''" "$policy"; then
		note "description and targetRules use \"basic strings\"; the lines above mix in ''' literals."
	fi
	if grep -nE '^(paths|regexes)[ \t]*=.*"' "$policy"; then
		note "paths and regexes use '''literal strings'''; the lines above mix in \" basic strings."
	fi

	# gitleaks honours a repo-root .gitleaksignore: a fingerprint exemption list
	# outside this policy entirely, and one the scan of the export would read.
	if git ls-tree -r --name-only HEAD | grep -qx '\.gitleaksignore'; then
		note "a tracked .gitleaksignore exempts findings outside .gitleaks.toml, and nothing here reads it."
	fi

	return "$bad"
}

if ! shape_gate; then
	echo "" >&2
	echo "test-secret-scan: .gitleaks.toml is in a form this suite cannot fully read," >&2
	echo "  so it cannot promise every allowlist was planted against. Fix the shape, or" >&2
	echo "  teach scripts/test-secret-scan.sh the new one — do not leave it unread." >&2
	exit 1
fi

# --- 1. the policy, read rather than restated --------------------------------
# One record per field: <index><SEP><kind><SEP><value>, kind in desc|rule|path|
# regex. The index is MONOTONIC across the whole file and never resets, so no
# two allowlists can merge into one record set.
read_policy() {
	awk -v sep="$SEP" '
		function flush(   n, i, parts, q, kind) {
			if (key == "") return
			q = (key == "paths" || key == "regexes") ? "\x27\x27\x27" : "\""
			kind = (key == "targetRules") ? "rule" : \
			       (key == "paths") ? "path" : \
			       (key == "regexes") ? "regex" : "desc"
			n = split(buf, parts, q)
			for (i = 2; i <= n; i += 2) print idx sep kind sep parts[i]
			key = ""; buf = ""
		}
		/^\[\[allowlists\]\]/ { flush(); idx++; inside = 1; next }
		/^\[/                 { flush(); inside = 0; next }
		!inside               { next }
		{
			if (key == "") {
				if ($0 !~ /^(description|targetRules|paths|regexes)[ \t]*=/) next
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

field() { printf '%s\n' "$POLICY" | awk -F"$SEP" -v i="$1" -v k="$2" '$1 == i && $2 == k { print $3 }'; }
indices() { printf '%s\n' "$POLICY" | awk -F"$SEP" '{ print $1 }' | sort -un; }

# Every allowlist must end the run having been planted against. This ledger is
# the backstop for a shape the gate above did not anticipate: whatever the
# reason an entry escaped the loops, it is reported rather than skipped.
planted_against=""
mark_planted() { planted_against="${planted_against} $1"; }

# The first line of a list, without an early-exiting consumer: an `awk … exit`
# closes the pipe and SIGPIPEs its producer, which `pipefail` then reports as
# 141 — the same trap the bounded `od` read below avoids.
first_of() { awk 'NF && !done { print; done = 1 }'; }

# --- 2. the plants ------------------------------------------------------------
# distinct_token <n> — n DISTINCT characters from a 62-symbol alphabet, so the
# value's Shannon entropy is log2(n) by construction rather than by luck. `od`
# is bounded by -N, so nothing downstream can SIGPIPE the producer.
distinct_token() {
	local want="$1" out
	out="$(od -An -tu1 -N512 /dev/urandom | awk -v want="$want" '
		BEGIN {
			ALPHA = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
			n = length(ALPHA)
			for (i = 1; i <= n; i++) A[i] = substr(ALPHA, i, 1)
		}
		{ for (i = 1; i <= NF; i++) { ch = A[($i % n) + 1]; if (!(ch in seen)) { seen[ch] = 1; out = out ch } } }
		END { print substr(out, 1, want) }')"
	[[ "${#out}" -eq "$want" ]] || return 1
	printf '%s' "$out"
}

# candidate_line <rule-id> — one line shaped like that rule's secret, or exit 1
# for a rule this suite does not know how to plant for.
candidate_line() {
	case "$1" in
	generic-api-key) printf 'const plantedApiKey = "%s"\n' "$(distinct_token 40)" ;;
	linkedin-client-id) printf 'const planted_linkedin_client_id = "%s"\n' "$(distinct_token 14)" ;;
	github-pat) printf 'const plantedToken = "ghp_%s"\n' "$(distinct_token 36)" ;;
	*) return 1 ;;
	esac
}

# plant_line <rule-id> — a line PROVED to trip that rule against the default
# rule set, alone in its own directory. A candidate is only SHAPED like the
# secret; the entropy floor and the rule's stopwords decide the rest, and an
# assertion that a plant is "still caught" is worthless if the plant never
# tripped anything. Bounded retries, then a hard failure — never a skip.
plant_line() {
	local rule="$1" line attempt
	printf '[extend]\nuseDefault = true\n' >"$probe/config.toml"
	for attempt in 1 2 3 4 5 6 7 8; do
		line="$(candidate_line "$rule")" || return 1
		rm -rf "${probe:?}/tree"
		mkdir -p "$probe/tree"
		printf '%s' "$line" >"$probe/tree/candidate.go"
		if ! "$gitleaks" dir "$probe/tree" --config "$probe/config.toml" --redact --no-banner >/dev/null 2>&1; then
			printf '%s' "$line"
			return 0
		fi
	done
	return 2
}

# The rule every allowlist here is foreign to, so it carries property (a) for
# each of them. Asserted below rather than assumed: if an allowlist ever targets
# it, the loop would plant the same rule twice and (a) would silently stop being
# tested for that entry.
FOREIGN_RULE="github-pat"

# expect <caught|missed> <path> <rule-id> <why> — plant a token of <rule-id> in
# the export copy of <path>, scan, then restore the file so each case is judged
# alone.
expect() {
	local want="$1" target="$2" rule="$3" why="$4" file="$work/$2" got line status=0
	if [[ ! -f "$file" ]]; then
		fail "$target does not exist in the committed tree"
		return
	fi
	line="$(plant_line "$rule")" || status=$?
	if [[ "$status" -eq 1 ]]; then
		fail "no plant recipe for rule '$rule' — $target is ungated. Add one to candidate_line()."
		return
	elif [[ "$status" -ne 0 ]]; then
		fail "could not generate a token that trips '$rule' in 8 attempts — $target is ungated, and the recipe in candidate_line() no longer matches the pinned rule set."
		return
	fi
	# The backup lives OUTSIDE the scanned tree. Left beside the original it
	# would be scanned too, under a name no allowlist path matches — its own
	# findings would then turn every case green and the suite would pass
	# without testing anything.
	cp "$file" "$backups/orig"
	# The trailing newline is load-bearing. An unterminated final line is read
	# differently under `regexTarget = "line"`, and a plant landing as one is
	# reported missed in any file that carries an exempted line elsewhere — an
	# accusation against the policy caused entirely by the plant's shape.
	printf '\n%s\n' "$line" >>"$file"
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

echo "test-secret-scan: planting a token the allowlists must not hide"

# --- 3. the baseline the whole suite rests on ---------------------------------
# `expect` judges a scan of the WHOLE tree, so ANY finding anywhere makes it
# report "caught". If the committed tree does not scan clean, every "caught"
# case below passes without the plant having done anything, and the suite reads
# green while gating nothing.
if ! "$gitleaks" dir "$work" --config "$root/.gitleaks.toml" --redact --no-banner >/dev/null 2>&1; then
	echo "test-secret-scan: the committed tree does not scan clean, so a planted" >&2
	echo "  token cannot be told from what is already there — every 'caught' case" >&2
	echo "  below would pass for free. Run 'make secret-scan' and resolve that first." >&2
	exit 1
fi

# --- 4. the sanctioned wholesale exemption, as a closed set -------------------
# A global allowlist — no `targetRules` — waves its files through for EVERY
# rule, the anti-pattern .gitleaks.toml opens by warning against. That makes it
# a CLOSED set, enumerated rather than derived: deriving it from the policy
# would let the policy grant itself the exemption. It is keyed on the paths as
# well as the description, because widening the sanctioned entry's own `paths`
# in place, and adding a second entry that reuses its description, each hide a
# real credential otherwise. LC_ALL=C so the comparison does not depend on the
# caller's collation.
SANCTIONED_GLOBAL_DESC="Fabricated fixtures in tests and Storybook stories"
SANCTIONED_GLOBAL_PATHS='\.stories\.tsx$
\.test\.tsx?$
_test\.go$'

for i in $(indices); do
	[[ -n "$(field "$i" rule)" ]] && continue
	desc="$(field "$i" desc)"
	paths="$(field "$i" path | LC_ALL=C sort)"
	if [[ "$desc" != "$SANCTIONED_GLOBAL_DESC" ]]; then
		fail "allowlist '$desc' names no targetRules, so it exempts its files from EVERY rule. Scope it with targetRules, or sanction it here and argue for it in the pull request."
		mark_planted "$i"
	elif [[ "$paths" != "$SANCTIONED_GLOBAL_PATHS" ]]; then
		fail "the sanctioned wholesale exemption's paths have moved. It may exempt fixtures and nothing else; sanctioning is keyed on the paths, not on the description alone."
		mark_planted "$i"
	fi
done

# A description may not be reused: the sanctioning above is a string comparison,
# and a second entry wearing the sanctioned string would inherit its blessing.
DUPES="$(printf '%s\n' "$POLICY" | awk -F"$SEP" '$2 == "desc" { print $3 }' | LC_ALL=C sort | uniq -d)"
if [[ -n "$DUPES" ]]; then
	fail "two allowlists share a description, and the wholesale-exemption check keys on it: $DUPES"
fi

# --- 5. the plant recipes themselves ------------------------------------------
# A control file no allowlist covers. Every rule any allowlist targets must be
# plantable and caught HERE first: without it, "still caught" below could be
# reporting a plant that never tripped anything.
CONTROL="backend/internal/modules/people/person.go"
TARGETED="$(for i in $(indices); do field "$i" rule; done | LC_ALL=C sort -u)"
if printf '%s\n' "$TARGETED" | grep -qx "$FOREIGN_RULE"; then
	fail "an allowlist now targets $FOREIGN_RULE, which this suite uses as the rule every allowlist is foreign to. Property (a) would stop being tested for it. Pick a different FOREIGN_RULE."
fi
for rule in $(printf '%s\n%s\n' "$FOREIGN_RULE" "$TARGETED" | LC_ALL=C sort -u); do
	[[ -z "$rule" ]] && continue
	expect caught "$CONTROL" "$rule" "no allowlist covers this file, so every rule must fire here"
done

# --- 6. every scoped allowlist, against its own rule and a foreign one --------
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

# The files a pattern covers, minus anything exempt wholesale, read from the
# tree the scan actually reads rather than from the index — a file staged but
# not committed is absent from the export and would fail as "does not exist".
#
# gitleaks matches `paths` with Go's RE2 against the ABSOLUTE path, while this
# matches `grep -E` against a repo-relative one, so a `^`-anchored pattern is
# inert there and live here. That disagreement cannot pass silently: the
# subjects below include a file whose CONTENT the allowlist regex matches, and
# an inert allowlist and a live one differ there.
subjects_for() {
	local matched
	matched="$(git ls-tree -r --name-only HEAD | { grep -E -- "$1" || true; })"
	if [[ -n "$global_paths" ]]; then
		matched="$(printf '%s\n' "$matched" | { grep -E -v -- "$global_paths" || true; })"
	fi
	printf '%s\n' "$matched"
}

for i in $(indices); do
	rules="$(field "$i" rule)"
	[[ -z "$rules" ]] && continue
	desc="$(field "$i" desc)"
	patterns="$(field "$i" path)"
	if [[ -z "$patterns" ]]; then
		fail "allowlist '$desc' names no paths, so it applies repo-wide for its rules — strictly broader than any path-scoped entry, and nothing here can plant against it. Give it paths."
		mark_planted "$i"
		continue
	fi
	regexes="$(field "$i" regex)"
	while IFS= read -r pattern; do
		[[ -z "$pattern" ]] && continue
		covered="$(subjects_for "$pattern")"
		if [[ -z "$(printf '%s' "$covered" | tr -d '[:space:]')" ]]; then
			fail "allowlist '$desc' names a path no committed file matches: $pattern — nothing gates it"
			mark_planted "$i"
			continue
		fi
		# Two subjects, because they exercise different halves. The first file
		# the pattern covers proves the path scope; a file whose CONTENT the
		# allowlist's own regex matches is where the exemption is actually
		# live, and it is the one where an over-broad entry hides the plant.
		subject="$(printf '%s\n' "$covered" | first_of)"
		live=""
		while IFS= read -r rx; do
			[[ -z "$rx" ]] && continue
			while IFS= read -r f; do
				[[ -n "$f" ]] || continue
				[[ -f "$work/$f" ]] || continue
				if grep -qE -- "$rx" "$work/$f"; then
					live="$f"
					break
				fi
			done < <(printf '%s\n' "$covered")
			[[ -n "$live" ]] && break
		done < <(printf '%s\n' "$regexes")
		for target in $subject $live; do
			for rule in $rules $FOREIGN_RULE; do
				expect caught "$target" "$rule" \
					"the '$desc' exemption must cover the line it names, not the file"
			done
		done
		mark_planted "$i"
	done < <(printf '%s\n' "$patterns")
done

# --- 7. the wholesale exemptions, asserted rather than assumed ----------------
# Fixtures are exempt in full, and deliberately so: a test asserting that a
# credentialed URL is refused has to contain one. Asserted rather than assumed,
# so the trade-off is on the record instead of being something nobody noticed —
# and derived from the entry itself, so the day its paths change the assertion
# follows them instead of naming a file that is no longer the example.
for i in $(indices); do
	[[ -n "$(field "$i" rule)" ]] && continue
	desc="$(field "$i" desc)"
	while IFS= read -r pattern; do
		[[ -z "$pattern" ]] && continue
		exempt="$(git ls-tree -r --name-only HEAD | { grep -E -- "$pattern" || true; } | first_of)"
		if [[ -z "$exempt" ]]; then
			fail "wholesale exemption '$desc' names a path no committed file matches: $pattern"
			mark_planted "$i"
			continue
		fi
		expect missed "$exempt" "$FOREIGN_RULE" "'$desc' exempts this path in full, by design"
		mark_planted "$i"
	done < <(field "$i" path)
done

# --- 8. the ledger ------------------------------------------------------------
for i in $(indices); do
	case " $planted_against " in
	*" $i "*) ;;
	*) fail "allowlist $i ('$(field "$i" desc)') finished the run with no plant against it — this suite cannot say whether it is over-broad." ;;
	esac
done

if [[ "$failures" -ne 0 ]]; then
	echo "test-secret-scan: FAIL ($failures)" >&2
	exit 1
fi
echo "test-secret-scan: ok"
