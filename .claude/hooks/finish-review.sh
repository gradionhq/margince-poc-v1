#!/usr/bin/env bash
# End-of-work review orchestration (Stop hook).
#
# Fires when the agent finishes a turn. If — and only if — there is a real
# unpushed backend change vs origin/main, it drives the pre-push review flow:
#
#   1. `craft static` FIRST (the deterministic ADR-0045 gate, same diff-scoped
#      contract as .githooks/pre-push). Non-zero exit = BLOCKER finding → the
#      hook blocks the stop and asks the agent to fix, and loops until green.
#   2. Once craft is green, the hook blocks once more and instructs the agent to
#      launch the two review subagents (craft-reviewer + security-redteam) in
#      parallel and act on their findings.
#   3. When the agent stops again on the same (now-reviewed) diff, the hook lets
#      the stop through.
#
# Loop-safe: state is keyed on a hash of the unpushed backend diff, so a fix that
# changes the diff restarts the flow (craft re-runs on the new code), and an
# unchanged diff never re-triggers. A per-diff attempt cap prevents trapping the
# session if craft can't be made green.
#
# Reads the Stop hook JSON on stdin; emits {"decision":"block","reason":...} to
# hold the stop, or exits 0 to allow it.
set -euo pipefail

# --- locate the repo (hook cwd is the project dir) --------------------------
root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [ -z "$root" ]; then exit 0; fi   # not a git repo → nothing to review

state_file="$root/.git/margince-finish-review.state"
max_craft_attempts=3

# --- the unpushed backend diff --------------------------------------------
base="$(git -C "$root" merge-base HEAD origin/main 2>/dev/null || true)"
if [ -z "$base" ]; then exit 0; fi   # fresh repo / no remote base → skip

# The changed backend Go files this push would carry: tracked modifications vs
# base (committed + uncommitted, still present) PLUS new untracked files (a fresh
# module file is exactly what we want reviewed). Generated code excluded. (No
# `mapfile`: it is absent from the bash 3.2 that ships on macOS.)
files=()
while IFS= read -r f; do
	[ -n "$f" ] && files+=("$f")
done < <({
	git -C "$root" diff --name-only --diff-filter=d "$base" -- backend
	git -C "$root" ls-files --others --exclude-standard -- backend
} | grep -E '\.go$' | grep -v '_gen\.go$' | sort -u || true)
if [ "${#files[@]}" -eq 0 ]; then exit 0; fi   # no backend code changed → not a code turn

# Identity of the current change: content hash of the tracked diff plus each
# untracked file's contents, so any edit — tracked or not — yields a fresh hash.
diff_hash="$({
	git -C "$root" diff "$base" -- backend
	git -C "$root" ls-files --others --exclude-standard -- backend | while IFS= read -r u; do
		printf '=== %s\n' "$u"; cat "$root/$u"
	done
} | shasum -a 256 | cut -d' ' -f1)"

# --- read prior state for this hash ---------------------------------------
phase="craft"; attempts=0
if [ -f "$state_file" ]; then
	read -r saved_hash saved_phase saved_attempts < "$state_file" || true
	if [ "${saved_hash:-}" = "$diff_hash" ]; then
		phase="${saved_phase:-craft}"; attempts="${saved_attempts:-0}"
	fi
fi

# Already fully reviewed this exact diff → let the stop through.
if [ "$phase" = "done" ]; then exit 0; fi

emit_block() {   # $1 = reason text → hold the stop and feed the reason back
	printf '{"decision":"block","reason":%s}\n' "$(printf '%s' "$1" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')"
}

save_state() { printf '%s %s %s\n' "$diff_hash" "$1" "$2" > "$state_file"; }

# --- phase 1: the deterministic craft gate --------------------------------
if [ "$phase" = "craft" ]; then
	args=(); for f in "${files[@]}"; do [ -n "$f" ] && args+=("$root/$f"); done
	if craft_out="$(go run -C "$root/cli/craft" . static "${args[@]}" 2>&1)"; then
		# green → advance to the agent phase
		save_state "agents_requested" 0
		emit_block "Work looks finished and there is an unpushed backend diff, so the end-of-work review runs now.

Step 1 — craft static (the deterministic ADR-0045 gate): PASSED on the ${#args[@]} changed backend file(s).

Step 2 — launch the two review subagents IN PARALLEL (one message, two Agent tool calls):
  • subagent_type \"craft-reviewer\" — craftsmanship double-check against the dossier at /Users/lars/develop/margince/specs/research/craftsmanship-loved-and-anti-patterns.md
  • subagent_type \"security-redteam\" — adversarial security / tenant-isolation review of the diff

Both review the unpushed backend diff vs origin/main and report findings; they do not edit. When they return, apply every confirmed finding, then finish. (If your fixes change the diff, craft static and this review will re-run on the new code — that is intended.)"
		exit 0
	else
		attempts=$((attempts + 1))
		if [ "$attempts" -gt "$max_craft_attempts" ]; then
			# Do not trap the session: warn loudly, advance to agents anyway.
			save_state "agents_requested" 0
			emit_block "craft static still reports BLOCKER findings after ${max_craft_attempts} attempts on this diff — NOT auto-cleared. Address them (or waive a genuine false positive in-source: //craft:ignore <check> <reason>). Proceeding to the review subagents; do not push until craft is green.

--- craft static output ---
$craft_out"
			exit 0
		fi
		save_state "craft" "$attempts"
		emit_block "End-of-work gate: craft static (the deterministic ADR-0045 craftsmanship gate) found BLOCKER findings on the changed backend files. Fix them before finishing — new/touched code must be clean. A genuine false positive is waived in-source with a reason: //craft:ignore <check> <reason>.

--- craft static output ---
$craft_out"
		exit 0
	fi
fi

# --- phase 2: agents were requested; the agent stopped again on the same diff.
# Treat the review as complete for this diff and let the stop through.
if [ "$phase" = "agents_requested" ]; then
	save_state "done" 0
	exit 0
fi

exit 0
