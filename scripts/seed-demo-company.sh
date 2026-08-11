#!/usr/bin/env bash
# seed-demo-company.sh — one company with every card on its record page filled.
#
# The company record page shows a card only when it has something true to say,
# so on a freshly imported account most of them correctly stay empty or absent.
# That is right, and it makes the page impossible to LOOK at: there is no way
# to see the design at full density without an account that has been running
# for a while.
#
# This builds that account. Everything with an API goes through the API — the
# deals, the contacts, the activities — because a hand-inserted row the real
# writer never produces proves nothing and drifts the moment the writer
# changes. Only the accounting CONNECTION is written directly, because there is
# no endpoint to connect a source yet; the invoices behind it are then produced
# by the real sync, not by this file.
#
# Idempotent: re-running adopts what an earlier run created.
#
# Usage:  scripts/seed-demo-company.sh [company-name]

set -euo pipefail

API_BASE="${API_BASE:-http://localhost:8080}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@demo.test}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-demo-password-123}"
COMPANY="${1:-Glazed Frog}"
PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-55432}"
PGUSER="${PGUSER:-margince_owner}"
PGDATABASE="${PGDATABASE:-margince}"
export PGPASSWORD="${PGPASSWORD:-dev}"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
SESSION=""

fail() { echo "seed-demo-company: $*" >&2; exit 1; }

api() { # api <method> <path> [json-body] — prints the status, body in $workdir/body
  local method="$1" path="$2" data="${3:-}"
  curl -sS --max-time 30 -o "$workdir/body" -D "$workdir/headers" -w '%{http_code}' \
    -X "$method" "$API_BASE/v1$path" \
    -H 'Content-Type: application/json' \
    ${SESSION:+--cookie "crm_session=$SESSION"} \
    ${data:+--data "$data"} || true
}

# -q so an INSERT ... RETURNING yields the value alone: without it psql also
# prints its "INSERT 0 1" status line, and the caller captures both as one id.
psql_one() { psql -q -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -tAc "$1"; }

curl -fsS --max-time 10 "$API_BASE/readyz" >/dev/null 2>&1 \
  || fail "$API_BASE/readyz is not answering — start the stack first (make dev)"

status="$(api POST /auth/login "$(jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email:$e,password:$p}')")"
[ "$status" = "200" ] || fail "login returned HTTP $status"
SESSION="$(sed -n 's/^[Ss]et-[Cc]ookie: crm_session=\([^;]*\).*/\1/p' "$workdir/headers" | tr -d '\r')"
[ -n "$SESSION" ] || fail "the server answered OK but set no crm_session cookie"
echo "  OK: logged in as $ADMIN_EMAIL"

# ---- the company -----------------------------------------------------------
# A CUSTOMER, because that is the lifecycle the money readings are for: the
# finance section is absent by contract on a target or a prospect (FIN-AC-3),
# so a demo account that is not one cannot show the cards this exists to show.
echo "== the company =="
org_id="$(psql_one "SELECT id FROM organization WHERE display_name = '$COMPANY' AND archived_at IS NULL LIMIT 1")"
if [ -z "$org_id" ]; then
  status="$(api POST /organizations "$(jq -n --arg n "$COMPANY" '{
    display_name: $n,
    lifecycle: "customer",
    website: "https://glazedfrog.example",
    description: "Supplies architectural glazing, aluminium systems and modular walls to builders, architects and developers.",
    employee_band: "51_200",
    source: "manual"
  }')")"
  [ "$status" = "201" ] || { cat "$workdir/body" >&2; fail "creating $COMPANY returned HTTP $status"; }
  org_id="$(jq -r .id < "$workdir/body")"
  echo "  OK: created $COMPANY"
else
  echo "  OK: $COMPANY already present"
fi
echo "  $COMPANY = $org_id"

# ---- the people ------------------------------------------------------------
# Three named roles, because the relationship-coverage card reads role gaps and
# the Today card's "best route" ranks contacts by strength. One contact makes
# both of them say nothing interesting.
echo "== the people =="
person_id() { psql_one "SELECT id FROM person WHERE full_name = '$1' AND archived_at IS NULL LIMIT 1"; }

