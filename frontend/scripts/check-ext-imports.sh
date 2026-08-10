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
# Fails on, in any *.ts / *.tsx under extensions/*/frontend/:
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

  # What this unit is allowed to reach on npm: its own declared dependencies,
  # plus the core surface. Read per unit, because one unit's dependency is not
  # another's.
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

  while IFS= read -r -d '' file; do
    CHECKED=$((CHECKED + 1))
    # Every static specifier in the file: `from "x"`, `import "x"`, and
    # `import("x")`. Comments are not stripped — a commented-out bad import is
    # a bad import somebody is about to uncomment.
    while IFS= read -r spec; do
      [[ -n "$spec" ]] || continue
      case "$spec" in
      .*)
        # A relative specifier is fine inside the unit; it is the ESCAPE that
        # is refused. Resolve it and check it still lands under the layer.
        resolved="$(cd "$(dirname "$file")" && cd "$(dirname "$spec")" 2>/dev/null && pwd || true)"
        if [[ -z "$resolved" || "$resolved" != "$layer"* ]]; then
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
        for d in "${DECLARED[@]}"; do
          [[ "$pkgname" == "$d" ]] && declared=1 && break
        done
        if [[ "$declared" -eq 0 ]]; then
          echo "FAIL: ${file#"$ROOT"/}: '$spec' is not declared by extensions/$unit/frontend/package.json — a unit imports what it declares, or it works only by accident of hoisting" >&2
          EXIT=1
        fi
        ;;
      esac
    done < <(
      grep -oE '(from|import)[[:space:]]*\(?[[:space:]]*"[^"]+"' "$file" |
        grep -oE '"[^"]+"' | tr -d '"' || true
    )
  done < <(find "$layer" -type f \( -name "*.ts" -o -name "*.tsx" \) -not -path "*/node_modules/*" -print0)
done

echo "==> extension import gate ($CHECKED file(s) under extensions/*/frontend)"
if [[ "$EXIT" -eq 0 ]]; then
  echo "PASS — every unit screen reaches the core only through the published surface"
fi
exit "$EXIT"
