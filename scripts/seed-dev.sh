#!/usr/bin/env bash
#
# seed-dev.sh — create/refresh the demo workspace through the public API.
#
# The seed is an API client, not a SQL fixture, on purpose: passwords
# are salted Argon2id, and every record write must commit domain row +
# audit_log + event_outbox in one transaction. A SQL fixture would
# duplicate all of that and silently drift from the schema; the API
# cannot.
#
# Pure client: the stack must already be running (`make dev`). Idempotent:
# a re-run logs in instead of re-bootstrapping, and re-creating a record
# that already exists answers 409 on its natural key (person email, org
# domain, deal name checked via list), which counts as "already seeded".
#
# Bootstrap happens at api boot from the deployment configuration
# (A107/ADR-0061: `make dev` writes .tmp/dev/*/margince.yaml with these
# demo credentials) — this script only signs in and seeds records.

set -euo pipefail

API_BASE="${API_BASE:-http://localhost:8080}"
WORKSPACE_SLUG="${WORKSPACE_SLUG:-demo-workspace}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@demo.test}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-demo-password-123}"

command -v jq >/dev/null 2>&1 || { echo "seed-dev: jq is required" >&2; exit 1; }

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

workdir="$(mktemp -d -t seed-dev.XXXXXX)"
trap 'rm -rf "$workdir"' EXIT

SESSION=""

# One installation serves one organization (A107/ADR-0061): the server
# resolves the tenant itself — no header selects it.
# A transport failure (refused, timeout) prints status 000 and must not
# trip set -e — the caller's status handling owns the error message.
api() { # api <method> <path> [json-body] — prints the HTTP status, body lands in $workdir/body
  local method="$1" path="$2" data="${3:-}"
  curl -sS --max-time 30 -o "$workdir/body" -D "$workdir/headers" -w '%{http_code}' \
    -X "$method" "$API_BASE/v1$path" \
    -H 'Content-Type: application/json' \
    ${SESSION:+--cookie "crm_session=$SESSION"} \
    ${data:+--data "$data"} || true
}

# The session cookie is Secure, which curl's jar refuses to replay over
# plain-http localhost — so pull the token out and send it explicitly.
capture_session() {
  SESSION="$(sed -n 's/^[Ss]et-[Cc]ookie: crm_session=\([^;]*\).*/\1/p' "$workdir/headers" | tr -d '\r')"
  [ -n "$SESSION" ] || fail "the server answered OK but set no crm_session cookie"
}

echo "== seed-dev: API reachability =="
curl -fsS --max-time 10 "$API_BASE/readyz" >/dev/null 2>&1 \
  || fail "$API_BASE/readyz is not answering — start the stack first (make dev)"
echo "  OK: $API_BASE is up"

echo "== seed-dev: demo workspace ($WORKSPACE_SLUG) =="
status="$(api POST /auth/login "$(jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email:$e,password:$p}')")"
case "$status" in
  200)
    capture_session
    echo "  OK: logged in as $ADMIN_EMAIL"
    ;;
  *)
    echo "  response body:" >&2
    cat "$workdir/body" >&2
    fail "login as $ADMIN_EMAIL returned HTTP $status — the api bootstraps the demo organization at boot from its margince.yaml (make dev writes it); if the credentials changed, reset the dev database and restart the stack"
    ;;
esac

# Demo records ride the same natural-key dedupe the product uses: a 201
# created it, a 409 means an earlier run did — anything else is a defect.
ensure() { # ensure <label> <path> <json-body>
  local label="$1" path="$2" data="$3" status
  status="$(api POST "$path" "$data")"
  case "$status" in
    201) echo "  OK: created $label" ;;
    409) echo "  OK: $label already present" ;;
    *)
      echo "  response body:" >&2
      cat "$workdir/body" >&2
      fail "POST /v1$path ($label) returned HTTP $status"
      ;;
  esac
}

echo "== seed-dev: demo people =="
ensure "person Alice Müller" /people \
  '{"full_name":"Alice Müller","emails":[{"email":"alice@demo.test","is_primary":true}],"source":"seed"}'
ensure "person Bob Schmidt" /people \
  '{"full_name":"Bob Schmidt","emails":[{"email":"bob@demo.test","is_primary":true}],"source":"seed"}'
ensure "person Carol Wagner" /people \
  '{"full_name":"Carol Wagner","emails":[{"email":"carol@demo.test","is_primary":true}],"source":"seed"}'

echo "== seed-dev: demo organization =="
ensure "organization Demo GmbH" /organizations \
  '{"display_name":"Demo GmbH","domains":[{"domain":"demo.test","is_primary":true}],"source":"seed"}'

echo "== seed-dev: demo deals =="
# Deals have no natural key, so idempotency is a name probe against the
# list before creating. Stages come from the bootstrap-seeded default
# pipeline ("Sales": Qualified → … → Won/Lost).
status="$(api GET /pipelines)"
[ "$status" = "200" ] || fail "GET /v1/pipelines returned HTTP $status"
pipeline_id="$(jq -r '.data[] | select(.is_default) | .id' "$workdir/body")"
[ -n "$pipeline_id" ] || fail "no default pipeline — the bootstrap seed did not run?"
stage_id_qualified="$(jq -r --arg p "$pipeline_id" '.data[] | select(.id == $p) | .stages[] | select(.name == "Qualified") | .id' "$workdir/body")"
stage_id_proposal="$(jq -r --arg p "$pipeline_id" '.data[] | select(.id == $p) | .stages[] | select(.name == "Proposal") | .id' "$workdir/body")"
[ -n "$stage_id_qualified" ] && [ -n "$stage_id_proposal" ] \
  || fail "the default pipeline is missing its seeded Qualified/Proposal stages"

status="$(api GET '/deals?limit=100')"
[ "$status" = "200" ] || fail "GET /v1/deals returned HTTP $status"
deals_page="$workdir/deals.json"
cp "$workdir/body" "$deals_page"

ensure_deal() { # ensure_deal <name> <stage-id> <amount-minor>
  local name="$1" stage="$2" amount="$3"
  if jq -e --arg n "$name" '.data[] | select(.name == $n)' "$deals_page" >/dev/null; then
    echo "  OK: deal $name already present"
    return
  fi
  ensure "deal $name" /deals "$(jq -n --arg n "$name" --arg p "$pipeline_id" --arg s "$stage" --argjson a "$amount" \
    '{name:$n,pipeline_id:$p,stage_id:$s,amount_minor:$a,currency:"EUR",source:"seed"}')"
}

ensure_deal "Acme Expansion" "$stage_id_qualified" 2500000
ensure_deal "Globex Renewal" "$stage_id_proposal" 1200000

echo ""
echo "seed-dev: DONE — log in at $API_BASE with $ADMIN_EMAIL / $ADMIN_PASSWORD (workspace $WORKSPACE_SLUG)"
