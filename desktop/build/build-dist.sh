#!/usr/bin/env bash
# Assemble the distributable folder from what the other build scripts staged.
#
# Run build-postgres.sh, build-valkey.sh and build-app.sh first; this only
# stages and signs what already exists, so a missing input is an error rather
# than a silently incomplete build.
#
# The output layout is the update contract:
#
#   margince/
#   ├── margince                 replaced by an update
#   ├── Start Margince.command   replaced by an update
#   ├── runtime/                 replaced by an update
#   ├── margince.yaml            the user's — created on first run
#   ├── margince.env             the user's — created on first run
#   └── data/                    the user's — database, logs, uploads
#
# The program directory is runtime/, not resources/: codesign reads a folder
# holding both a same-named executable and a "resources" subdirectory as a
# legacy bundle, and then refuses to verify the launcher inside it.
#
# Only the first three are shipped. An update replaces exactly those and
# leaves the rest, so "copy the new files over the old folder" cannot destroy
# the records the installation exists to hold.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=desktop/build/macos-target.sh
. "$HERE/macos-target.sh"
ROOT="$(cd "$HERE/../.." && pwd)"
STAGE="$ROOT/build/desktop/.stage"
DIST="$ROOT/build/desktop/margince"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

require() {
  local path="$1" hint="$2"
  if [ ! -e "$path" ]; then
    echo "missing $path — run $hint first" >&2
    exit 1
  fi
}

# The double-clickable entry point. Finder opens a .command file in Terminal,
# which is what makes a terminal-launched stack reachable without one.
write_starter() {
  cat > "$DIST/Start Margince.command" <<'STARTER'
#!/bin/bash
# Double-click this file to start Margince.
cd "$(dirname "$0")"

# Gatekeeper has already asked about THIS file by the time it runs, so clearing
# the launcher's own quarantine here is what stops the identical question being
# asked again about ./margince a moment later. The launcher clears runtime/ for
# the programs it spawns; between the two, a downloaded bundle costs exactly one
# dialog however it is started.
#
# This one file and no more: the folder also holds the user's records, which are
# not ours to relabel, and a live Postgres socket, which the call fails against.
/usr/bin/xattr -d com.apple.quarantine ./margince 2>/dev/null || true

./margince
STARTER
  chmod +x "$DIST/Start Margince.command"
}

stage_dist() {
  log "assembling the distributable folder"
  rm -rf "$DIST"
  mkdir -p "$DIST/runtime"

  cp -R "$STAGE/pgsql" "$DIST/runtime/pgsql"
  cp "$STAGE/valkey/valkey-server" "$DIST/runtime/valkey-server"
  cp "$STAGE/bin/api" "$STAGE/bin/worker" "$STAGE/bin/migrate" "$DIST/runtime/"
  cp -R "$STAGE/web" "$DIST/runtime/web"
  cp "$STAGE/bin/margince" "$DIST/margince"
  write_starter
}

# verify_signed checks that what we assembled is actually signed.
#
# Nothing is signed HERE: each build script signs its own output in staging,
# because codesign reads a directory containing a same-named executable as a
# bundle and would try to sign this whole folder. Signatures are embedded in
# the Mach-O and survive the copy, so this only confirms they arrived.
#
# Ad-hoc only. A published build needs a Developer ID plus notarization,
# without which a downloaded copy is quarantined and the first launch is
# refused as coming from an unidentified developer.
verify_signed() {
  log "verifying signatures (ad-hoc — NOT release signing)"
  local binary
  for binary in margince runtime/api runtime/worker runtime/migrate \
    runtime/valkey-server runtime/pgsql/bin/postgres; do
    if ! codesign --verify "$DIST/$binary" 2>/dev/null; then
      echo "FAIL: $binary is not validly signed" >&2
      exit 1
    fi
  done
}

# verify_runnable_os checks what the ASSEMBLED folder needs, not what each
# build step meant to produce.
#
# The per-step checks constrain their own output; this one constrains the thing
# the user actually copies, and it is the only place the Go binaries and the C
# binaries are judged by the same rule. It also names the one architecture the
# folder contains — a bundle built on Apple silicon does not run on an Intel
# Mac, and vice versa, so which one this is belongs in the build log rather
# than in a support conversation.
verify_runnable_os() {
  log "verifying every binary runs on macOS $MACOS_MIN or newer"

  # Mach-O by content, not by the executable bit: "Start Margince.command" is
  # an executable shell script, vtool has nothing to say about it, and asking
  # would fail this check on the one shipped file that has no OS requirement
  # at all.
  local binaries=() file
  while IFS= read -r file; do
    if file -b "$file" | grep -q 'Mach-O'; then
      binaries+=("$file")
    fi
  done < <(find "$DIST" -type f)
  # An empty list would pass this check while examining nothing — the way a
  # verification step most often fails.
  if [ ${#binaries[@]} -eq 0 ]; then
    echo "FAIL: found no executables in $DIST to check" >&2
    exit 1
  fi
  if ! assert_min_os "${binaries[@]}"; then
    exit 1
  fi
  log "architecture: $(lipo -archs "$DIST/margince")"
}

main() {
  require "$STAGE/pgsql/bin/postgres" "build-postgres.sh"
  require "$STAGE/valkey/valkey-server" "build-valkey.sh"
  require "$STAGE/bin/api" "build-app.sh"
  require "$STAGE/bin/margince" "build-app.sh"
  require "$STAGE/web/index.html" "build-app.sh"

  stage_dist
  verify_signed
  verify_runnable_os

  log "built $DIST ($(du -sh "$DIST" | awk '{print $1}'))"
  log "run it:  \"$DIST/margince\""
}

main "$@"
