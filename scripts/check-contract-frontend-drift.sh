#!/usr/bin/env bash
# check-contract-frontend-drift.sh — the frontend's generated types are part of
# the contract, so the BACKEND gate has to notice when they are stale.
#
# Editing backend/api/crm.yaml requires three regenerations. `make check-backend`
# used to enforce one:
#
#   make gen        → internal/contracts, compose stubs, agentpolicy, recordshapes
#                     — enforced by the backend `drift` gate
#   pnpm gen:api    → frontend/src/api/schema.d.ts + public-events.ts
#                     — enforced ONLY by the frontend lane's fe-drift leg
#   -update-mcp-info → the published MCP surface docs
#                     — enforced by a unit test in check-backend
#
# `make gen` has no frontend reference at all, and a backend-only author has no
# reason to run the lane that would catch the second. So a contract change could
# go green through the whole backend gate and strand the frontend types — which
# is not hypothetical: it put main red for a day (#1573), and the trail ran from
# two screens the author never opened back to an audit-action contract change two
# PRs earlier, as a dozen TS2322 lines naming properties that exist.
#
# The Makefile already STATED this invariant in frontend-check's own comment
# while enforcing it nowhere. This makes the claim true rather than softening it.
#
# WHERE THIS RUNS, AND WHERE IT DOES NOT. This is the LOCAL half. CI already
# covers the same ground from the other side and it is worth being exact about
# which, because #1639 assumed otherwise:
#
#   - CI's `fe-quality` job runs `make fe-drift`, and the change classifier
#     routes `backend/api/**` to the frontend lane — so a contract change on a
#     pull request does meet this check there.
#   - CI's `deterministic-gates` job, which runs `make check-backend`, installs
#     Go and nothing else. There is no pnpm on it and it does not need one.
#
# So the environment this leg exists for is a developer's: `make check-backend`
# or the pre-push hook, where a backend-only author never runs the lane that
# would catch it. That is exactly the gap #1639 describes.
#
# The CI half is not left to trust either — it is pinned by
# TestTheContractReachesTheFrontendLane (backend/contractfrontendlane_test.go),
# which fails when the classifier stops routing `backend/api/**` to the
# frontend lane. Dropping that one line is what would actually reopen #1573,
# and it would otherwise be invisible.
#
# THE TRAP THIS GATE IS ITSELF EXPOSED TO. It has to be skippable, because the
# backend lane must run on a bare Go checkout — and a gate that skips cleanly is
# a gate that can skip silently, which is the same defect one level up. Two rules
# hold it shut, and each is tested by check-contract-frontend-drift.test.sh:
#
#   1. The skip is LOUD. It names the leg, the reason and the artifacts it did
#      not check, on stderr, every time. Its only trigger is pnpm's absence, so
#      an environment that HAS pnpm cannot skip.
#   2. Census, not verdict. `pnpm gen:api` is required to have actually REWRITTEN
#      every artifact named below before the diff is trusted. A generator that
#      silently wrote nothing and a tree with no drift produce the same clean
#      diff, and "checked nothing successfully" must not read as green.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# The artifacts this leg is responsible for, relative to frontend/. The set is
# named once and asserted non-empty below: a list that quietly empties is the
# failure mode this gate exists to make impossible.
ARTIFACTS=(src/api/schema.d.ts src/api/public-events.ts)

# The census comes FIRST, before any exit. An empty list on the skip path would
# print "0 artifact(s) NOT checked" and exit 0, which is the vacuous pass this
# gate is about — reachable on a bare checkout, where nobody would look.
if [[ ${#ARTIFACTS[@]} -eq 0 ]]; then
  echo "check-contract-frontend-drift: FAIL — the artifact list is empty, so this leg" >&2
  echo "  would report success having compared nothing." >&2
  exit 1
fi

if ! command -v pnpm >/dev/null 2>&1; then
  echo "check-contract-frontend-drift: SKIPPED — pnpm is not on PATH." >&2
  echo "  ${#ARTIFACTS[@]} artifact(s) NOT checked: ${ARTIFACTS[*]}" >&2
  echo "  A contract change that skips \`pnpm gen:api\` will not be caught by this run." >&2
  echo "  Install the frontend toolchain (\`make install\`) to arm this leg." >&2
  echo "  On a pull request CI still covers it: fe-quality runs make fe-drift, and the" >&2
  echo "  change classifier routes backend/api/** to the frontend lane." >&2
  exit 0
fi

cd "$ROOT/frontend"

# A stamp every artifact must be newer than afterwards. `find -newer` is the
# portable form of "this file was rewritten"; it is what turns a generator that
# silently did nothing into a failure instead of a clean diff.
STAMP="$(mktemp)"
trap 'rm -f "$STAMP"' EXIT
checked=0
for f in "${ARTIFACTS[@]}"; do
  [[ -f "$f" ]] || { echo "check-contract-frontend-drift: FAIL — $f does not exist; the committed contract types are missing, not merely stale" >&2; exit 1; }
done

# Installing is the frontend lane's job, not this gate's. A check that mutates
# the tree in order to read it is doing two things, and it is also how an
# unpinned `pnpm install` — lifecycle scripts and all — ends up in the path of a
# Go-only target.
if [[ ! -d node_modules ]]; then
  echo "check-contract-frontend-drift: FAIL — pnpm is on PATH but frontend/node_modules is not installed." >&2
  echo "  This leg regenerates ${#ARTIFACTS[@]} contract artifact(s) and diffs them; it cannot do that" >&2
  echo "  without the toolchain. Run \`make install\` (or \`cd frontend && pnpm install\`)." >&2
  echo "  This is a failure and not a skip on purpose: the skip exists for a checkout with NO" >&2
  echo "  frontend toolchain, and a tree that has one but has not installed it would otherwise" >&2
  echo "  turn this gate off quietly." >&2
  exit 1
fi

touch "$STAMP"
pnpm gen:api

for f in "${ARTIFACTS[@]}"; do
  if [[ -z "$(find "$f" -newer "$STAMP" -print -quit 2>/dev/null)" ]]; then
    echo "check-contract-frontend-drift: FAIL — \`pnpm gen:api\` did not rewrite $f." >&2
    echo "  The generator produced no output for an artifact this leg is responsible" >&2
    echo "  for, so a clean diff below would mean 'compared nothing', not 'no drift'." >&2
    exit 1
  fi
  checked=$((checked + 1))
done

if ! git diff --exit-code -- "${ARTIFACTS[@]}"; then
  echo "" >&2
  echo "check-contract-frontend-drift: FAIL — the frontend contract types drifted." >&2
  echo "  backend/api/crm.yaml changed and $checked generated artifact(s) were not" >&2
  echo "  regenerated with it. The frontend typecheck would fail in screens this change" >&2
  echo "  never touched, naming properties that exist (#1573)." >&2
  echo "" >&2
  echo "  Fix: cd frontend && pnpm gen:api, then commit ${ARTIFACTS[*]}" >&2
  exit 1
fi

echo "check-contract-frontend-drift: OK — $checked generated contract artifact(s) regenerated and unchanged"
