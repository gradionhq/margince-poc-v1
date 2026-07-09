#!/usr/bin/env bash
#
# verify-boot.sh — the scripted boot proof: a running stack really serves
# a login, the seeded demo data, and a buildable frontend.
#
# Pure client by design: the caller owns the stack lifecycle (`make dev`
# in one terminal, `make seed-dev` once) the same way CI's live-boot job
# does — this script only proves the result. It fails loudly on the first
# broken step and never skips a check: a green run means a human can log
# in and see data right now.
#
# Steps:
#   1. POST /v1/auth/login with the seeded demo admin → 200 + crm_session.
#   2. GET /v1/people under that session → the three seeded people.
#   3. The frontend production build compiles (pnpm build) — a real
#      compile+bundle, not a stale-dist check.

set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_BASE="${API_BASE:-http://localhost:8080}"
WORKSPACE_SLUG="${WORKSPACE_SLUG:-demo-workspace}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@demo.test}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-demo-password-123}"

command -v jq >/dev/null 2>&1 || { echo "verify-boot: jq is required" >&2; exit 1; }

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

workdir="$(mktemp -d -t verify-boot.XXXXXX)"
trap 'rm -rf "$workdir"' EXIT

echo "== verify-boot 1/3: login as the seeded demo admin =="
login_status="$(curl -sS -o "$workdir/login.json" -D "$workdir/headers" -w '%{http_code}' \
  -X POST "$API_BASE/v1/auth/login" \
  -H "X-Workspace-Slug: $WORKSPACE_SLUG" \
  -H 'Content-Type: application/json' \
  --data "$(jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email:$e,password:$p}')")"
if [ "$login_status" != "200" ]; then
  echo "  response body:" >&2
  cat "$workdir/login.json" >&2
  fail "POST /v1/auth/login returned HTTP $login_status (expected 200). Is the stack up and seeded? (make dev, then make seed-dev)"
fi
# The cookie is Secure, which curl's jar refuses to replay over plain-http
# localhost — extract the token and send it explicitly.
session="$(sed -n 's/^[Ss]et-[Cc]ookie: crm_session=\([^;]*\).*/\1/p' "$workdir/headers" | tr -d '\r')"
[ -n "$session" ] || fail "login answered 200 but set no crm_session cookie"
echo "  OK: logged in as $ADMIN_EMAIL, session captured"

echo "== verify-boot 2/3: seeded people are visible =="
people_status="$(curl -sS -o "$workdir/people.json" -w '%{http_code}' \
  "$API_BASE/v1/people?limit=100" \
  -H "X-Workspace-Slug: $WORKSPACE_SLUG" \
  --cookie "crm_session=$session")"
if [ "$people_status" != "200" ]; then
  echo "  response body:" >&2
  cat "$workdir/people.json" >&2
  fail "GET /v1/people returned HTTP $people_status (expected 200)"
fi
for name in "Alice Müller" "Bob Schmidt" "Carol Wagner"; do
  if ! jq -e --arg n "$name" '.data[] | select(.full_name == $n)' "$workdir/people.json" >/dev/null; then
    echo "  full /v1/people response:" >&2
    cat "$workdir/people.json" >&2
    fail "seeded person '$name' missing from GET /v1/people — seed absent or stale (make seed-dev)"
  fi
  echo "  OK: found '$name'"
done

echo "== verify-boot 3/3: frontend production build =="
if ! (cd "$REPO_DIR/frontend" && pnpm install --frozen-lockfile && pnpm build); then
  fail "the frontend production build failed — see output above"
fi
echo "  OK: frontend builds"

echo ""
echo "verify-boot: ALL CHECKS GREEN"
