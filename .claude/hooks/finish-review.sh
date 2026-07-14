#!/usr/bin/env bash
# End-of-work review orchestration (Stop hook).
#
# Fires when the agent finishes a turn. If — and only if — THIS SESSION edited
# backend Go code, it drives the pre-push review flow:
#
#   1. `craft static` FIRST (the deterministic ADR-0045 gate, same diff-scoped
#      contract as .githooks/pre-push). Non-zero exit = BLOCKER finding → the
#      hook blocks the stop and asks the agent to fix, and loops until green.
#   2. Once craft is green, the hook blocks once more and instructs the agent to
#      launch the two review subagents (craft-reviewer + security-redteam) in
#      parallel and act on their findings.
#   3. When the agent stops again on the same (now-reviewed) set, the hook lets
#      the stop through.
#
# SESSION-SCOPED (this is the whole point): the review set is the backend Go
# files THIS session touched, derived from the Edit/Write tool calls in the
# session transcript — NOT `git diff origin/main`. A parallel session's
# uncommitted work in the same tree is invisible here, so a read-only or
# frontend-only turn never gets dragged into reviewing code it did not write.
#
# Loop-safe: state is keyed on a hash of the session-edited files' contents, so a
# fix that changes them restarts the flow (craft re-runs on the new code), and an
# unchanged set never re-triggers. A per-set attempt cap prevents trapping the
# session if craft can't be made green.
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

state_file="$root/.git/margince-finish-review.state"
max_craft_attempts=3

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

# Keep only those with a real NET change still in the tree: an uncommitted
# modification vs HEAD, or a new untracked file. An edit-then-revert nets to
# nothing (clean vs HEAD) and drops out — the review must not fire on a file a
# sibling committed or that this session put back exactly as it found it.
files=()
for f in "${edited[@]}"; do
	if ! git -C "$root" diff --quiet HEAD -- "$f" 2>/dev/null; then
		files+=("$f")   # tracked, has uncommitted changes
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

# --- read prior state for this hash ---------------------------------------
phase="craft"; attempts=0
if [ -f "$state_file" ]; then
	read -r saved_hash saved_phase saved_attempts < "$state_file" || true
	if [ "${saved_hash:-}" = "$diff_hash" ]; then
		phase="${saved_phase:-craft}"; attempts="${saved_attempts:-0}"
	fi
fi

# Already fully reviewed this exact set → let the stop through.
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
		emit_block "Work looks finished and this session edited backend Go code, so the end-of-work review runs now — scoped to the ${#args[@]} file(s) THIS session changed.

Step 1 — craft static (the deterministic ADR-0045 gate): PASSED.

Step 2 — launch the two review subagents IN PARALLEL (one message, two Agent tool calls):
  • subagent_type \"craft-reviewer\" — craftsmanship double-check against the CLAUDE.md craftsmanship rules
  • subagent_type \"security-redteam\" — adversarial security / tenant-isolation review of the diff

Both review the backend files this session changed and report findings; they do not edit. When they return, apply every confirmed finding, then finish. (If your fixes change those files, craft static and this review will re-run on the new code — that is intended.)"
		exit 0
	else
		attempts=$((attempts + 1))
		if [ "$attempts" -gt "$max_craft_attempts" ]; then
			# Do not trap the session: warn loudly, advance to agents anyway.
			save_state "agents_requested" 0
			emit_block "craft static still reports BLOCKER findings after ${max_craft_attempts} attempts on this session's backend edits — NOT auto-cleared. Address them (or waive a genuine false positive in-source: //craft:ignore <check> <reason>). Proceeding to the review subagents; do not push until craft is green.

--- craft static output ---
$craft_out"
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
