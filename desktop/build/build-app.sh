#!/usr/bin/env bash
# Build the Margince-authored half of the bundle: the three process-role
# binaries, the frontend, and the launcher that supervises them.
#
# The server binaries are built through build/composition/, not with a bare
# `go build`, because that wiring is what links the enabled extensions/ units
# in. A bundle built against the vanilla stub would silently ship without the
# first-party packs and look identical from the outside.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
OUT="$ROOT/build/desktop/.stage"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

build_server_binaries() {
  log "materializing build/composition"
  (cd "$ROOT/backend" && GOWORK="$ROOT/go.work" go run ./tools/gen-composition)

  local composition="$ROOT/build/composition/go.work"
  if [ ! -f "$composition" ]; then
    echo "FAIL: gen-composition did not produce $composition" >&2
    exit 1
  fi

  mkdir -p "$OUT/bin"
  local role
  for role in api worker migrate; do
    log "building $role"
    (cd "$ROOT/backend" && GOWORK="$composition" go build -o "$OUT/bin/$role" "./cmd/$role")
    sign_binary "$OUT/bin/$role"
  done
}

# sign_binary ad-hoc signs one executable, HERE in the staging directory
# rather than after assembly.
#
# codesign treats a directory containing a same-named executable as a tool
# bundle, so signing the launcher once it sits in the distributable folder
# makes codesign try to sign that whole folder — and fail on the .command
# starter script it cannot sign as a subcomponent. Staging paths cannot
# collide that way. Signatures are embedded in the Mach-O, so they survive
# the copy into the folder.
sign_binary() {
  codesign --force --sign - --timestamp=none "$1"
}

build_frontend() {
  log "building the frontend"
  (cd "$ROOT/frontend" && pnpm install --frozen-lockfile && pnpm build)
  rm -rf "$OUT/web"
  cp -R "$ROOT/frontend/dist" "$OUT/web"
}

build_launcher() {
  # GOWORK=off: the launcher is a standalone stdlib-only module deliberately
  # outside the workspace, so it neither sees nor perturbs the backend's
  # dependency graph.
  log "building the launcher"
  (cd "$ROOT/desktop/launcher" && GOWORK=off go build -o "$OUT/bin/margince" .)
  sign_binary "$OUT/bin/margince"
}

main() {
  build_server_binaries
  if [ "${SKIP_FRONTEND:-0}" != "1" ]; then
    build_frontend
  fi
  build_launcher
  log "binaries in $OUT/bin"
}

main "$@"
