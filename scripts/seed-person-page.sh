#!/usr/bin/env bash
# The person record page's demo account, seeded through the API.
#
# It is an API client for the same reason seed-dev.sh is: every record write
# must commit domain row + audit_log + event_outbox in one transaction, and
# captured_by is stamped from the authenticated principal. A SQL fixture would
# duplicate all of that and drift from the schema — and a page filled by rows
# the real writer never produces proves nothing about the real writer.
#
# It seeds ONE account in two states, which are the two mockups:
#
#   Sarah Cole  (active, meeting ahead)  — State A
#   Nadia Ferreira (gone quiet)          — State B
#
# Both hang off the same company so the buying committee has somebody in it.
# Idempotent: a re-run reports what is already present and changes nothing.

set -euo pipefail

API_BASE="${API_BASE:-http://localhost:18080}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@demo.test}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-demo-password-123}"

command -v jq >/dev/null 2>&1 || { echo "seed-person-page: jq is required" >&2; exit 1; }

fail() { echo "FAIL: $1" >&2; exit 1; }

workdir="$(mktemp -d -t seed-person.XXXXXX)"
trap 'rm -rf "$workdir"' EXIT
SESSION=""

api() { # api <method> <path> [json-body] — prints the HTTP status, body in $workdir/body
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

echo "== seed-person-page: sign in =="
status="$(api POST /auth/login "$(jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" \
  '{email:$e,password:$p}')")"
[ "$status" = "200" ] || fail "login as $ADMIN_EMAIL returned HTTP $status (is the stack up? make dev)"
capture_session

# ---------------------------------------------------------------------------
# The company both contacts work at.
# ---------------------------------------------------------------------------

echo "== seed-person-page: the account =="
org_id=""
status="$(api GET '/organizations?q=Glazed%20Frog&limit=10')"
[ "$status" = "200" ] || fail "GET /v1/organizations returned HTTP $status"
org_id="$(jq -r '.data[] | select(.display_name == "Glazed Frog") | .id' "$workdir/body" | head -1)"
if [ -z "$org_id" ]; then
  status="$(api POST /organizations '{"display_name":"Glazed Frog","domains":[{"domain":"glazedfrog.com","is_primary":true}],"source":"seed"}')"
  [ "$status" = "201" ] || { cat "$workdir/body" >&2; fail "create organization returned HTTP $status"; }
  org_id="$(jq -r .id "$workdir/body")"
  echo "  OK: created Glazed Frog"
else
  echo "  OK: Glazed Frog already present"
fi

# ---------------------------------------------------------------------------
# People. person_ref echoes the id whether we just made them or not, so a
# re-run threads the same records through everything below.
# ---------------------------------------------------------------------------

ensure_person() { # ensure_person <full_name> <title> <email> — prints the id
  local name="$1" title="$2" email="$3" id status
  status="$(api GET "/people?q=$(printf '%s' "$name" | jq -sRr @uri)&limit=10")"
  [ "$status" = "200" ] || fail "GET /v1/people returned HTTP $status"
  id="$(jq -r --arg n "$name" '.data[] | select(.full_name == $n) | .id' "$workdir/body" | head -1)"
  if [ -n "$id" ]; then
    echo "$id"
    return
  fi
  status="$(api POST /people "$(jq -n --arg n "$name" --arg t "$title" --arg e "$email" \
    '{full_name:$n,title:$t,emails:[{email:$e,is_primary:true}],source:"seed"}')")"
  [ "$status" = "201" ] || { cat "$workdir/body" >&2; fail "create person $name returned HTTP $status"; }
  jq -r .id "$workdir/body"
}

echo "== seed-person-page: the people =="
sarah_id="$(ensure_person "Sarah Cole" "CFO" "sarah@glazedfrog.com")"
echo "  Sarah Cole: $sarah_id"
nadia_id="$(ensure_person "Nadia Ferreira" "VP Operations" "nadia@glazedfrog.com")"
echo "  Nadia Ferreira: $nadia_id"
nick_id="$(ensure_person "Nick Oettinger" "Head of Finance Systems" "nick@glazedfrog.com")"
echo "  Nick Oettinger: $nick_id"
mark_id="$(ensure_person "Mark Hughes" "Director of Operations" "mark@glazedfrog.com")"
echo "  Mark Hughes: $mark_id"

# ---------------------------------------------------------------------------
# Employment. The page's header reads the employer off the current-primary
# relationship, not off a denormalized string.
# ---------------------------------------------------------------------------

echo "== seed-person-page: employment =="
employ() { # employ <person-id> <role>
  local person="$1" role="$2" status
  status="$(api POST /relationships "$(jq -n --arg p "$person" --arg o "$org_id" --arg r "$role" \
    '{kind:"employment",person_id:$p,organization_id:$o,role:$r,is_current_primary:true,source:"seed"}')")"
  case "$status" in
    201) echo "  OK: employed $role" ;;
    409) echo "  OK: employment already present" ;;
    *) cat "$workdir/body" >&2; fail "employment returned HTTP $status" ;;
  esac
}
employ "$sarah_id" "CFO"
employ "$nadia_id" "VP Operations"
employ "$nick_id" "Head of Finance Systems"
employ "$mark_id" "Director of Operations"

