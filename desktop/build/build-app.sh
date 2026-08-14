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

# build_frontend builds the COMPOSED SPA, for the same reason the server
# binaries are built through build/composition/: a bare `pnpm build` resolves
# the committed empty-tree registry, so the bundle would ship a server with the
# enabled units linked and a UI that routes none of them. The Dockerfile's web
# stage is the reference for this lane.
build_frontend() {
  log "building the frontend (composed)"
  local registry="$ROOT/build/composition/frontend"
  if [ ! -f "$registry/extensions.gen.ts" ]; then
    echo "FAIL: gen-composition did not produce $registry/extensions.gen.ts" >&2
    exit 1
  fi

  # The published-event types are the one generated artifact whose composed
  # form is written back into the tracked source tree (frontend/src/api/), so
  # this lane refuses to run it: a throwaway Docker layer can afford a dirty
  # checkout and a developer's cannot. As long as no enabled unit contributes
  # a public event the two documents are identical and there is nothing to
  # generate; the moment one does, this says so instead of shipping types that
  # silently omit it.
  if ! diff -q "$ROOT/backend/api/public-events.yaml" "$ROOT/build/composition/api/public-events.yaml" >/dev/null; then
    echo "FAIL: an enabled extension contributes public events, so frontend/src/api/public-events.ts must be regenerated (pnpm gen:events:composed) before this bundle can ship correct types" >&2
    exit 1
  fi

  # --ignore-scripts matches every other frontend install in this repo (the
  # Dockerfile, scripts/verify-boot.sh, the CI lane): a lockfile pins WHAT is
  # installed, and this stops a dependency's lifecycle script from running
  # arbitrary code on the build machine on the way in.
  (cd "$ROOT/frontend" &&
    pnpm install --frozen-lockfile --ignore-scripts &&
    MARGINCE_COMPOSITION_FRONTEND="$registry" pnpm gen:composed-types &&
    MARGINCE_COMPOSITION_FRONTEND="$registry" pnpm build:composed)
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