ensure_person() { # ensure_person <name> <email> <title>
  local name="$1" email="$2" title="$3" existing status
  existing="$(person_id "$name")"
  if [ -n "$existing" ]; then
    # stderr, not stdout: stdout IS the id this function returns, and a
    # progress line on it becomes part of the value the caller captures.
    echo "  OK: $name already present" >&2
    printf '%s' "$existing"
    return
  fi
  status="$(api POST /people "$(jq -n --arg n "$name" --arg e "$email" --arg t "$title" --arg o "$org_id" '{
    full_name: $n,
    job_title: $t,
    emails: [{email: $e, is_primary: true}],
    organization_id: $o,
    source: "manual"
  }')")"
  [ "$status" = "201" ] || { cat "$workdir/body" >&2; fail "creating $name returned HTTP $status"; }
  echo "  OK: created $name" >&2
  jq -r .id < "$workdir/body"
}

sarah_id="$(ensure_person "Sarah Cole" "sarah@glazedfrog.example" "CFO")"
nick_id="$(ensure_person "Nick Oettinger" "nick@glazedfrog.example" "Head of Operations")"
mark_id="$(ensure_person "Mark Hughes" "mark@glazedfrog.example" "Operations Manager")"

# ---- the deals -------------------------------------------------------------
# Two open, so the commercial card has a leading deal to name and the KPI row
# has a pipeline to total. Both in EUR: a mixed-currency account deliberately
# refuses to rank its deals, which is correct and shows nothing.
echo "== the deals =="
status="$(api GET /pipelines)"
[ "$status" = "200" ] || fail "GET /v1/pipelines returned HTTP $status"
pipeline_id="$(jq -r '.data[] | select(.is_default) | .id' "$workdir/body")"
stage_proposal="$(jq -r --arg p "$pipeline_id" '.data[] | select(.id == $p) | .stages[] | select(.name == "Proposal") | .id' "$workdir/body")"
stage_qualified="$(jq -r --arg p "$pipeline_id" '.data[] | select(.id == $p) | .stages[] | select(.name == "Qualified") | .id' "$workdir/body")"
[ -n "$stage_proposal" ] && [ -n "$stage_qualified" ] || fail "the default pipeline has no Proposal/Qualified stage"

ensure_deal() { # ensure_deal <name> <stage-id> <amount-minor> <close-date>
  local name="$1" stage="$2" amount="$3" closes="$4" status
  if [ -n "$(psql_one "SELECT id FROM deal WHERE name = '$name' AND archived_at IS NULL LIMIT 1")" ]; then
    echo "  OK: deal $name already present"
    return
  fi
  status="$(api POST /deals "$(jq -n --arg n "$name" --arg p "$pipeline_id" --arg s "$stage" \
    --arg o "$org_id" --arg c "$closes" --argjson a "$amount" '{
      name: $n, pipeline_id: $p, stage_id: $s, organization_id: $o,
      amount_minor: $a, currency: "EUR", expected_close_date: $c, source: "manual"
    }')")"
  [ "$status" = "201" ] || { cat "$workdir/body" >&2; fail "creating deal $name returned HTTP $status"; }
  echo "  OK: created deal $name"
}

ensure_deal "Expansion Phase 2" "$stage_proposal" 9500000 "2026-11-30"
ensure_deal "Facade retrofit programme" "$stage_qualified" 4200000 "2027-02-15"

# ---- the history -----------------------------------------------------------
# A conversation in both directions over several months, plus a booked meeting
# and an open task. Half the Today card reads off these: whose move it is, the
# next commitment, the next meeting, and how long the economic buyer has been
# quiet.
echo "== the history =="
ensure_activity() { # ensure_activity <source-id> <json-body>
  local key="$1" body="$2" status
  if [ -n "$(psql_one "SELECT id FROM activity WHERE source_id = '$key' AND archived_at IS NULL LIMIT 1")" ]; then
    echo "  OK: $key already present"
    return
  fi
  status="$(api POST /activities "$body")"
  [ "$status" = "201" ] || { cat "$workdir/body" >&2; fail "creating activity $key returned HTTP $status"; }
  echo "  OK: created $key"
}

