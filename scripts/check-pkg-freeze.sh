#!/usr/bin/env bash
# Published-surface freeze gate (ADR-0069 §3, EXT-P3; the ADR-0023
# Amendment 2 patch-lane arm): backend/pkg is frozen published API from
# its first release — it evolves additively or through versioned
# successors, never in place. This gate apidiffs every published package
# against the merge target and reports every INCOMPATIBLE change
# (removed package, removed or re-signatured symbol, narrowed interface);
# additive growth always passes.
#
# TWO REGIMES, derived from the tree (no config):
#   advisory — before the first v1+ release tag exists: the surface is
#     design-fluid, incompatible changes print as ADVISORY and never
#     block. What you see is what v1.0.0 will freeze.
#   enforce  — from the first v1+ tag: incompatible changes FAIL. A
#     ratified change is recorded in the allowlist as its exact apidiff
#     finding BOUND to the merge-base sha it was ratified against (the
#     gate prints the ready-to-paste line): once the ratifying PR
#     merges, every future merge-base differs, so an entry can never
#     license a recurring same-text finding or pre-authorize a future
#     break. Superseded/unused entries warn until removed. Package
#     REMOVALS are never allowlistable (deprecate-then-major cycle).
# PKG_FREEZE_MODE=advisory|enforce overrides (testing the other regime).
#
# Baseline: the PR's merge target (origin/$GITHUB_BASE_REF in CI); the
# local approximation is the extensions integration branch while the
# arc holds there, else origin/main. PKG_FREEZE_BASE=<ref> overrides.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

indent() { sed 's/^/  /'; }

# Pinned tool invocation (`go run` at an exact version, the
# check-contract-breaking.sh pattern): x/exp carries no tags, so the pin
# is a pseudo-version.
APIDIFF="${APIDIFF:-go run golang.org/x/exp/cmd/apidiff@v0.0.0-20260718201538-764159d718ef}"

if [[ -n "${PKG_FREEZE_MODE:-}" ]]; then
  MODE="$PKG_FREEZE_MODE"
else
  # The first STABLE v1+ release tag arms enforcement — exact release
  # syntax only (vMAJOR.MINOR.PATCH, major ≥ 1), so a prerelease
  # (v1.0.0-rc.1) or an unrelated v1* tag cannot arm it early. A failed
  # tag enumeration fails the gate: an error must never silently weaken
  # a release-enforcement threshold to advisory.
  if ! RELEASE_TAGS="$(git tag --list 'v*')"; then
    echo "FAIL: pkg-freeze — cannot enumerate release tags (git tag failed)" >&2
    exit 1
  fi
  if printf '%s\n' "$RELEASE_TAGS" | grep -qE '^v[1-9][0-9]*\.[0-9]+\.[0-9]+$'; then
    MODE=enforce
  else
    MODE=advisory
  fi
fi
case "$MODE" in
  advisory | enforce) ;;
  *) echo "check-pkg-freeze: unknown PKG_FREEZE_MODE='$MODE' (want advisory|enforce)" >&2; exit 2 ;;
esac

resolve_base_ref() {
  if [[ -n "${GITHUB_BASE_REF:-}" ]] && git rev-parse -q --verify "origin/$GITHUB_BASE_REF" > /dev/null; then
    echo "origin/$GITHUB_BASE_REF"
    return 0
  fi
  for ref in origin/feat/extensions-adr-0069 origin/main; do
    if git rev-parse -q --verify "$ref" > /dev/null; then
      echo "$ref"
      return 0
    fi
  done
  return 1
}

BASE_REF="${PKG_FREEZE_BASE:-$(resolve_base_ref || true)}"
if [[ -z "$BASE_REF" ]]; then
  # In CI a missing base ref is a broken checkout, never a skip; locally
  # it just means the remote was never fetched.
  if [[ "${CI:-}" = "true" ]]; then
    echo "FAIL: pkg-freeze — no base ref (origin/main missing); fetch-depth must cover the merge base" >&2
    exit 1
  fi
  echo "SKIP pkg-freeze: no origin ref found — fetch origin to arm the published-surface gate"
  exit 0
fi

BASE="$(git merge-base HEAD "$BASE_REF")"
BASE12="$(git rev-parse --short=12 "$BASE")"

if ! git ls-tree -d "$BASE" backend/pkg > /dev/null 2>&1 || [[ -z "$(git ls-tree -d "$BASE" backend/pkg)" ]]; then
  echo "OK: pkg-freeze ($MODE) — no published surface at $BASE_REF merge-base; nothing frozen yet"
  exit 0
fi

WORKTREE="$(mktemp -d "${TMPDIR:-/tmp}/pkg-freeze.XXXXXX")"
EXPORT_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pkg-freeze-export.XXXXXX")"
cleanup() {
  git worktree remove --force "$WORKTREE" > /dev/null 2>&1 || true
  rm -rf "$WORKTREE" "$EXPORT_DIR"
}
trap cleanup EXIT
git worktree add --detach --quiet "$WORKTREE" "$BASE"

