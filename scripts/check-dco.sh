#!/bin/sh
# Developer Certificate of Origin gate: every commit introduced by a PR must
# carry a `Signed-off-by:` trailer (git commit -s). Called by the `dco` CI job
# with the PR's base and head SHAs; the range excludes history already on the
# base branch, so only the PR's own commits are checked.
set -eu

base="${1:?usage: check-dco.sh <base-sha> <head-sha>}"
head="${2:?usage: check-dco.sh <base-sha> <head-sha>}"

range="${base}..${head}"
missing=0

# %H is the full hash; the trailer is matched case-insensitively against the
# whole commit message body via git log's grep, per commit.
for sha in $(git rev-list "$range"); do
	if ! git log -1 --format='%B' "$sha" | grep -qiE '^Signed-off-by: .+ <.+@.+>'; then
		subject="$(git log -1 --format='%s' "$sha")"
		echo "DCO: commit ${sha} missing Signed-off-by trailer: ${subject}" >&2
		missing=1
	fi
done

if [ "$missing" -ne 0 ]; then
	echo "" >&2
	echo "Every commit must be signed off (git commit -s). See CONTRIBUTING.md." >&2
	exit 1
fi

echo "DCO: all commits in ${range} are signed off"
