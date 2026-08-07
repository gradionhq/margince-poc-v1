#!/usr/bin/env bash
# Build the event bus for darwin-arm64.
#
# Valkey rather than Redis: the bundle redistributes this binary inside a
# BUSL-1.1 product, and Redis 7.4 onward ships under RSALv2/SSPL. Valkey is
# the BSD-licensed fork of the Redis 7.2 lineage and speaks the same
# protocol, so platform/events needs no change to talk to it.
#
# Only valkey-server is kept. The CLI and benchmark tools are developer
# conveniences the shipped app never invokes, and every megabyte here is a
# megabyte the user downloads.
set -euo pipefail

VALKEY_VERSION="9.1.1"
VALKEY_SHA256="7d7232acd1b8a49b4e05d07a00b3ca8c801ae06ab633ca6a3423bc5f385ab7ee"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
STAGE="$ROOT/build/desktop/.stage"
WORK="$STAGE/.work"
OUT="$STAGE/valkey"
JOBS="$(sysctl -n hw.ncpu)"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

fetch() {
  local url="$1" dest="$2" want="$3"
  if [ ! -f "$dest" ]; then
    log "downloading $(basename "$dest")"
    curl -fSL --retry 3 --max-time 600 "$url" -o "$dest.part"
    mv "$dest.part" "$dest"
  fi
  local got
  got="$(shasum -a 256 "$dest" | awk '{print $1}')"
  if [ "$got" != "$want" ]; then
    echo "checksum mismatch for $dest" >&2
    echo "  expected $want" >&2
    echo "  actual   $got" >&2
    exit 1
  fi
}

verify() {
  # Same self-containment rule as the Postgres build: a link into a package
  # manager's prefix works here and fails on the user's machine.
  local dep offenders=0
  while IFS= read -r dep; do
    case "$dep" in
      /opt/homebrew/* | /usr/local/*)
        echo "  valkey-server -> $dep" >&2
        offenders=$((offenders + 1))
        ;;
    esac
  done < <(otool -L "$OUT/valkey-server" | tail -n +2 | awk '{print $1}')

  if [ "$offenders" -gt 0 ]; then
    echo "FAIL: valkey-server links against a package-manager prefix" >&2
    exit 1
  fi

  log "$("$OUT/valkey-server" --version)"
  log "size: $(du -sh "$OUT" | awk '{print $1}')"
}

main() {
  mkdir -p "$WORK"
  rm -rf "$OUT"
  mkdir -p "$OUT"

  fetch "https://github.com/valkey-io/valkey/archive/refs/tags/$VALKEY_VERSION.tar.gz" \
    "$WORK/valkey-$VALKEY_VERSION.tar.gz" "$VALKEY_SHA256"

  local src="$WORK/valkey-$VALKEY_VERSION"
  rm -rf "$src"
  log "unpacking valkey $VALKEY_VERSION"
  tar -xzf "$WORK/valkey-$VALKEY_VERSION.tar.gz" -C "$WORK"

  log "building valkey (-j$JOBS)"
  # BUILD_TLS=no keeps OpenSSL out of the tree; the bus is reached only over
  # loopback by processes this launcher started, never across a network.
  make -C "$src" -j"$JOBS" BUILD_TLS=no

  install -m 755 "$src/src/valkey-server" "$OUT/valkey-server"
  # install_name_tool is not needed here (nothing was rewritten), but the
  # binary is re-signed for consistency with the Postgres tree so the whole
  # Resources directory presents one signing story to the notary.
  codesign --force --sign - --timestamp=none "$OUT/valkey-server" >/dev/null 2>&1

  verify
}

main "$@"