# ---------------------------------------------------------------------------
# The open deal, and the seats on it. €95k · Proposal · closes 30 Jun is what
# both mockups render.
# ---------------------------------------------------------------------------

echo "== seed-person-page: the deal =="
status="$(api GET /pipelines)"
[ "$status" = "200" ] || fail "GET /v1/pipelines returned HTTP $status"
pipeline_id="$(jq -r '.data[] | select(.is_default) | .id' "$workdir/body")"
stage_proposal="$(jq -r --arg p "$pipeline_id" \
  '.data[] | select(.id == $p) | .stages[] | select(.name == "Proposal") | .id' "$workdir/body")"
[ -n "$stage_proposal" ] || fail "the default pipeline has no Proposal stage"

# The mockups show "closes 30 Jun", but an open deal may not claim a close date
# in the past and this seed has to keep working as time passes. The nearest
# 30 June that is still ahead of today carries the mockup's figure honestly.
close_date="$(date -u '+%Y')-06-30"
if [ "$close_date" \< "$(date -u '+%Y-%m-%d')" ]; then
  close_date="$(( $(date -u '+%Y') + 1 ))-06-30"
fi

status="$(api GET '/deals?limit=100')"
[ "$status" = "200" ] || fail "GET /v1/deals returned HTTP $status"
deal_id="$(jq -r '.data[] | select(.name == "Expansion — Phase 2") | .id' "$workdir/body" | head -1)"
if [ -z "$deal_id" ]; then
  status="$(api POST /deals "$(jq -n --arg p "$pipeline_id" --arg s "$stage_proposal" --arg o "$org_id" --arg c "$close_date" \
    '{name:"Expansion — Phase 2",pipeline_id:$p,stage_id:$s,organization_id:$o,
      amount_minor:9500000,currency:"EUR",expected_close_date:$c,source:"seed"}')")"
  [ "$status" = "201" ] || { cat "$workdir/body" >&2; fail "create deal returned HTTP $status"; }
  deal_id="$(jq -r .id "$workdir/body")"
  echo "  OK: created Expansion — Phase 2 (€95k, Proposal, closes $close_date)"
else
  echo "  OK: Expansion — Phase 2 already present"
fi

echo "== seed-person-page: the buying committee =="
seat() { # seat <person-id> <role> <label>
  local person="$1" role="$2" label="$3" status
  status="$(api POST /relationships "$(jq -n --arg p "$person" --arg d "$deal_id" --arg r "$role" \
    '{kind:"deal_stakeholder",person_id:$p,deal_id:$d,role:$r,source:"seed"}')")"
  case "$status" in
    201) echo "  OK: $label seated as $role" ;;
    409) echo "  OK: $label already seated" ;;
    *) cat "$workdir/body" >&2; fail "seating $label returned HTTP $status" ;;
  esac
}
seat "$sarah_id" "economic_buyer" "Sarah"
seat "$nadia_id" "economic_buyer" "Nadia"
seat "$nick_id" "champion" "Nick"
seat "$mark_id" "user" "Mark"

