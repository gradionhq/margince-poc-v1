#!/usr/bin/env bash
# The oldest macOS this bundle runs on, and the one place it is written down.
#
# Sourced, not run: `. "$HERE/macos-target.sh"`.
#
# WHY THIS FILE EXISTS. clang defaults its deployment target to the OS of the
# machine doing the build. Left alone, every C binary here is stamped
# LC_BUILD_VERSION minos = whatever the builder happened to be running, and
# macOS refuses to launch a binary whose minos is newer than the host. The
# bundle would then work for everyone on the build machine's macOS or newer and
# fail for everyone else — with a floor nobody chose, nobody wrote down, and
# that moves every time the builder takes an OS update.
#
# 13.0 is not arbitrary: it is Go's own floor for this toolchain, so the C
# halves (Postgres, the bus) and the Go halves (api, worker, migrate, the
# launcher) agree on one number instead of disagreeing silently. It follows Go
# rather than leading it — go1.27 ended macOS 12 support, so a Go binary from
# this tree reports minos 13.0 and the assertion below refuses the bundle until
# this number matches. Check what a Go binary reports before changing it:
#
#     vtool -show-build <a built Go binary> | grep minos
MACOS_MIN="13.0"
export MACOSX_DEPLOYMENT_TARGET="$MACOS_MIN"

# assert_min_os <file>... — no shipped binary may require a NEWER macOS than
# the floor above.
#
# A check rather than a comment, because the failure it guards is invisible on
# the machine that causes it: the builder's own Mac always satisfies whatever
# floor the builder's own Mac produced.
assert_min_os() {
  local file minos newest
  for file in "$@"; do
    minos="$(vtool -show-build "$file" 2>/dev/null | awk '/^ *minos/ {print $2; exit}')"
    if [ -z "$minos" ]; then
      echo "FAIL: $file declares no macOS build version, so the OS it needs cannot be known" >&2
      return 1
    fi
    # sort -V so 9.0 sorts below 13.0 the way a version does, not the way a
    # string does.
    newest="$(printf '%s\n%s\n' "$MACOS_MIN" "$minos" | sort -V | tail -1)"
    if [ "$newest" != "$MACOS_MIN" ]; then
      echo "FAIL: $file requires macOS $minos but this bundle declares $MACOS_MIN" >&2
      echo "      It was built without MACOSX_DEPLOYMENT_TARGET, so it inherited the build machine's OS" >&2
      echo "      and would refuse to launch on any older Mac." >&2
      return 1
    fi
  done
}
