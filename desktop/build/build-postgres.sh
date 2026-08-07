#!/usr/bin/env bash
# Build a relocatable PostgreSQL + pgvector for darwin-arm64.
#
# "Relocatable" is the whole point: the result must run from wherever the
# user drags Margince.app, so nothing inside it may reference an absolute
# path outside itself. Postgres finds its own share/ and lib/ relative to
# the running executable, so the binaries are already position-independent
# once the Mach-O load commands are rewritten to @rpath — which is what
# the relocate step below does.
#
# Only the three contrib modules the schema actually requires are built
# (unaccent, pg_trgm, btree_gist); building all of contrib drags in
# optional system dependencies we deliberately configure away.
set -euo pipefail

PG_VERSION="16.14"
PG_SHA256="f6d077142737920858ce958ccdb75c6ee137a63b5b0853c70693d401ac7e3471"
PGVECTOR_VERSION="0.8.6"
PGVECTOR_SHA256="10bf9938906e5d643bbc4a7eea104b6f57ba4898e5b76b20e60484ea1d5a7f8f"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
STAGE="$ROOT/build/desktop/.stage"
WORK="$STAGE/.work"
OUT="$STAGE/pgsql"
JOBS="$(sysctl -n hw.ncpu)"

# The contrib modules the migrations require:
#   unaccent, pg_trgm  -> core/0052_fts_linguistics.up.sql
#   btree_gist         -> core/0032_meeting_exclusion.up.sql
# (vector comes from pgvector, built separately below.)
CONTRIB_MODULES=(unaccent pg_trgm btree_gist)

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

