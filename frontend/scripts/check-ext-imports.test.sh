#!/usr/bin/env bash
# The import gate's own test. It runs against SYNTHETIC unit trees rather than
# the real extensions/, because the real ones are supposed to pass — a gate
# proven only by "the tree is currently clean" is one that keeps passing after
# it stops working.
#
# Usage: bash frontend/scripts/check-ext-imports.test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GATE="$SCRIPT_DIR/check-ext-imports.sh"
FRONTEND_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# A unit that declares react as a peer and nothing else.
scaffold() {
  local unit="$1" body="$2"
  local layer="$TMP/extensions/$unit/frontend"
  mkdir -p "$layer"
  cat >"$layer/package.json" <<'JSON'
{
  "name": "@margince-ext/probe",
  "private": true,
  "main": "screen.tsx",
  "peerDependencies": { "react": "^19.0.0" },
  "devDependencies": { "vitest": "^3.0.0" }
}
JSON
  printf '%s\n' "$body" >"$layer/screen.tsx"
}

# The same unit, with the body written to a TEST file instead of the screen.
scaffold_test() {
  local body="$1"
  scaffold probe 'export default function S() { return null }'
  printf '%s\n' "$body" >"$TMP/extensions/probe/frontend/screen.test.tsx"
}

# The same unit, plus one more file it ships beside the screen. The extension is
# the caller's: a bundler does not care which of a unit's files reaches core, so
# neither may the gate.
scaffold_sibling_file() {
  local filename="$1" body="$2"
  scaffold probe 'export default function S() { return null }'
  printf '%s\n' "$body" >"$TMP/extensions/probe/frontend/$filename"
}

# The same unit, plus a directory BESIDE its frontend/ layer — the gate scans
# only the layer, so anything the screen can reach outside it is ungated.
scaffold_sibling_dir() {
  local dirname="$1" body="$2"
  local dir="$TMP/extensions/probe/$dirname"
  mkdir -p "$dir"
  printf '%s\n' "$body" >"$dir/steal.ts"
}

run_gate() {
  MARGINCE_EXT_DIR="$TMP/extensions" \
    MARGINCE_SURFACE_PKG="$FRONTEND_DIR/package.json" \
    bash "$GATE" 2>&1
}

FAILURES=0

expect_refusal() {
  local name="$1" body="$2" want="$3"
  rm -rf "${TMP:?}/extensions"
  scaffold probe "$body"
  local out status=0
  out="$(run_gate)" || status=$?
  if [[ "$status" -eq 0 ]]; then
    echo "FAIL: the gate accepted $name" >&2
    FAILURES=$((FAILURES + 1))
  elif ! grep -q -- "$want" <<<"$out"; then
    echo "FAIL: $name refused, but the message does not mention '$want':" >&2
    echo "$out" >&2
    FAILURES=$((FAILURES + 1))
  fi
}

# A refusal case whose tree the caller has already built, for the ones a single
# screen.tsx cannot express.
expect_refusal_tree() {
  local name="$1" want="$2"
  local out status=0
  out="$(run_gate)" || status=$?
  if [[ "$status" -eq 0 ]]; then
    echo "FAIL: the gate accepted $name" >&2
    FAILURES=$((FAILURES + 1))
  elif ! grep -q -- "$want" <<<"$out"; then
    echo "FAIL: $name refused, but the message does not mention '$want':" >&2
    echo "$out" >&2
    FAILURES=$((FAILURES + 1))
  fi
}

expect_accepted() {
  local name="$1" body="$2"
  rm -rf "${TMP:?}/extensions"
  scaffold probe "$body"
  local out status=0
  out="$(run_gate)" || status=$?
  if [[ "$status" -ne 0 ]]; then
    echo "FAIL: the gate refused $name:" >&2
    echo "$out" >&2
    FAILURES=$((FAILURES + 1))
  fi
}

# The deep import wearing a relative disguise — the whole reason this exists.
expect_refusal "a relative escape into core" \
  'import { session } from "../../../frontend/src/app/session";' \
  "leaves the unit's own frontend/"

# The same escape, spelled four other ways. Each of these passed the gate once:
# it is one thing to refuse the deep import a well-behaved unit would write by
# accident, and another to refuse the one an author is trying to sneak past —
# and a boundary DESIGN.md §4.6 asks a reviewer to accept a supply-chain cost
# for has to be the second kind.
expect_refusal "a relative escape written with single quotes" \
  "import { session } from '../../../frontend/src/app/session';" \
  "leaves the unit's own frontend/"

expect_refusal "a relative escape split across lines" \
  'import { session } from
  "../../../frontend/src/app/session";' \
  "leaves the unit's own frontend/"

# A unit's shipped code is not only its .tsx. A `main` of "screen.jsx" is a
# fully unscanned unit if the collector only ever looks at TypeScript.
rm -rf "${TMP:?}/extensions"
scaffold_sibling_file "helper.js" \
  'import { session } from "../../../frontend/src/app/session";'
expect_refusal_tree "a relative escape in a .js file the unit ships" \
  "leaves the unit's own frontend/"

# The containment test is a prefix test, so a sibling directory whose name
# merely STARTS with the layer's name reads as inside it — and is scanned by
# nothing, because the collector globs extensions/*/frontend.
rm -rf "${TMP:?}/extensions"
scaffold probe 'import { steal } from "../frontend-lib/steal";'
scaffold_sibling_dir "frontend-lib" 'export const steal = 1;'
expect_refusal_tree "a relative escape into a sibling directory named like the layer" \
  "leaves the unit's own frontend/"

expect_refusal "an unpublished core subpath" \
  'import { thing } from "@margince/frontend/internals";' \
  "is not a published subpath"

expect_refusal "an undeclared npm package" \
  'import dayjs from "dayjs";' \
  "is not declared by"

# A package the unit DOES declare is fine, and so is the surface, and so is a
# relative import that stays inside the unit.
expect_accepted "a declared peer, the surface, and an internal relative import" \
  'import { useState } from "react";
import { Button } from "@margince/frontend/design-system";
import { helper } from "./helper";'

# A devDependency is for tests, and only for tests: shipped code importing one
# would pull a test runner into the bundle.
rm -rf "${TMP:?}/extensions"
scaffold_test 'import { it } from "vitest";'
if ! out="$(run_gate)"; then
  echo "FAIL: the gate refused a test importing a declared devDependency:" >&2
  echo "$out" >&2
  FAILURES=$((FAILURES + 1))
fi

expect_refusal "shipped code importing a devDependency" \
  'import { it } from "vitest";' \
  "is not declared by"

if [[ "$FAILURES" -ne 0 ]]; then
  echo "$FAILURES case(s) failed" >&2
  exit 1
fi
echo "PASS — the extension import gate refuses what it must and accepts what it must"