mail() { # mail <key> <subject> <direction> <when> <person-id>
  ensure_activity "$1" "$(jq -n --arg s "$2" --arg d "$3" --arg w "$4" --arg k "$1" \
    --arg o "$org_id" --arg p "$5" '{
      kind: "email", subject: $s, direction: $d, occurred_at: $w,
      source: "manual", source_system: "demo-seed", source_id: $k,
      links: [{entity_type: "organization", entity_id: $o},
              {entity_type: "person", entity_id: $p}]
    }')"
}

mail demo-gf-1 "Expansion scope — first pass"        outbound "2026-04-14T09:12:00Z" "$sarah_id"
mail demo-gf-2 "Re: Expansion scope — first pass"    inbound  "2026-04-16T15:40:00Z" "$sarah_id"
mail demo-gf-3 "Implementation capacity"             inbound  "2026-06-02T08:05:00Z" "$sarah_id"
mail demo-gf-4 "Re: Implementation capacity"         outbound "2026-06-03T11:20:00Z" "$sarah_id"
mail demo-gf-5 "Phased rollout — revised proposal"   outbound "2026-07-21T16:30:00Z" "$sarah_id"
mail demo-gf-6 "Operations handover questions"       inbound  "2026-07-28T10:15:00Z" "$nick_id"

# A meeting on the books, so the Today card has one to prepare for.
ensure_activity demo-gf-meeting "$(jq -n --arg o "$org_id" --arg p "$sarah_id" '{
  kind: "meeting", subject: "Executive alignment", meeting_status: "booked",
  occurred_at: "2026-08-12T09:00:00Z", duration_seconds: 1800,
  source: "manual", source_system: "demo-seed", source_id: "demo-gf-meeting",
  links: [{entity_type: "organization", entity_id: $o},
          {entity_type: "person", entity_id: $p}]
}')"

# An open commitment with a due date — the Today card's "next commitment".
ensure_activity demo-gf-task "$(jq -n --arg o "$org_id" '{
  kind: "task", subject: "Send revised expansion proposal",
  occurred_at: "2026-08-05T08:00:00Z", due_at: "2026-08-14T16:00:00Z",
  source: "manual", source_system: "demo-seed", source_id: "demo-gf-task",
  links: [{entity_type: "organization", entity_id: $o}]
}')"

# ---- the accounting source -------------------------------------------------
# Written to the database, and this is the ONE thing here that is: there is no
# endpoint to connect an accounting source yet. What it connects is the offline
# generator, so the INVOICES are not written here — the real sync produces
# them, at the real write shape, and a hand-inserted ledger would drift from
# the writer the moment the mirror changes.
echo "== the accounting source =="
ws_id="$(psql_one "SELECT workspace_id FROM organization WHERE id = '$org_id'")"
conn_id="$(psql_one "SELECT id FROM finance_connection WHERE archived_at IS NULL AND status <> 'disconnected' LIMIT 1")"
if [ -z "$conn_id" ]; then
  conn_id="$(psql_one "INSERT INTO finance_connection
      (workspace_id, provider, status, credential_ref, source, captured_by)
    VALUES ('$ws_id', 'offline_demo', 'active', 'offline://demo', 'system', 'system:seed')
    RETURNING id")"
  echo "  OK: connected the offline demo source"
else
  echo "  OK: an accounting source is already connected"
fi

if [ -z "$(psql_one "SELECT 1 FROM finance_customer_link WHERE organization_id = '$org_id' AND archived_at IS NULL")" ]; then
  psql_one "INSERT INTO finance_customer_link
      (workspace_id, connection_id, organization_id, external_customer_id,
       sync_hash, source, captured_by)
    VALUES ('$ws_id', '$conn_id', '$org_id', 'GLAZED-FROG', 'seed', 'system', 'system:seed')" >/dev/null
  echo "  OK: matched $COMPANY to a customer in the source"
else
  echo "  OK: $COMPANY is already matched"
fi

echo ""
echo "seed-demo-company: DONE"
echo "  $COMPANY — $API_BASE/#/companies/$org_id"
echo ""
echo "  The invoices arrive with the next finance sync, which runs on boot."
echo "  Restart the stack (make dev) to pull them in now."
