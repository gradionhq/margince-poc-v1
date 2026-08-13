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
asset="licensecheck.wasm.gz"
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

# GitHub's own digest for the stored asset, read BEFORE the download so the
# comparison is against a value this script did not derive from the bytes it is
# checking.
expected="$(gh api "repos/$repo/releases/tags/$tag" \
  --jq ".assets[] | select(.name==\"$asset\") | .digest" | sed 's/^sha256://')"
if [[ ! "$expected" =~ ^[0-9a-f]{64}$ ]]; then
  echo "fetch-license-module: release $tag publishes no $asset with a sha256 digest" >&2
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

# Both files are rewritten together, and only after the bytes verified: a tree
# holding a module and a digest that disagree is the one state the committed
# digest exists to make impossible.
mv "$work_dir/$asset" "$module_dir/$asset"
printf '%s\n' "$tag" > "$module_dir/VERSION"
(cd "$module_dir" && shasum -a 256 "$asset" > "$asset.sha256")

echo "fetch-license-module: $asset pinned to $tag (sha256 $expected)"
