#!/usr/bin/env bash
# Published-surface freeze gate (ADR-0069 §3, EXT-P3; the ADR-0023
# Amendment 2 patch-lane arm): backend/pkg is frozen published API from
# its first consumer — it evolves additively or through versioned
# successors, never in place. This gate apidiffs every published package
# against the base revision and fails on any INCOMPATIBLE change
# (removed package, removed or re-signatured symbol, narrowed interface);
# additive growth passes.
#
# Baseline: the merge-base with the extensions integration branch while
# the arc is held there (in-arc slices must not break each other), else
# with origin/main — derived, never configured. Override with
# PKG_FREEZE_BASE=<ref> for a deliberate, reviewed surface change; like
# every deliberate break it must be visible in the PR, not silent.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Pinned tool invocation (`go run` at an exact version, the
# check-contract-breaking.sh pattern): x/exp carries no tags, so the pin
# is a pseudo-version.
APIDIFF="${APIDIFF:-go run golang.org/x/exp/cmd/apidiff@v0.0.0-20260718201538-764159d718ef}"

resolve_base_ref() {
  for ref in origin/feat/extensions-adr-0069 origin/main; do
    if git rev-parse -q --verify "$ref" > /dev/null; then
      echo "$ref"
      return 0
    fi
  done
  return 1
}

BASE_REF="${PKG_FREEZE_BASE:-$(resolve_base_ref || true)}"
if [ -z "$BASE_REF" ]; then
  # In CI a missing base ref is a broken checkout, never a skip; locally
  # it just means the remote was never fetched.
  if [ "${CI:-}" = "true" ]; then
    echo "FAIL: pkg-freeze — no base ref (origin/main missing); fetch-depth must cover the merge base" >&2
    exit 1
  fi
  echo "SKIP pkg-freeze: no origin ref found — fetch origin to arm the published-surface gate"
  exit 0
fi

BASE="$(git merge-base HEAD "$BASE_REF")"

if ! git ls-tree -d "$BASE" backend/pkg > /dev/null 2>&1 || [ -z "$(git ls-tree -d "$BASE" backend/pkg)" ]; then
  echo "OK: pkg-freeze — no published surface at $BASE_REF merge-base; nothing frozen yet"
  exit 0
fi

WORKTREE="$(mktemp -d "${TMPDIR:-/tmp}/pkg-freeze.XXXXXX")"
cleanup() {
  git worktree remove --force "$WORKTREE" > /dev/null 2>&1 || true
  rm -rf "$WORKTREE"
}
trap cleanup EXIT
git worktree add --detach --quiet "$WORKTREE" "$BASE"

OLD_PKGS="$(cd "$WORKTREE/backend" && go list ./pkg/... 2> /dev/null || true)"
if [ -z "$OLD_PKGS" ]; then
  echo "OK: pkg-freeze — no published packages at the merge-base; nothing frozen yet"
  exit 0
fi

EXPORT_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pkg-freeze-export.XXXXXX")"
trap 'cleanup; rm -rf "$EXPORT_DIR"' EXIT

# A ratified surface change is recorded in the allowlist (the contract
# gate's exception pattern) as `<baseline-sha12> <package>: <finding>` —
# the exact apidiff finding BOUND to the merge-base commit it was
# ratified against. The binding is what makes an entry incapable of
# licensing anything later: the moment the ratifying PR merges, every
# future merge-base is a different commit, so the entry is provably
# expired — it cannot reactivate when a same-text finding recurs
# (remove → reintroduce → remove again) and cannot pre-authorize a
# future break. Expired entries warn (never fail: the baseline moves
# past an entry the instant its PR merges, so failing would redden the
# merged branch and every innocent sibling while the cleanup could not
# have ridden the ratifying PR). Package removals are NOT allowlistable:
# removal is the deprecate-then-major cycle.
BASE12="$(git rev-parse --short=12 "$BASE")"
ALLOWLIST="${PKG_FREEZE_ALLOWLIST:-scripts/pkg-freeze-allowlist.txt}"
allowed="$EXPORT_DIR/allowed"
expired="$EXPORT_DIR/expired"
: > "$allowed"
: > "$expired"
if [ -f "$ALLOWLIST" ]; then
  entries="$(grep -vE '^\s*(#|$)' "$ALLOWLIST" || true)"
  if [ -n "$entries" ]; then
    printf '%s\n' "$entries" | grep -E "^$BASE12 " | sed "s/^$BASE12 //" > "$allowed" || true
    printf '%s\n' "$entries" | grep -vE "^$BASE12 " > "$expired" || true
  fi
fi

findings="$EXPORT_DIR/findings"
: > "$findings"
failed=0
count=0
for pkg in $OLD_PKGS; do
  count=$((count + 1))
  rel="${pkg#github.com/gradionhq/margince/backend/}"
  if [ ! -d "backend/$rel" ]; then
    echo "FAIL: pkg-freeze — published package $pkg was removed; published surface dies slowly (deprecate, then remove with its major cycle — ADR-0069 §3)" >&2
    failed=1
    continue
  fi
  export_file="$EXPORT_DIR/$(echo "$pkg" | tr '/' '_').export"
  (cd "$WORKTREE/backend" && $APIDIFF -w "$export_file" "./$rel")
  (cd backend && $APIDIFF -incompatible "$export_file" "./$rel") \
    | sed -e 's/^- //' -e "s|^|$pkg: |" >> "$findings"
done

violations="$(grep -Fxv -f "$allowed" "$findings" || true)"
unused="$(grep -Fxv -f "$findings" "$allowed" || true)"

if [ -n "$violations" ]; then
  echo "FAIL: pkg-freeze — incompatible published-surface change vs $BASE_REF (merge-base $BASE12):" >&2
  echo "$violations" | sed 's/^/  /' >&2
  echo "A ratified change is recorded in $ALLOWLIST as this exact line (visible in the PR):" >&2
  echo "$violations" | sed "s/^/  $BASE12 /" >&2
  failed=1
fi
if [ -s "$expired" ]; then
  echo "WARN: pkg-freeze — allowlist entries bound to a superseded baseline (they can license nothing; remove them with the next allowlist edit):"
  sed 's/^/  /' "$expired"
fi
if [ -n "$unused" ]; then
  echo "WARN: pkg-freeze — allowlist entries bound to the current baseline ($BASE12) match no finding (typo, or the change was reverted — remove them):"
  echo "$unused" | sed 's/^/  /'
fi

if [ "$failed" -ne 0 ]; then
  echo "pkg-freeze: the published surface changes additively or via versioned successors, never in place (EXT-P3)." >&2
  exit 1
fi
active="$(($(grep -c . "$allowed" || true) - $(echo "$unused" | grep -c . || true)))"
echo "OK: pkg-freeze — published surface additive-or-unchanged vs $BASE_REF ($count packages, $active active exceptions)"