# Fail-closed package discovery: a module or loading error must never
# read as "nothing frozen" — only the go tool's own "matched no
# packages" does.
golist_err="$(mktemp "${TMPDIR:-/tmp}/pkg-freeze-golist.XXXXXX")"
if ! OLD_PKGS="$(cd "$WORKTREE/backend" && go list ./pkg/... 2> "$golist_err")"; then
  if grep -q "matched no packages" "$golist_err"; then
    OLD_PKGS=""
  else
    echo "FAIL: pkg-freeze — cannot load the baseline's published packages:" >&2
    indent < "$golist_err" >&2
    rm -f "$golist_err"
    exit 1
  fi
fi
rm -f "$golist_err"
if [[ -z "$OLD_PKGS" ]]; then
  echo "OK: pkg-freeze ($MODE) — no published packages at the merge-base; nothing frozen yet"
  exit 0
fi

findings="$EXPORT_DIR/findings"
removals="$EXPORT_DIR/removals"
: > "$findings"
: > "$removals"
count=0
for pkg in $OLD_PKGS; do
  count=$((count + 1))
  rel="${pkg#github.com/gradionhq/margince/backend/}"
  if [[ ! -d "backend/$rel" ]]; then
    echo "$pkg: package removed" >> "$removals"
    continue
  fi
  export_file="$EXPORT_DIR/$(echo "$pkg" | tr '/' '_').export"
  (cd "$WORKTREE/backend" && $APIDIFF -w "$export_file" "./$rel")
  (cd backend && $APIDIFF -incompatible "$export_file" "./$rel") \
    | sed -e 's/^- //' -e "s|^|$pkg: |" >> "$findings"
done

if [[ "$MODE" = "advisory" ]]; then
  if [[ -s "$removals" ]] || [[ -s "$findings" ]]; then
    echo "ADVISORY: pkg-freeze — incompatible published-surface changes vs $BASE_REF (design-fluid pre-v1.0.0; these HARD-FAIL from the first v1 release tag):"
    cat "$removals" "$findings" | indent
  else
    echo "OK: pkg-freeze (advisory) — published surface additive-or-unchanged vs $BASE_REF ($count packages)"
  fi
  exit 0
fi

# enforce: the allowlist licenses ratified findings, baseline-bound.
ALLOWLIST="${PKG_FREEZE_ALLOWLIST:-scripts/pkg-freeze-allowlist.txt}"
allowed="$EXPORT_DIR/allowed"
expired="$EXPORT_DIR/expired"
: > "$allowed"
: > "$expired"
if [[ -f "$ALLOWLIST" ]]; then
  entries="$(awk '!/^[[:space:]]*(#|$)/' "$ALLOWLIST")"
  if [[ -n "$entries" ]]; then
    printf '%s\n' "$entries" | grep -E "^$BASE12 " | sed "s/^$BASE12 //" > "$allowed" || true
    printf '%s\n' "$entries" | grep -vE "^$BASE12 " > "$expired" || true
  fi
fi

failed=0
violations="$(grep -Fxv -f "$allowed" "$findings" || true)"
unused="$(grep -Fxv -f "$findings" "$allowed" || true)"

if [[ -s "$removals" ]]; then
  echo "FAIL: pkg-freeze — published package removed (never allowlistable; deprecate, then remove with its major cycle — ADR-0069 §3):" >&2
  indent < "$removals" >&2
  failed=1
fi
if [[ -n "$violations" ]]; then
  echo "FAIL: pkg-freeze — incompatible published-surface change vs $BASE_REF (merge-base $BASE12):" >&2
  echo "$violations" | indent >&2
  echo "A ratified change is recorded in $ALLOWLIST as this exact line (visible in the PR):" >&2
  echo "$violations" | sed "s/^/$BASE12 /" | indent >&2
  failed=1
fi
if [[ -s "$expired" ]]; then
  echo "WARN: pkg-freeze — allowlist entries bound to a superseded baseline (they can license nothing; remove them with the next allowlist edit):"
  indent < "$expired"
fi
if [[ -n "$unused" ]]; then
  echo "WARN: pkg-freeze — allowlist entries bound to the current baseline ($BASE12) match no finding (typo, or the change was reverted — remove them):"
  echo "$unused" | indent
fi

if [[ "$failed" -ne 0 ]]; then
  echo "pkg-freeze: the published surface changes additively or via versioned successors, never in place (EXT-P3)." >&2
  exit 1
fi
active="$(($(grep -c . "$allowed" || true) - $(echo "$unused" | grep -c . || true)))"
echo "OK: pkg-freeze (enforce) — published surface additive-or-unchanged vs $BASE_REF ($count packages, $active active exceptions)"
