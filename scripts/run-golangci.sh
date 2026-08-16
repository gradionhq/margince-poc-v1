#!/usr/bin/env bash
# run-golangci.sh — run golangci-lint, and tell a finding in THIS checkout apart
# from one its cache remembers from another.
#
# golangci-lint's analysis cache is per MACHINE (~/Library/Caches/golangci-lint,
# ~/.cache/golangci-lint on Linux), shared by every checkout on it, and keyed by
# file CONTENT rather than by path. This repo is worked in several git worktrees
# at once by design, so the same unchanged file exists at several absolute paths
# with ONE cache entry between them — and that entry keeps the path of whichever
# worktree filled it first. A run in a second worktree gets a cache hit and
# re-reports the issues against the first worktree's path.
#
# That is not cosmetic, which is the part that costs the time. Two of golangci's
# processors decide whether an issue is REPORTED by reading its path:
#
#   - the nolint processor opens the file at that path to find the `//nolint:`
#     directives that waive it. A path in another worktree — deleted, or simply
#     not this one — is a file it cannot associate with the run, so every
#     in-source waiver stops applying and the findings under it come back.
#   - `.golangci.yml`'s exclusion rules match the path as reported, anchored to
#     the config file's own directory (`^tools/`, `^\.\./cli/craft/`). A foreign
#     path matches neither, so the path-scoped exemptions stop applying too.
#
# The run then prints findings this tree has waived, in files it does not
# contain, under a header naming modules it does contain — on a branch that may
# not have touched Go at all. Every part of that reads as a real failure except
# the `../../` on the front of the paths, which is the easiest thing on the
# screen to skim past. Three times in one session (issue #1378) the first read
# was "my branch broke these modules", and the remedy printed underneath — fix
# the finding — cannot be followed, because the file is somewhere else.
#
# WHAT THIS CATCHES: an issue reported against a file outside this checkout. It
# says what that is and prints the one command that clears it, instead of
# leaving the findings to read as the named module's own. Exit code 40 marks the
# case so a caller can tell it from real findings (exit 1).
#
# Note what the finding IS, since "false positive" is the wrong word for it: the
# cache hit means the content is byte-identical, so the finding is true ABOUT THE
# CODE. What is wrong is the path, and with the path go the waivers that were
# written for it. Clearing the cache is therefore the whole fix — the findings do
# not come back with local paths, they go away, because locally they are waived.
#
# WHAT THIS DOES NOT CATCH, deliberately: nothing about the cache itself. It
# neither clears it nor partitions it per worktree. Clearing is destructive to a
# resource other sessions are using concurrently and belongs to the human who
# knows what else is running; partitioning (GOLANGCI_LINT_CACHE per checkout)
# costs every worktree a cold analysis cache forever, to buy back a message this
# prints for free. This targets the misreading, which is what the time went on.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

# Prefer the version-pinned install from `make tools` over whatever is on PATH,
# for the reason backend/Makefile spells out: a Homebrew golangci-lint would
# silently lint at a version CI does not run. GOLANGCI_LINT lets a caller that
# has already resolved the binary (backend/Makefile has) pass it straight in.
golangci="${GOLANGCI_LINT:-}"
if [[ -z "$golangci" ]]; then
  pinned="$(go env GOPATH)/bin/golangci-lint"
  if [[ -x "$pinned" ]]; then
    golangci="$pinned"
  elif command -v golangci-lint >/dev/null 2>&1; then
    golangci="$(command -v golangci-lint)"
  else
    echo "FAIL: golangci-lint not found — run 'make tools'"
    exit 1
  fi
fi

# Issue paths are relative to the config file's directory (golangci v2's default
# `relative-path-mode: cfg`), which is what the exclusion rules in
# backend/.golangci.yml are anchored to and what a reported `../` counts up
# from. Resolving a path against anything else would put in-repo findings
# outside the repo, or foreign ones inside it — both silently.
base="$PWD"
prev=""
for arg in "$@"; do
  if [[ "$arg" == --config=* ]]; then
    base="$(dirname "${arg#--config=}")"
  elif [[ "$prev" == "--config" || "$prev" == "-c" ]]; then
    base="$(dirname "$arg")"
  fi
  prev="$arg"
done
base="$(cd "$base" && pwd -P)"

log="$(mktemp)"
trap 'rm -f "$log"' EXIT

# Streamed, not captured and replayed: the backend lane's baseline pass takes
# minutes and a gate that goes silent for them looks hung. stderr joins stdout
# so the "no such file or directory" warnings golangci emits about the foreign
# files stay next to the findings they belong to. The stated cost of reading
# what golangci printed is that it now prints into a pipe, so its `--color auto`
# resolves to none where a bare terminal run used to be coloured.
set +e
"$golangci" "$@" 2>&1 | tee "$log"
status=${PIPESTATUS[0]}
set -e

# lexical_abs — resolve a reported path against $base WITHOUT touching the disk.
# The whole point is paths that do not exist, so anything that stats (realpath,
# cd) answers "no" for the case under test and cannot tell it from a file merely
# deleted in this tree.
lexical_abs() {
  local path="$1" out="" segment
  [[ "$path" == /* ]] || path="$base/$path"
  while IFS= read -r segment; do
    case "$segment" in
    '' | .) ;;
    ..) out="${out%/*}" ;;
    *) out="$out/$segment" ;;
    esac
  done < <(printf '%s\n' "${path//\//$'\n'}")
  printf '%s\n' "${out:-/}"
}

# The escape-stripping is not decoration. `--color always` survives a pipe, and
# a caller is free to pass it; the codes carry no space, so they would ride into
# the captured path and resolve to a directory nothing matches — a guard that
# quarantined every run the moment someone asked for colour.
escape="$(printf '\033')"
outside=()
while IFS= read -r reported; do
  [[ -n "$reported" ]] || continue
  absolute="$(lexical_abs "$reported")"
  if [[ "$absolute" != "$root" && "$absolute" != "$root"/* ]]; then
    outside+=("$absolute")
  fi
done < <(LC_ALL=C sed -n -e "s/${escape}\[[0-9;]*m//g" \
  -e 's/^\([^ ]*\.go\):[0-9][0-9]*:[0-9][0-9]*:.*/\1/p' "$log" | sort -u)

if ((${#outside[@]} > 0)); then
  echo
  echo "STALE CACHE — the findings above are not about this checkout."
  echo
  echo "golangci-lint reported ${#outside[@]} file(s) that lie outside $root:"
  # Capped, and the cap says so — a poisoned entry for a large package can name
  # hundreds of files, and a page of them buries the sentence that explains it.
  printf '  %s\n' "${outside[@]:0:10}"
  if ((${#outside[@]} > 10)); then
    echo "  … and $((${#outside[@]} - 10)) more, all outside this checkout"
  fi
  echo
  echo "Its analysis cache is machine-wide and keyed by file CONTENT, not by path, so"
  echo "an entry another worktree filled answered for this run and kept that worktree's"
  echo "path. A foreign path is one golangci cannot read '//nolint:' directives from and"
  echo "one the path-anchored exclusions in .golangci.yml do not match, so findings that"
  echo "are waived HERE came back — under module names that do exist here. There is"
  echo "nothing in this checkout to fix."
  echo
  echo "Clear the cache and run the gate again:"
  echo
  echo "  \"\$(go env GOPATH)/bin/golangci-lint\" cache clean"
  echo
  echo "That pinned binary, not a bare 'golangci-lint': 'make tools' installs the version"
  echo "the gates run, and another one earlier in PATH would clear a different cache."
  exit 40
fi

exit "$status"
