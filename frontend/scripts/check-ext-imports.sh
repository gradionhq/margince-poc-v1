#!/usr/bin/env bash
# The extension frontend import gate: a unit's screen may reach the core ONLY
# through the published surface, and may reach npm only through what its own
# package declares.
#
# This is the frontend's answer to backend/extensions_arch_test.go, and it
# carries more weight than that test does. On the Go side the compiler makes
# internal/** unreachable for free and the test holds the remainder; a bundler
# enforces nothing at all — it resolves whatever a path can reach — so on this
# side the boundary is exactly two things: frontend/package.json's `exports`
# map, and this script. Delete this and a unit can import the session store.
#
# Fails on, in any module file under extensions/*/frontend/ (every extension a
# bundler resolves — ts, tsx, mts, cts, js, jsx, mjs, cjs — because a unit whose
# `main` is a .jsx is a unit this gate would otherwise never read):
#   1. A relative specifier escaping the unit's own frontend/ directory
#      ("../../../frontend/src/app/session") — the deep import wearing a
#      relative disguise.
#   2. A @margince/frontend subpath that is not in the exports map. The allowed
#      set is READ FROM the map rather than restated here, so widening the
#      surface is one edit in one place.
#   3. A bare specifier the unit's own package.json does not declare (in
#      dependencies or peerDependencies). A unit that imports what it did not
#      declare works only by accident of hoisting, and breaks when another
#      unit stops depending on it.
#
# Usage: frontend/scripts/check-ext-imports.sh   (wired into `make check-fe`)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
FRONTEND_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ROOT="$(cd "$FRONTEND_DIR/.." && pwd)"
EXT_DIR="${MARGINCE_EXT_DIR:-$ROOT/extensions}"
SURFACE_PKG="${MARGINCE_SURFACE_PKG:-$FRONTEND_DIR/package.json}"

# The published subpaths, straight out of the map. "@margince/frontend/app"
# from "./app", and so on.
# read -a rather than mapfile: mapfile is bash 4+, and the shells this repo's
# gates actually run under include macOS's bash 3.2.
ALLOWED_SUBPATHS=()
while IFS= read -r line; do ALLOWED_SUBPATHS+=("$line"); done < <(
  node -e '
    const pkg = require(process.argv[1]);
    for (const key of Object.keys(pkg.exports ?? {})) {
      console.log("@margince/frontend" + key.replace(/^\./, ""));
    }
  ' "$SURFACE_PKG"
)
if [[ "${#ALLOWED_SUBPATHS[@]}" -eq 0 ]]; then
  echo "FAIL: $SURFACE_PKG publishes no exports — the gate has no surface to hold a unit to" >&2
  exit 1
fi

EXIT=0
CHECKED=0