# ---------------------------------------------------------------------------
# Correspondence. The dates are what the moment ladder reads: Sarah replied
# recently and has a meeting inside 72 hours (State A); Nadia's last inbound
# is 16 days old with our follow-up 9 days ago (State B, gone quiet).
# ---------------------------------------------------------------------------

echo "== seed-person-page: correspondence =="
# Dates are computed relative to today so the states stay true whenever this
# runs — a fixed date would put every moment in the past within a week.
day() { date -u -v-"$1"d '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -d "$1 days ago" '+%Y-%m-%dT%H:%M:%SZ'; }
ahead() { date -u -v+"$1"H '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -d "+$1 hours" '+%Y-%m-%dT%H:%M:%SZ'; }

# direction is "" for a meeting or a note: the activity CHECK admits inbound,
# outbound or nothing, and a meeting is not a direction of travel.
log_activity() { # log_activity <person-id> <kind> <direction|""> <subject> <body> <when> <label>
  local person="$1" kind="$2" direction="$3" subject="$4" body="$5" when="$6" label="$7" status
  status="$(api GET "/activities?entity_type=person&entity_id=$person&limit=50")"
  [ "$status" = "200" ] || fail "GET /v1/activities returned HTTP $status"
  if jq -e --arg s "$subject" '.data[] | select(.subject == $s)' "$workdir/body" >/dev/null; then
    echo "  OK: \"$label\" already captured"
    return
  fi
  status="$(api POST /activities "$(jq -n --arg k "$kind" --arg d "$direction" --arg s "$subject" \
    --arg b "$body" --arg o "$when" --arg p "$person" \
    '{kind:$k,subject:$s,body:$b,occurred_at:$o,
      links:[{entity_type:"person",entity_id:$p}],source:"seed"}
     + (if $d == "" then {} else {direction:$d} end)')")"
  [ "$status" = "201" ] || { cat "$workdir/body" >&2; fail "capture \"$label\" returned HTTP $status"; }
  echo "  OK: captured \"$label\""
}

# --- Sarah: State A. Two-way, 11 in / 13 out, last inbound 2 days ago. ---
log_activity "$sarah_id" email inbound "Re: ROI discussion" \
  "Wants the ROI model with 18-month payback and sensitivity analysis. Also flagged change-management capacity as a risk." \
  "$(day 2)" "Sarah replied to ROI discussion"
log_activity "$sarah_id" email outbound "Revised offer" \
  "Sent the revised offer with the phased rollout option." \
  "$(day 6)" "You sent revised offer"
log_activity "$sarah_id" meeting "" "Expansion workshop" \
  "Ran the expansion workshop with Sarah, Nick and Mark." \
  "$(day 9)" "Expansion workshop"

# The meeting inside 72 hours: this is what makes State A's Today card the
# meeting-prep rung rather than anything below it.
log_activity "$sarah_id" meeting "" "Expansion review" \
  "Wednesday's expansion meeting." "$(ahead 30)" "Wednesday's expansion meeting"

# --- Nadia: State B. One-sided, went quiet 16 days ago. ---
log_activity "$nadia_id" email inbound "Budget questions" \
  "She replied asking who owns the budget and whether the expansion can be phased." \
  "$(day 16)" "Nadia asked about budget"
log_activity "$nadia_id" email outbound "Following up on capacity" \
  "Followed up on the capacity question and the rollout sequence." \
  "$(day 9)" "Your follow-up"
log_activity "$nadia_id" email outbound "Proposal shared" \
  "Shared the proposal with the phased rollout." \
  "$(day 18)" "Proposal shared"

# ---------------------------------------------------------------------------
# What was said. These go through the production claims writer, not SQL: the
# commitments card and the what-matters card read this store, and filling them
# any other way would prove nothing about the writer that will fill them for
# real (the extraction task, issue #849).
#
# Every claim cites the activity it was read from and quotes it verbatim. The
# writer refuses one that does not, which is the invariant rather than this
# script's good manners.
# ---------------------------------------------------------------------------

echo "== seed-person-page: what was said =="
activity_id() { # activity_id <person-id> <subject> — prints the id, empty if absent
  local person="$1" subject="$2" status
  status="$(api GET "/activities?entity_type=person&entity_id=$person&limit=50")"
  [ "$status" = "200" ] || fail "GET /v1/activities returned HTTP $status"
  jq -r --arg s "$subject" '.data[] | select(.subject == $s) | .id' "$workdir/body" | head -1
}

claim() { # claim <person-id> <kind> <body> <source-subject> <quote> [due-at]
  local person="$1" kind="$2" body="$3" subject="$4" quote="$5" due="${6:-}" source status
  source="$(activity_id "$person" "$subject")"
  [ -n "$source" ] || fail "no captured activity titled \"$subject\" to ground this claim on"

  status="$(api GET "/people/$person/360")"
  [ "$status" = "200" ] || fail "GET /v1/people/$person/360 returned HTTP $status"
  if jq -e --arg b "$body" '(.claims // [])[] | select(.body == $b)' "$workdir/body" >/dev/null; then
    echo "  OK: \"$body\" already recorded"
    return
  fi

  status="$(api POST "/people/$person/claims" "$(jq -n --arg k "$kind" --arg b "$body" \
    --arg a "$source" --arg q "$quote" --arg d "$due" \
    '{kind:$k,body:$b,source_activity_id:$a,source_quote:$q}
     + (if $d == "" then {} else {due_at:$d} end)')")"
  [ "$status" = "201" ] || { cat "$workdir/body" >&2; fail "record claim returned HTTP $status"; }
  echo "  OK: recorded \"$body\""
}

# Sarah: what she cares about, and the loops that are open.
claim "$sarah_id" priority "18-month payback" "Re: ROI discussion" \
  "Wants the ROI model with 18-month payback and sensitivity analysis."
claim "$sarah_id" objection "Change capacity" "Re: ROI discussion" \
  "Also flagged change-management capacity as a risk."
claim "$sarah_id" success_criterion "Phased rollout without a delivery gap" "Expansion workshop" \
  "Ran the expansion workshop with Sarah, Nick and Mark."
claim "$sarah_id" commitment_ours "send revised ROI model" "Re: ROI discussion" \
  "Wants the ROI model with 18-month payback and sensitivity analysis." \
  "$(ahead 24)"
claim "$sarah_id" commitment_theirs "confirm finance attendees" "Expansion workshop" \
  "Ran the expansion workshop with Sarah, Nick and Mark."
claim "$sarah_id" open_question "implementation capacity" "Re: ROI discussion" \
  "Also flagged change-management capacity as a risk."

# Nadia: the gone-quiet account. Her commitment of ours is OVERDUE, which is
# what the mockup's amber "overdue 5 days" row renders.
claim "$nadia_id" priority "Phased budget approval" "Budget questions" \
  "She replied asking who owns the budget and whether the expansion can be phased."
claim "$nadia_id" objection "Change capacity" "Following up on capacity" \
  "Followed up on the capacity question and the rollout sequence."
claim "$nadia_id" commitment_ours "answer capacity question" "Budget questions" \
  "She replied asking who owns the budget and whether the expansion can be phased." \
  "$(day 5)"
claim "$nadia_id" commitment_theirs "confirm budget owner" "Budget questions" \
  "She replied asking who owns the budget and whether the expansion can be phased."
claim "$nadia_id" open_question "revised rollout sequence" "Following up on capacity" \
  "Followed up on the capacity question and the rollout sequence."

echo ""
echo "seed-person-page: DONE"
echo "  State A (active, meeting ahead): $API_BASE/#/contacts/$sarah_id"
echo "  State B (gone quiet):            $API_BASE/#/contacts/$nadia_id"
