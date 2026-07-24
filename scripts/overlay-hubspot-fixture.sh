#!/usr/bin/env bash
# overlay-hubspot-fixture.sh — seed / reset a deterministic HubSpot CRM fixture
# for LOCAL overlay testing against a HubSpot *developer test account*.
#
# Why this exists: the overlay mirror only shows a record whose HubSpot OWNER's
# email matches a margince user's email (the auto owner→user mapping on connect;
# there is no admin-sees-all bypass). So every fixture record here is assigned to
# the test account's own owner — set your local config/margince.yaml admin email
# to THAT owner's email (printed by `whoami`/`seed`) and the records become
# visible after you connect + reconcile. See docs/how-to/test-overlay-locally.md.
#
# SAFETY: only ever creates and deletes records carrying the fixture MARKERS
# below (fixture-email domain / "[fixture]" name prefix). `reset` archives ONLY
# marker-matched records — it never touches your other data. Point this at a
# HubSpot developer TEST account (which cannot sync to production), never a live
# portal.
#
# Usage:
#   HUBSPOT_TOKEN=pat-xxxx scripts/overlay-hubspot-fixture.sh whoami
#   HUBSPOT_TOKEN=pat-xxxx scripts/overlay-hubspot-fixture.sh seed     # reset-first, then create
#   HUBSPOT_TOKEN=pat-xxxx scripts/overlay-hubspot-fixture.sh reset    # archive fixtures only
#
# Requires: bash, curl, jq. Token = a private-app token with CRM read+write on
# contacts/companies/deals (+ leads, owners). See the docs for the scope list.
set -euo pipefail

BASE="https://api.hubapi.com"
# Token from HUBSPOT_TOKEN (then the subcommand is $1), or as the first arg
# (then the subcommand is $2): `HUBSPOT_TOKEN=pat- script seed` or `script pat- seed`.
if [ -n "${HUBSPOT_TOKEN:-}" ]; then
  TOKEN="${HUBSPOT_TOKEN}"; SUBCMD="${1:-}"
else
  TOKEN="${1:-}"; SUBCMD="${2:-}"
fi
# Markers that identify OUR fixture records (and nothing else).
EMAIL_DOMAIN="overlay-fixture.test"     # contact emails end with @overlay-fixture.test
NAME_PREFIX="[fixture] "                # company/deal/lead names start with this

if [ -z "${TOKEN}" ]; then
  echo "error: set HUBSPOT_TOKEN=pat-... (a HubSpot private-app token)" >&2
  exit 2
fi
for bin in curl jq; do
  command -v "$bin" >/dev/null 2>&1 || { echo "error: $bin is required" >&2; exit 2; }
done

# hs METHOD PATH [JSON] — call the HubSpot API, fail loudly on a non-2xx status
# (printing the body so a scope/auth problem is legible, not silent).
hs() {
  local method="$1" path="$2" body="${3:-}" out status
  out="$(curl -sS -w $'\n%{http_code}' -X "$method" "${BASE}${path}" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H 'Content-Type: application/json' \
    ${body:+-d "$body"})"
  status="${out##*$'\n'}"
  out="${out%$'\n'*}"
  if [ "$status" -lt 200 ] || [ "$status" -ge 300 ]; then
    echo "HubSpot ${method} ${path} -> HTTP ${status}" >&2
    echo "$out" | jq . >&2 2>/dev/null || echo "$out" >&2
    return 1
  fi
  printf '%s' "$out"
}

# owner_id / owner_email — the test account's first CRM owner (the account user).
# Fixture records are assigned to it so the overlay owner→user mapping can match.
resolve_owner() {
  local owners
  owners="$(hs GET "/crm/v3/owners?limit=1")"
  OWNER_ID="$(printf '%s' "$owners" | jq -r '.results[0].id // empty')"
  OWNER_EMAIL="$(printf '%s' "$owners" | jq -r '.results[0].email // empty')"
  if [ -z "$OWNER_ID" ]; then
    echo "error: no CRM owner found — the token needs crm.objects.owners.read and the account needs at least one user" >&2
    exit 1
  fi
}

