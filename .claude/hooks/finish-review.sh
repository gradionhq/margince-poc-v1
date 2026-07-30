#!/usr/bin/env bash
# End-of-work review orchestration (Stop hook).
#
# Fires when the agent finishes a turn. If — and only if — THIS SESSION edited
# backend Go code, it drives the pre-push review flow:
#
#   1. `craft static` ALWAYS (the deterministic ADR-0045 gate, same diff-scoped
#      contract as .githooks/pre-push). Non-zero exit = BLOCKER finding → the
#      hook blocks the stop and asks the agent to fix, and loops until green.
#      This is cheap and deterministic, so it runs on every changed set.
#   2. The two review subagents run ONCE PER BRANCH, and only on a COMPLETE PR —
#      i.e. once the branch has an open pull request. Mid-work turns get the
#      craft gate and nothing else.
#   3. Anything after that round lets the stop through.
#
# WHY THE ROUND CAP IS KEYED ON THE BRANCH, NOT THE CHANGE: the review set is a
# content hash of the session-edited files, and applying a review finding changes
# those files. Keyed on the hash alone, every fix minted a fresh hash, reset the
# state to `craft`, and requested another pair of subagents — an unbounded loop
# for as long as the reviewers kept finding anything (observed: 12 rounds on one
# PR). The hash still drives the craft gate, so new code is always gated; the
# expensive judgment round is bounded by max_review_rounds per branch.
#
# SESSION-SCOPED (this is the whole point): the review set is the backend Go
# files THIS session touched, derived from the Edit/Write tool calls in the
# session transcript — NOT `git diff origin/main`. A parallel session's
# uncommitted work in the same tree is invisible here, so a read-only or
# frontend-only turn never gets dragged into reviewing code it did not write.
#
# Loop-safe on both axes: the content hash restarts the CRAFT gate on new code
# (that is wanted — new code must be gated), while max_review_rounds keeps the
# subagent pass from restarting with it. A per-set attempt cap prevents trapping
# the session if craft cannot be made green.
#
# Reads the Stop hook JSON on stdin; emits {"decision":"block","reason":...} to
# hold the stop, or exits 0 to allow it. Fails open (allows the stop) whenever
# the transcript is missing or yields no session-edited backend code.
set -euo pipefail

# --- read the Stop hook payload from stdin --------------------------------
payload="$(cat)"

# --- locate the repo (hook cwd is the project dir) --------------------------
root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [ -z "$root" ]; then exit 0; fi   # not a git repo → nothing to review

# Per-worktree state: --absolute-git-dir resolves to .git/ in the main
# checkout and .git/worktrees/<name>/ in a linked worktree, where $root/.git
# is a file, not a writable directory.
gitdir="$(git -C "$root" rev-parse --absolute-git-dir)"
state_file="$gitdir/margince-finish-review.state"
# Rounds live apart from the phase state, one line per branch — see below.
rounds_file="$gitdir/margince-finish-review.rounds"
max_craft_attempts=3
# How many subagent review rounds one branch gets, total. The reviewers are
# judgment-level and expensive; one pass over a finished PR is the deliverable,
# and a second pass over the fixes is what turns into a loop.
max_review_rounds=1

# --- the session transcript path (from the Stop payload) ------------------
transcript="$(printf '%s' "$payload" | python3 -c 'import json,sys
try:
    print(json.load(sys.stdin).get("transcript_path","") or "")
except Exception:
    print("")')"
if [ -z "$transcript" ] || [ ! -f "$transcript" ]; then exit 0; fi

# --- the backend Go files THIS session edited -----------------------------
# Parse the transcript for Edit/Write/MultiEdit/NotebookEdit tool calls and keep
# their targets that are non-generated backend Go files still present on disk.
# This is the exact "what did this turn/session change" set — a sibling session's
# tree residue never appears, because it has no tool_use here.
session_edits="$(python3 - "$transcript" "$root" <<'PY'
import json, os, sys

transcript, root = sys.argv[1], sys.argv[2]
root = os.path.realpath(root)
edit_tools = {"Edit", "Write", "MultiEdit", "NotebookEdit"}

