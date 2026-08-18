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
# THE TRAP THIS GATE IS ITSELF EXPOSED TO. The backend lane must run on a bare Go
# checkout, so this leg has to be skippable — and a gate that skips cleanly is a
# gate that can skip silently, which is the same defect one level up. Three rules
# hold it shut, and each is tested by check-contract-frontend-drift.test.sh:
#
#   1. The skip is LOUD. It names the leg and the reason, on stderr, every time.
#   2. An environment that HAS pnpm cannot take the skip path — the only skip
#      condition is pnpm's absence — and CI cannot take it at all: with
#      GITHUB_ACTIONS or CI set, a missing pnpm is a hard failure, because the
#      day CI's toolchain shifts the gate must report it rather than quietly
#      stop working.
#   3. Census, not verdict. `pnpm gen:api` is required to have actually REWRITTEN
#      every artifact named below before the diff is trusted. A generator that
#      silently wrote nothing and a tree with no drift produce the same clean
#      diff, and "checked nothing successfully" must not read as green.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# The artifacts this leg is responsible for, relative to frontend/. The set is
# named once and asserted non-empty below: a list that quietly empties is the
# failure mode this gate exists to make impossible.
ARTIFACTS=(src/api/schema.d.ts src/api/public-events.ts)

in_ci() { [[ -n "${GITHUB_ACTIONS:-}" || -n "${CI:-}" ]]; }

if ! command -v pnpm >/dev/null 2>&1; then
  if in_ci; then
    echo "check-contract-frontend-drift: FAIL — pnpm is not on PATH in a CI environment." >&2
    echo "  This leg regenerates frontend/src/api/*.d.ts from backend/api/crm.yaml and" >&2
    echo "  fails when the committed types drifted. CI has the frontend toolchain, so a" >&2
    echo "  missing pnpm here means the toolchain moved — not that the check is optional." >&2
    echo "  Restore pnpm on this job, or move the leg deliberately. Do not let it skip." >&2
    exit 1
  fi
  echo "check-contract-frontend-drift: SKIPPED — pnpm is not on PATH." >&2
  echo "  ${#ARTIFACTS[@]} artifact(s) NOT checked: ${ARTIFACTS[*]}" >&2
  echo "  A contract change that skips \`pnpm gen:api\` will not be caught by this run." >&2
  echo "  Install the frontend toolchain (\`make install\`) to arm this leg." >&2
  exit 0
fi

if [[ ${#ARTIFACTS[@]} -eq 0 ]]; then
  echo "check-contract-frontend-drift: FAIL — the artifact list is empty, so this leg" >&2
  echo "  would report success having compared nothing." >&2
  exit 1
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

pnpm install --frozen-lockfile
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