# deal_pipeline / deal_stage — resolve the account's first deals pipeline + its
# first stage, rather than hardcoding "default"/"appointmentscheduled" (which a
# customized test account may not have).
resolve_pipeline() {
  local pipelines
  pipelines="$(hs GET "/crm/v3/pipelines/deals")"
  DEAL_PIPELINE="$(printf '%s' "$pipelines" | jq -r '.results[0].id // empty')"
  DEAL_STAGE="$(printf '%s' "$pipelines" | jq -r '.results[0].stages[0].id // empty')"
  if [ -z "$DEAL_PIPELINE" ] || [ -z "$DEAL_STAGE" ]; then
    echo "error: could not resolve a deals pipeline/stage (crm.objects.deals.read / crm.schemas.deals.read)" >&2
    exit 1
  fi
}

# create OBJECT JSON -> prints the new record id
create() {
  hs POST "/crm/v3/objects/$1" "$2" | jq -r '.id'
}

# associate FROM_TYPE FROM_ID TO_TYPE TO_ID — HubSpot v4 default association.
associate() {
  hs PUT "/crm/v4/objects/$1/$2/associations/default/$3/$4" >/dev/null
}

# archive_marked OBJECT SEARCH_JSON — search fixtures by marker, batch-archive
# ONLY those. Paginates until the search is exhausted.
archive_marked() {
  local object="$1" search="$2" ids after resp
  while :; do
    resp="$(hs POST "/crm/v3/objects/${object}/search" "$(printf '%s' "$search" | jq --arg a "${after:-}" '. + (if $a=="" then {} else {after:$a} end)')")"
    ids="$(printf '%s' "$resp" | jq -r '[.results[].id] | @json')"
    if [ "$ids" != "[]" ]; then
      hs POST "/crm/v3/objects/${object}/batch/archive" \
        "$(printf '%s' "$ids" | jq '{inputs: [ .[] | {id: .} ]}')" >/dev/null
      echo "  archived $(printf '%s' "$ids" | jq 'length') ${object}"
    fi
    after="$(printf '%s' "$resp" | jq -r '.paging.next.after // empty')"
    [ -n "$after" ] || break
  done
}

cmd_reset() {
  echo "resetting fixture records (marker: @${EMAIL_DOMAIN} / '${NAME_PREFIX}')…"
  archive_marked contacts "$(jq -n --arg d "$EMAIL_DOMAIN" \
    '{filterGroups:[{filters:[{propertyName:"email",operator:"CONTAINS_TOKEN",value:("*@"+$d)}]}],properties:["email"],limit:100}')"
  archive_marked companies "$(jq -n --arg d "$EMAIL_DOMAIN" \
    '{filterGroups:[{filters:[{propertyName:"domain",operator:"CONTAINS_TOKEN",value:("*"+$d)}]}],properties:["domain"],limit:100}')"
  archive_marked deals "$(jq -n --arg p "$NAME_PREFIX" \
    '{filterGroups:[{filters:[{propertyName:"dealname",operator:"CONTAINS_TOKEN",value:($p+"*")}]}],properties:["dealname"],limit:100}')" || true
  # leads are best-effort (the object may not be enabled); ignore a search failure.
  archive_marked leads "$(jq -n --arg p "$NAME_PREFIX" \
    '{filterGroups:[{filters:[{propertyName:"hs_lead_name",operator:"CONTAINS_TOKEN",value:($p+"*")}]}],properties:["hs_lead_name"],limit:100}')" 2>/dev/null || echo "  (leads reset skipped — object not enabled)"
  echo "reset done."
}