for layer in "$EXT_DIR"/*/frontend; do
  [[ -d "$layer" ]] || continue
  unit="$(basename "$(dirname "$layer")")"
  manifest="$layer/package.json"
  [[ -f "$manifest" ]] || continue

  # What this unit may reach on npm, read per unit because one unit's
  # dependency is not another's — and split, because the answer differs by file.
  #
  # SHIPPED code may reach dependencies and peers. A screen importing a DEV
  # dependency would pull a test runner into the bundle, so devDependencies are
  # deliberately absent from this set.
  DECLARED=()
  while IFS= read -r line; do DECLARED+=("$line"); done < <(
    node -e '
      const pkg = require(process.argv[1]);
      for (const name of [
        ...Object.keys(pkg.dependencies ?? {}),
        ...Object.keys(pkg.peerDependencies ?? {}),
      ]) console.log(name);
    ' "$manifest"
  )
  # A TEST may reach dev dependencies too — that is what they are for.
  DECLARED_TEST=("${DECLARED[@]}")
  while IFS= read -r line; do DECLARED_TEST+=("$line"); done < <(
    node -e '
      const pkg = require(process.argv[1]);
      for (const name of Object.keys(pkg.devDependencies ?? {})) console.log(name);
    ' "$manifest"
  )

  while IFS= read -r -d '' file; do
    CHECKED=$((CHECKED + 1))
    # A test file may reach the dev dependencies; shipped code may not.
    case "$file" in
    *.test.ts | *.test.tsx | *.test.mts | *.test.cts | \
      *.test.js | *.test.jsx | *.test.mjs | *.test.cjs) ALLOWED_PKGS=("${DECLARED_TEST[@]}") ;;
    *) ALLOWED_PKGS=("${DECLARED[@]}") ;;
    esac
    # Every static specifier in the file: `from "x"`, `import "x"`, and
    # `import("x")`. Comments are not stripped — a commented-out bad import is
    # a bad import somebody is about to uncomment.
    #
    # Newlines are collapsed FIRST and all three quote characters are accepted,
    # because this gate is the only thing standing between a unit and core's
    # internals and it was previously blind to three spellings of the same
    # escape: a single-quoted specifier, a template literal, and an import
    # split across lines. Biome would have normalised the first two, but biome
    # never sees this tree — `pnpm lint` checks frontend/src, not extensions/.
    #
    # The quote characters are a class rather than a matched pair (ERE has no
    # backreference), so `"x'` over-matches. That bias is deliberate: a false
    # positive here is a loud, fixable gate failure, and a false negative is a
    # unit reading the session store. For the same reason, collapsing lines can
    # pair a comment ending in `from` with the next line's string literal — also
    # loud, also fixable, and cheaper than a parser.
    while IFS= read -r spec; do
      [[ -n "$spec" ]] || continue
      case "$spec" in
      .*)
        # A relative specifier is fine inside the unit; it is the ESCAPE that
        # is refused. Resolve it and check it still lands under the layer.
        resolved="$(cd "$(dirname "$file")" && cd "$(dirname "$spec")" 2>/dev/null && pwd || true)"
        # The layer itself, or something BENEATH it — the `/` is load-bearing.
        # An unslashed prefix test accepts a sibling whose name merely starts
        # with the layer's: extensions/foo/frontend-lib is not inside
        # extensions/foo/frontend, but "$layer"* says it is, and the collector
        # below never scans it because it globs */frontend.
        if [[ -z "$resolved" || ("$resolved" != "$layer" && "$resolved" != "$layer"/*) ]]; then
          echo "FAIL: ${file#"$ROOT"/}: relative import '$spec' leaves the unit's own frontend/ — reach the core through @margince/frontend/<subpath>, never by path" >&2
          EXIT=1
        fi
        ;;
      @margince/frontend*)
        allowed=0
        for ok in "${ALLOWED_SUBPATHS[@]}"; do
          [[ "$spec" == "$ok" ]] && allowed=1 && break
        done
        if [[ "$allowed" -eq 0 ]]; then
          echo "FAIL: ${file#"$ROOT"/}: '$spec' is not a published subpath — the surface is ${ALLOWED_SUBPATHS[*]}" >&2
          EXIT=1
        fi
        ;;
      *)
        # A bare specifier: the package root of "@scope/name/sub" or "name/sub".
        case "$spec" in
        @*) pkgname="$(echo "$spec" | cut -d/ -f1,2)" ;;
        *) pkgname="${spec%%/*}" ;;
        esac
        declared=0
        for d in "${ALLOWED_PKGS[@]}"; do
          [[ "$pkgname" == "$d" ]] && declared=1 && break
        done
        if [[ "$declared" -eq 0 ]]; then
          echo "FAIL: ${file#"$ROOT"/}: '$spec' is not declared by extensions/$unit/frontend/package.json — a unit imports what it declares, or it works only by accident of hoisting (shipped code may not reach a devDependency)" >&2
          EXIT=1
        fi
        ;;
      esac
    done < <(
      tr '\n' ' ' <"$file" |
        grep -oE $'(from|import)[[:space:]]*\\(?[[:space:]]*["\'`][^"\'`]+["\'`]' |
        grep -oE $'["\'`][^"\'`]+["\'`]' | tr -d $'"\'`' || true
    )
    # Every module format a bundler will happily take, not just the two a
    # well-behaved unit writes: `"main": "screen.jsx"` is a legal unit whose
    # every file this gate used to skip, which made the whole check opt-in.
  done < <(find "$layer" -type f \( \
    -name "*.ts" -o -name "*.tsx" -o \
    -name "*.mts" -o -name "*.cts" -o \
    -name "*.js" -o -name "*.jsx" -o \
    -name "*.mjs" -o -name "*.cjs" \
    \) -not -path "*/node_modules/*" -print0)
done

echo "==> extension import gate ($CHECKED file(s) under extensions/*/frontend)"
if [[ "$EXIT" -eq 0 ]]; then
  echo "PASS — every unit screen reaches the core only through the published surface"
fi
exit "$EXIT"