require_tools() {
  local missing=()
  for tool in curl shasum make clang install_name_tool otool codesign; do
    command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
  done
  if [ ${#missing[@]} -gt 0 ]; then
    echo "missing required tools: ${missing[*]}" >&2
    echo "install the Xcode Command Line Tools: xcode-select --install" >&2
    exit 1
  fi
}

# fetch <url> <dest> <sha256> — download once, and verify every time so a
# truncated or tampered cache in .work can never silently reach a build.
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

build_postgres() {
  local src="$WORK/postgresql-$PG_VERSION"
  rm -rf "$src"
  log "unpacking postgresql $PG_VERSION"
  tar -xjf "$WORK/postgresql-$PG_VERSION.tar.bz2" -C "$WORK"

  log "configuring postgresql (minimal external dependencies)"
  # Every --without here removes a dylib we would otherwise have to bundle
  # and relocate. ICU is safe to drop because the schema declares no
  # collations; readline only affects interactive psql, which the bundle
  # never launches. zlib resolves to the macOS SDK stub, which is a system
  # path and therefore stable across machines.
  (
    cd "$src"
    ./configure \
      --prefix="$OUT" \
      --without-icu \
      --without-readline \
      --without-openssl \
      --disable-debug \
      CFLAGS="-O2"
  )

  log "building postgresql (-j$JOBS)"
  make -C "$src" -j"$JOBS" world-bin
  make -C "$src" install-strip

  for module in "${CONTRIB_MODULES[@]}"; do
    log "building contrib/$module"
    make -C "$src/contrib/$module" -j"$JOBS"
    make -C "$src/contrib/$module" install
  done
}

build_pgvector() {
  local src="$WORK/pgvector-$PGVECTOR_VERSION"
  rm -rf "$src"
  log "unpacking pgvector $PGVECTOR_VERSION"
  tar -xzf "$WORK/pgvector-$PGVECTOR_VERSION.tar.gz" -C "$WORK"

  log "building pgvector against the staged postgres"
  # PG_CONFIG points at the build we just staged, so vector.so lands in
  # that installation's lib/ and matches its ABI exactly.
  make -C "$src" -j"$JOBS" PG_CONFIG="$OUT/bin/pg_config"
  make -C "$src" install PG_CONFIG="$OUT/bin/pg_config"
}

# mach_o_files — every binary and loadable object in the staged tree.
mach_o_files() {
  find "$OUT/bin" -type f -perm -u+x
  find "$OUT/lib" -type f \( -name '*.dylib' -o -name '*.so' \)
}

relocate() {
  log "rewriting Mach-O load commands to @rpath"

  # A dylib's install name is what dependents record. Reduce it to
  # @rpath/<name> so the loader resolves it through the dependent's rpath
  # rather than the absolute staging path baked in at link time.
  local lib
  while IFS= read -r lib; do
    install_name_tool -id "@rpath/$(basename "$lib")" "$lib"
  done < <(find "$OUT/lib" -maxdepth 1 -type f -name '*.dylib')

  local file dep rel
  while IFS= read -r file; do
    # Rewrite each dependency that points into the staging prefix.
    while IFS= read -r dep; do
      case "$dep" in
        "$OUT"/*) install_name_tool -change "$dep" "@rpath/$(basename "$dep")" "$file" ;;
      esac
    done < <(otool -L "$file" | tail -n +2 | awk '{print $1}')

    # Point the rpath at this file's own lib directory. bin/ and
    # lib/postgresql/ are each one level away from lib/; lib/ is itself.
    case "$file" in
      "$OUT"/bin/*) rel="@loader_path/../lib" ;;
      "$OUT"/lib/postgresql/*) rel="@loader_path/.." ;;
      *) rel="@loader_path" ;;
    esac
    install_name_tool -add_rpath "$rel" "$file" 2>/dev/null || true

    # install_name_tool invalidates the code signature, and arm64 macOS
    # refuses to execute an invalidly signed binary ("Killed: 9"). Re-sign
    # ad-hoc; the release pipeline re-signs the whole .app with a real
    # Developer ID afterwards.
    codesign --force --sign - --timestamp=none "$file" >/dev/null 2>&1
  done < <(mach_o_files)
}

verify() {
  log "verifying the tree is self-contained"

  # Two classes of absolute reference both work on this machine and fail on
  # the user's, and neither is visible without an explicit check:
  # a package manager's prefix, and the staging prefix this build used —
  # the latter being precisely what the relocate step exists to remove.
  local offenders=0 file dep
  while IFS= read -r file; do
    while IFS= read -r dep; do
      case "$dep" in
        /opt/homebrew/* | /usr/local/* | "$OUT"/*)
          echo "  $file -> $dep" >&2
          offenders=$((offenders + 1))
          ;;
      esac
    done < <(otool -L "$file" | tail -n +2 | awk '{print $1}')
  done < <(mach_o_files)

  if [ "$offenders" -gt 0 ]; then
    echo "FAIL: $offenders link(s) reference an absolute path outside the bundle" >&2
    exit 1
  fi

  # The extensions are the reason this build exists at all; a missing
  # control file means the migrations fail on the user's first launch.
  local ext
  for ext in vector unaccent pg_trgm btree_gist; do
    if [ ! -f "$OUT/share/extension/$ext.control" ]; then
      echo "FAIL: extension '$ext' is missing from the build" >&2
      exit 1
    fi
  done

  log "postgres $("$OUT/bin/postgres" --version | awk '{print $3}') staged at $OUT"
  log "extensions present: vector unaccent pg_trgm btree_gist"
  log "size: $(du -sh "$OUT" | awk '{print $1}')"
}

main() {
  require_tools
  mkdir -p "$WORK"
  rm -rf "$OUT"
  mkdir -p "$OUT"

  fetch "https://ftp.postgresql.org/pub/source/v$PG_VERSION/postgresql-$PG_VERSION.tar.bz2" \
    "$WORK/postgresql-$PG_VERSION.tar.bz2" "$PG_SHA256"
  fetch "https://github.com/pgvector/pgvector/archive/refs/tags/v$PGVECTOR_VERSION.tar.gz" \
    "$WORK/pgvector-$PGVECTOR_VERSION.tar.gz" "$PGVECTOR_SHA256"

  build_postgres
  build_pgvector
  relocate
  verify
}

main "$@"
