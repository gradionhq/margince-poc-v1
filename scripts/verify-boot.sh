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
#   3. Every composed unit's declared channel transport is published by
#      GET /v1/channel-providers — the boot step really ran.
#   4. The frontend production build compiles (pnpm build) — a real
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

# A transport failure (refused, timeout) makes curl print status 000 and
# exit non-zero; `|| true` keeps set -e from eating the fail() message,
# and --max-time keeps a stalled API from hanging the proof.
echo "== verify-boot 1/4: login as the seeded demo admin =="
login_status="$(curl -sS --max-time 15 -o "$workdir/login.json" -D "$workdir/headers" -w '%{http_code}' \
  -X POST "$API_BASE/v1/auth/login" \
  -H "X-Workspace-Slug: $WORKSPACE_SLUG" \
  -H 'Content-Type: application/json' \
  --data "$(jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email:$e,password:$p}')" || true)"
if [ -z "$login_status" ] || [ "$login_status" = "000" ]; then
  fail "could not reach $API_BASE — is the stack up? (make dev)"
fi
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

echo "== verify-boot 2/4: seeded people are visible =="
people_status="$(curl -sS --max-time 15 -o "$workdir/people.json" -w '%{http_code}' \
  "$API_BASE/v1/people?limit=100" \
  -H "X-Workspace-Slug: $WORKSPACE_SLUG" \
  --cookie "crm_session=$session" || true)"
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

echo "== verify-boot 3/4: every composed unit's transport is registered =="
# The boot proof for the channel registry, and it belongs HERE rather than in a
# Go test: registering the vocabulary is a BOOT STEP the api runs, so the only
# thing that can prove the api runs it is an api that booted. This lane boots
# with no MARGINCE_KEYVAULT_ROOT_KEY, which is exactly the install on which the
# write used to be skipped entirely — a unit's transport went unregistered, and
# its captured messages then failed activity's channel_provider foreign key.
#
# The expectation is DERIVED from the composed manifests rather than listed
# here, so a unit that starts (or stops) declaring a channel needs no edit to
# this script, and a list that quietly went stale cannot report success.
expected_transports="$(jq -r '.channels // [] | .[].provider' "$REPO_DIR"/extensions/*/manifest.generated.json | sort -u)"
providers_status="$(curl -sS --max-time 15 -o "$workdir/providers.json" -w '%{http_code}' \
  "$API_BASE/v1/channel-providers" \
  -H "X-Workspace-Slug: $WORKSPACE_SLUG" \
  --cookie "crm_session=$session" || true)"
if [ "$providers_status" != "200" ]; then
  echo "  response body:" >&2
  cat "$workdir/providers.json" >&2
  fail "GET /v1/channel-providers returned HTTP $providers_status (expected 200)"
fi
if [ -z "$expected_transports" ]; then
  echo "  OK: no composed unit declares a channel, nothing to register"
else
  while read -r provider; do
    if ! jq -e --arg p "$provider" '.data[] | select(.provider == $p)' "$workdir/providers.json" >/dev/null; then
      echo "  full /v1/channel-providers response:" >&2
      cat "$workdir/providers.json" >&2
      fail "a composed unit declares transport '$provider', but the running api does not publish it — the boot step that registers the channel vocabulary did not run"
    fi
    echo "  OK: '$provider' is registered"
  done <<<"$expected_transports"
fi

echo "== verify-boot 4/4: frontend production build =="
# --ignore-scripts: no lifecycle scripts run at install; esbuild ships
# prebuilt platform binaries as optional deps, so the build works without
# its validating postinstall.
if ! (cd "$REPO_DIR/frontend" && pnpm install --frozen-lockfile --ignore-scripts && pnpm build); then
  fail "the frontend production build failed — see output above"
fi
echo "  OK: frontend builds"

echo ""
echo "verify-boot: ALL CHECKS GREEN"