targets = set()
with open(transcript, "r", errors="replace") as fh:
    for line in fh:
        line = line.strip()
        if not line:
            continue
        try:
            entry = json.loads(line)
        except Exception:
            continue
        message = entry.get("message") or {}
        content = message.get("content")
        if not isinstance(content, list):
            continue
        for block in content:
            if not isinstance(block, dict) or block.get("type") != "tool_use":
                continue
            if block.get("name") not in edit_tools:
                continue
            params = block.get("input") or {}
            path = params.get("file_path") or params.get("notebook_path")
            if path:
                targets.add(path)

rel = set()
for path in targets:
    absolute = os.path.realpath(path if os.path.isabs(path) else os.path.join(root, path))
    if not absolute.startswith(root + os.sep):
        continue
    relative = os.path.relpath(absolute, root)
    if not relative.startswith("backend/"):
        continue
    if not relative.endswith(".go") or relative.endswith("_gen.go"):
        continue
    if not os.path.exists(absolute):
        continue
    rel.add(relative)

for relative in sorted(rel):
    print(relative)
PY
)"
edited=()
while IFS= read -r f; do
	[ -n "$f" ] && edited+=("$f")
done <<< "$session_edits"
if [ "${#edited[@]}" -eq 0 ]; then exit 0; fi   # this session issued no backend edits → not our turn

# Keep only those with a real NET change still in the tree, measured against
# the merge-base with origin/main: committed-but-unmerged work stays in
# scope (the normal loop commits within the turn), an edit-then-revert nets
# to nothing and drops out, and a sibling session's files never enter
# because the set is transcript-derived.
base="$(git -C "$root" merge-base HEAD origin/main 2>/dev/null || git -C "$root" rev-parse HEAD)"
files=()
for f in "${edited[@]}"; do
	if ! git -C "$root" diff --quiet "$base" -- "$f" 2>/dev/null; then
		files+=("$f")   # changed vs the merge-base — committed or not
	elif [ -n "$(git -C "$root" ls-files --others --exclude-standard -- "$f")" ]; then
		files+=("$f")   # new untracked file this session wrote
	fi
done
if [ "${#files[@]}" -eq 0 ]; then exit 0; fi   # no net backend change from this session → nothing to review

# Identity of the change: content hash of exactly the session-edited files, so
# any further edit to them yields a fresh hash and restarts the review flow.
diff_hash="$({
	for f in "${files[@]}"; do
		printf '=== %s\n' "$f"
		cat "$root/$f" 2>/dev/null || true
	done
} | shasum -a 256 | cut -d' ' -f1)"

branch="$(git -C "$root" rev-parse --abbrev-ref HEAD 2>/dev/null || echo detached)"

# --- read prior state -----------------------------------------------------
# phase/attempts are per CHANGE: the craft gate must re-run on new code, so a
# single record is enough — it only has to survive to the next stop.
phase="craft"; attempts=0
if [ -f "$state_file" ]; then
	read -r saved_hash saved_phase saved_attempts < "$state_file" || true
	if [ "${saved_hash:-}" = "$diff_hash" ]; then
		phase="${saved_phase:-craft}"; attempts="${saved_attempts:-0}"
	fi
fi

# rounds is per BRANCH, in its own file with one line per branch. It cannot share
# the single-record state: working on a sibling branch and coming back would
# overwrite the count and hand this branch a second review.
# Guarded on the file existing: under `set -euo pipefail` a failing awk in a
# command substitution kills the hook outright, and 2>/dev/null hides the message
# without changing the exit status.
rounds=0
if [ -f "$rounds_file" ]; then
	rounds="$({ awk -F'\t' -v b="$branch" '$1 == b { print $2 }' "$rounds_file" || true; } | tail -1)"
	rounds="${rounds:-0}"
fi

# Already fully reviewed this exact set → let the stop through.
if [ "$phase" = "done" ]; then exit 0; fi

emit_block() {   # $1 = reason text → hold the stop and feed the reason back
	printf '{"decision":"block","reason":%s}\n' "$(printf '%s' "$1" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')"
}

save_state() { printf '%s %s %s\n' "$diff_hash" "$1" "$2" > "$state_file"; }

# record_round persists this branch's count, rewriting only its own line.
record_round() {
	tmp="$rounds_file.tmp.$$"
	{ if [ -f "$rounds_file" ]; then awk -F'\t' -v b="$branch" '$1 != b' "$rounds_file" || true; fi
	  printf '%s\t%s\n' "$branch" "$1"; } > "$tmp" && mv "$tmp" "$rounds_file"
}

