#!/usr/bin/env bash
# fetch-license-module.sh — refresh the bundled license-validation WebAssembly
# module from the margince-constellation release it is pinned to, and record
# what was fetched.
#
# The module is a compiled artifact of a PRIVATE repository, embedded in a
# PUBLIC one. That rules out both alternatives to committing the blob: the
# package cannot import the upstream host (a public source install could not
# resolve a private module path), and a build-time download would need
# constellation credentials that no source installation and none of this
# repository's CI jobs have. So the blob is committed, and this script is how it
# is replaced — never by hand, so the digest beside it cannot drift from it.
#
# WHAT IS VERIFIED: the downloaded bytes against the digest GitHub itself
# computed for the stored asset, before anything is written into the tree. The
# release's own SHA256SUMS file covers only the four CLI binaries and not the
# wasm module, so the API's per-asset digest is the available authority; asking
# upstream to cover the module in SHA256SUMS too is tracked in issue #1190.
# Committing the digest is the second half: internal/platform/licensecheck's
# TestBundledModuleMatchesItsRecordedDigest holds the blob to it on every build,
# so a swapped or truncated module fails the gate rather than a boot.
#
# USAGE
#   scripts/fetch-license-module.sh              refetch the pinned release
#   scripts/fetch-license-module.sh <tag>        move the pin to <tag>, e.g. sha-9e9c638b8d03
#
# A rolling tag is refused. `latest` names a different build every day, so a pin
# to it would not identify the module anybody reviewed.
set -euo pipefail

repo="gradionhq/margince-constellation"
# The asset's COMPRESSION is upstream's to change — it moved from gzip to brotli
# once already — and the host reads the format out of the bytes rather than the
# name, so this matches whatever framing the release publishes and the tree
# always holds it under one fixed name. That keeps a refresh a data-only diff:
# nothing in the Go tree names a compression format.
asset_glob="licensecheck.wasm.*"
bundled="licensecheck.wasm.module"
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
module_dir="$root_dir/backend/internal/platform/licensecheck/module"

if ! command -v gh >/dev/null 2>&1; then
  echo "fetch-license-module: the GitHub CLI (gh) is required to reach $repo, which is private" >&2
  exit 1
fi

tag="${1:-$(tr -d '[:space:]' < "$module_dir/VERSION")}"
if [[ ! "$tag" =~ ^sha-[0-9a-f]{12,}$ ]]; then
  echo "fetch-license-module: '$tag' is not an immutable release tag (want sha-<commit>);" >&2
  echo "  a rolling tag names a different build every day and could not identify the bundled module" >&2
  exit 1
fi

# The tag names a commit by convention; that it RESOLVES to that commit is
# checked rather than assumed. A moved or hand-made tag would otherwise pass the
# shape check above and hand over an asset built from something else entirely,
# while every reader of the pin — the vendoring comment in host.go included —
# takes the twelve hex digits as the commit the module was built from.
resolved="$(gh api "repos/$repo/commits/$tag" --jq '.sha')"
if [[ "$resolved" != "${tag#sha-}"* ]]; then
  echo "fetch-license-module: tag $tag resolves to commit $resolved, which it does not name;" >&2
  echo "  refusing to fetch an asset whose provenance the pin would misstate" >&2
  exit 1
fi

# GitHub's own digest for the stored asset, read BEFORE the download so the
# comparison is against a value this script did not derive from the bytes it is
# checking. The release's SHA256SUMS covers only the CLI binaries, so this
# per-asset digest is the available authority (issue #1190).
read -r asset expected <<<"$(gh api "repos/$repo/releases/tags/$tag" \
  --jq ".assets[] | select(.name|test(\"^licensecheck[.]wasm[.]\")) | \"\(.name) \(.digest)\"" |
  sed 's/sha256://')"
if [[ -z "${asset:-}" || ! "${expected:-}" =~ ^[0-9a-f]{64}$ ]]; then
  echo "fetch-license-module: release $tag publishes no $asset_glob with a sha256 digest" >&2
  exit 1
fi

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
gh release download "$tag" --repo "$repo" --pattern "$asset" --dir "$work_dir"

actual="$(shasum -a 256 "$work_dir/$asset" | cut -d' ' -f1)"
if [[ "$actual" != "$expected" ]]; then
  echo "fetch-license-module: the download does not match the digest GitHub reports for $tag" >&2
  echo "  expected $expected" >&2
  echo "  actual   $actual" >&2
  exit 1
fi

# All three files are rewritten together, and only after the bytes verified: a
# tree holding a module and a digest that disagree is the one state the committed
# digest exists to make impossible. The digest is recorded against the UPSTREAM
# asset name, so the pin still says which artifact was fetched.
mv "$work_dir/$asset" "$module_dir/$bundled"
printf '%s\n' "$tag" > "$module_dir/VERSION"
(cd "$module_dir" && printf '%s  %s\n' "$actual" "$asset" > "$bundled.sha256")

echo "fetch-license-module: $asset pinned to $tag (sha256 $expected)"