cmd_seed() {
  resolve_owner
  resolve_pipeline
  echo "seeding fixtures owned by ${OWNER_EMAIL} (owner id ${OWNER_ID})…"
  cmd_reset   # idempotent: clear any prior fixture first

  # Companies
  local acme globex
  acme="$(create companies "$(jq -n --arg o "$OWNER_ID" '{properties:{name:"[fixture] Acme Overlay",domain:"acme.overlay-fixture.test",hubspot_owner_id:$o}}')")"
  globex="$(create companies "$(jq -n --arg o "$OWNER_ID" '{properties:{name:"[fixture] Globex Overlay",domain:"globex.overlay-fixture.test",hubspot_owner_id:$o}}')")"

  # Contacts
  local ada grace linus
  ada="$(create contacts "$(jq -n --arg o "$OWNER_ID" '{properties:{email:"ada.fixture@overlay-fixture.test",firstname:"Ada",lastname:"Lovelace",hubspot_owner_id:$o}}')")"
  grace="$(create contacts "$(jq -n --arg o "$OWNER_ID" '{properties:{email:"grace.fixture@overlay-fixture.test",firstname:"Grace",lastname:"Hopper",hubspot_owner_id:$o}}')")"
  linus="$(create contacts "$(jq -n --arg o "$OWNER_ID" '{properties:{email:"linus.fixture@overlay-fixture.test",firstname:"Linus",lastname:"Torvalds",hubspot_owner_id:$o}}')")"
  associate contacts "$ada" companies "$acme"
  associate contacts "$grace" companies "$acme"
  associate contacts "$linus" companies "$globex"

  # Deals
  local d1 d2 d3
  d1="$(create deals "$(jq -n --arg o "$OWNER_ID" --arg p "$DEAL_PIPELINE" --arg s "$DEAL_STAGE" '{properties:{dealname:"[fixture] Acme Renewal",amount:"12000",pipeline:$p,dealstage:$s,hubspot_owner_id:$o}}')")"
  d2="$(create deals "$(jq -n --arg o "$OWNER_ID" --arg p "$DEAL_PIPELINE" --arg s "$DEAL_STAGE" '{properties:{dealname:"[fixture] Globex New Business",amount:"48000",pipeline:$p,dealstage:$s,hubspot_owner_id:$o}}')")"
  d3="$(create deals "$(jq -n --arg o "$OWNER_ID" --arg p "$DEAL_PIPELINE" --arg s "$DEAL_STAGE" '{properties:{dealname:"[fixture] Acme Expansion",amount:"9000",pipeline:$p,dealstage:$s,hubspot_owner_id:$o}}')")"
  associate deals "$d1" companies "$acme"
  associate deals "$d2" companies "$globex"
  associate deals "$d3" companies "$acme"
  associate deals "$d1" contacts "$ada"

  # Leads (best-effort — the leads object may not be enabled on the account).
  if create leads "$(jq -n --arg o "$OWNER_ID" '{properties:{hs_lead_name:"[fixture] Inbound Lead",hubspot_owner_id:$o}}')" >/dev/null 2>&1; then
    echo "  created 1 lead"
  else
    echo "  (leads skipped — object not enabled or missing scope; contacts/companies/deals are sufficient)"
  fi

  echo
  echo "seed done: 2 companies, 3 contacts, 3 deals (+1 lead if enabled), all owned by ${OWNER_EMAIL}."
  echo
  echo "NEXT: set your local config/margince.yaml admin email to  ${OWNER_EMAIL}"
  echo "      so the overlay owner→user mapping matches and these records are visible"
  echo "      after you connect + POST /v1/overlay/reconcile."
}

cmd_whoami() {
  resolve_owner
  echo "owner id:    ${OWNER_ID}"
  echo "owner email: ${OWNER_EMAIL}"
  echo "-> set config/margince.yaml admin email to this so mirrored records are visible."
}

case "$SUBCMD" in
  seed)   cmd_seed ;;
  reset)  cmd_reset ;;
  whoami) cmd_whoami ;;
  *)      echo "usage: HUBSPOT_TOKEN=pat-... $0 {seed|reset|whoami}" >&2; exit 2 ;;
esac