# reviewable answers whether this branch has an OPEN PR — the "complete PR"
# condition. Fails OPEN (no review requested) when gh is unavailable or there is
# no PR yet, so a missing credential never traps the session in a gate it cannot
# satisfy. No PR selector on purpose: gh infers the PR for the current branch,
# which is exactly the question — passing --repo would suppress that inference.
#
# The state is checked, not just the existence: `gh pr view` also resolves a
# CLOSED or MERGED PR for the branch, so existence alone would keep handing out
# review rounds on a branch whose PR has already landed.
reviewable() {
	command -v gh >/dev/null 2>&1 || return 1
	state="$( (cd "$root" && gh pr view --json state --jq '.state' 2>/dev/null) || true )"
	[ "$state" = "OPEN" ]
}

# request_review holds the stop and asks for the subagent round, but only when
# this branch is entitled to one. BOTH gates live here so that no caller can skip
# them: the craft-exhausted path below used to request a round with neither the
# round cap nor the open-PR condition applied.
# $1 = reason text. Returns non-zero when it declined, having marked this set done.
request_review() {
	if [ "$rounds" -ge "$max_review_rounds" ] || ! reviewable; then
		save_state "done" 0
		return 1
	fi
	record_round "$((rounds + 1))"
	save_state "agents_requested" 0
	emit_block "$1"
	return 0
}

# --- phase 1: the deterministic craft gate --------------------------------
if [ "$phase" = "craft" ]; then
	args=(); for f in "${files[@]}"; do [ -n "$f" ] && args+=("$root/$f"); done
	if craft_out="$(go run -C "$root/cli/craft" . static "${args[@]}" 2>&1)"; then
		request_review "This branch has an open PR and craft static is green, so the one end-of-work review round for it runs now — scoped to the ${#args[@]} backend file(s) THIS session changed.

Step 1 — craft static (the deterministic ADR-0045 gate): PASSED.

Step 2 — launch the two review subagents IN PARALLEL (one message, two Agent tool calls):
  • subagent_type \"craft-reviewer\" — craftsmanship double-check against the CLAUDE.md craftsmanship rules
  • subagent_type \"security-redteam\" — adversarial security / tenant-isolation review of the diff

Both review the backend files this session changed and report findings; they do not edit. When they return, apply every confirmed finding, then finish.

This is the ONLY subagent round this branch gets — craft static still re-runs on whatever your fixes touch, but the reviewers will not be requested again. So triage their findings yourself: apply what is a defect, and record what is a judgment call or a follow-up rather than reopening the design." || true
		exit 0
	else
		attempts=$((attempts + 1))
		if [ "$attempts" -gt "$max_craft_attempts" ]; then
			# Do not trap the session on a craft gate it cannot satisfy: stop
			# gating, but warn loudly. A stuck craft gate is not a reason to hand
			# out a second review round, or one on a branch with no PR — so this
			# goes through request_review like the green path, and falls back to a
			# plain warning when it declines.
			craft_warning="craft static still reports BLOCKER findings after ${max_craft_attempts} attempts on this session's backend edits — NOT auto-cleared. Address them, or waive a genuine false positive in-source: //craft:ignore <check> <reason>. Do not push until craft is green.

--- craft static output ---
$craft_out"
			request_review "$craft_warning

The end-of-work review round runs now anyway; launch craft-reviewer and security-redteam in parallel as above." || emit_block "$craft_warning"
			exit 0
		fi
		save_state "craft" "$attempts"
		emit_block "End-of-work gate: craft static (the deterministic ADR-0045 craftsmanship gate) found BLOCKER findings on the backend files this session changed. Fix them before finishing — new/touched code must be clean. A genuine false positive is waived in-source with a reason: //craft:ignore <check> <reason>.

--- craft static output ---
$craft_out"
		exit 0
	fi
fi

# --- phase 2: agents were requested; the agent stopped again on the same set.
# Treat the review as complete for this set and let the stop through.
if [ "$phase" = "agents_requested" ]; then
	save_state "done" 0
	exit 0
fi

exit 0
