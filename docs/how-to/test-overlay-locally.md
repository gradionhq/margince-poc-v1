# Test the overlay locally against real HubSpot (team setup)

Validate the overlay end-to-end against the **real HubSpot API** — safely, reproducibly, and the same
way for every teammate — using an **isolated HubSpot developer test account** plus a committed fixture
seed script. This is the real-API path; for pure logic/regression coverage with no HubSpot at all, run
the integration lane instead (`make test-it DIR=backend/internal/modules/overlay`).

For the mental model (SoR modes, mirror-as-cache, fail-closed visibility) read
[explanation/overlay-augmentation.md](../explanation/overlay-augmentation.md); for the operator-level
connect reference, [connect-a-hubspot-overlay.md](connect-a-hubspot-overlay.md). This page is the
opinionated *team test* workflow those two don't spell out.

## Why a developer test account (and one per person)

- A HubSpot **developer test account** is a separate portal that **cannot sync with any other account**,
  so it is structurally impossible for anything you do here to reach a production portal. Create one at
  [app.hubspot.com/developer](https://app.hubspot.com/developer) → your app-dev account →
  *Test accounts → Create* (up to 10, free, 90-day Enterprise-feature trial).
- **One account per teammate**, not a shared one: write-back mutates records, and separate portals keep
  teammates from stepping on each other. The committed seed script makes every account's data identical,
  so "per person" costs nothing in reproducibility.

## The one rule that makes or breaks it: owner email = your margince admin email

The overlay mirror shows a record **only** if its HubSpot **owner's email** matches a margince user's
email — the auto owner→user mapping seeded on connect. There is **no admin-sees-all bypass**. So:

- the seed script assigns every fixture record to your test account's **owner** (your HubSpot login), and
- you set your **local `config/margince.yaml` admin email to that same owner email**.

Skip this and you'll connect fine but see an **empty** mirror. `whoami`/`seed` print the exact owner
email to use.

## One-time setup (per teammate)

### 1. Create a private app + token in your test account
In the test account: **Settings → Integrations → Private Apps → Create a private app → Scopes**, grant:

- **Read:** `crm.objects.contacts.read`, `crm.objects.companies.read`, `crm.objects.deals.read`,
  `crm.objects.leads.read`, `crm.objects.owners.read`, and `crm.schemas.{contacts,companies,deals}.read`.
  (`owners.read` is the easy-to-miss one — the visibility mapping 403s without it.)
- **Write** (needed to test write-back): `crm.objects.{contacts,companies,deals}.write`.

Create it and copy the token (`pat-…`; shown once).

### 2. Seed the fixture
```sh
HUBSPOT_TOKEN=pat-XXXX scripts/overlay-hubspot-fixture.sh seed
```
This creates a deterministic, owner-assigned fixture — 2 companies, 3 contacts, 3 deals (+ a lead if the
object is enabled), all marker-tagged (`@overlay-fixture.test` / `[fixture] …`) — and prints the **owner
email** to use next. It is idempotent (resets its own fixture first) and only ever touches
marker-tagged records.

### 3. Point your margince admin at that owner email
Edit your local (gitignored) `config/margince.yaml` and set the bootstrap **admin email** to the owner
email the script printed. If the stack has already bootstrapped an admin under a different email, run
`make dev-fresh` to re-bootstrap onto a clean database with the new email.

## Run the test

```sh
# 1. Boot the stack (sets the dev keyvault key so the token can be sealed)
make dev

# 2. Log in as the admin from config/margince.yaml → session cookie
curl -sS -X POST http://localhost:8080/v1/auth/login -H 'content-type: application/json' \
  -d '{"email":"<your-hubspot-owner-email>","password":"<from config/margince.yaml>"}' \
  -c cookies.txt >/dev/null

# 3. Connect the overlay (region: "us" or "eu1" — your test account's region)
curl -sS -X POST http://localhost:8080/v1/overlay/connection -b cookies.txt \
  -H 'content-type: application/json' \
  -d '{"incumbent":"hubspot","region":"us","privateAppToken":"pat-XXXX"}'

# 4. Trigger the initial backfill now (else it waits for the worker's next poll
#    tick). The call is queued, not synchronous: it answers 202 immediately and
#    the worker's poller (default ~2min, --overlay-reconcile-interval) picks it
#    up on its next tick and runs the sweep. Poll sync-status (step 5) until
#    every object class reaches backfillComplete/fresh.
curl -sS -X POST http://localhost:8080/v1/overlay/reconcile -b cookies.txt

# 5. Watch it hydrate, then read the fixture back FROM THE MIRROR
curl -sS http://localhost:8080/v1/overlay/sync-status -b cookies.txt | jq '.objects'
curl -sS 'http://localhost:8080/v1/deals?limit=10'         -b cookies.txt | jq '.data[].name'    # [fixture] deals
curl -sS 'http://localhost:8080/v1/people?limit=10'        -b cookies.txt | jq '.data[].full_name'
curl -sS 'http://localhost:8080/v1/organizations?limit=10' -b cookies.txt | jq '.data[].name'
curl -sS http://localhost:8080/v1/overlay/budget           -b cookies.txt | jq  # window/consumed/band + per-source sources + ~unknown headroom + search
```
Or just open **http://localhost:8080** and log in — records list and open normally, a top bar mode chip
marks this as an overlay installation, and Settings → Integrations' overlay card shows the per-object
sync status and the HubSpot rate budget. No screen renders a per-record freshness affordance (there is no
`last_synced_at` on a record read — see the next section for what a write does and does not touch).

### Test write-back (mutates the test portal only)
```sh
DEAL=$(curl -sS 'http://localhost:8080/v1/deals?limit=1' -b cookies.txt | jq -r '.data[0].id')
curl -sS -X PATCH "http://localhost:8080/v1/deals/$DEAL" -b cookies.txt \
  -H 'content-type: application/json' -d '{"name":"[fixture] Acme Renewal (edited)"}'
```
It writes to HubSpot **first**, then re-mirrors — confirm the rename in the test account's HubSpot UI.
Update and archive on person/organization/deal, plus update on lead and activity, all write back this
way; the 360 screens show Edit/Archive in overlay mode for every type that supports them. `create`,
`merge`, `advance-deal`, `promote-lead`, and `disqualify-lead` still answer `422 unsupported_by_sor`:
`create` because a record made this way would carry no `owner_id`, and the fail-closed visibility rule
makes an unowned record invisible to everyone, including whoever created it; the mirror implements none
of the other four.

#### What actually goes over the wire

Only the fields below project onto a writable HubSpot property
(`backend/internal/modules/overlay/hubspot/mapwrite.go`); anything else on the entity — including every
custom field and `owner_id` — has no writable counterpart and is dropped from the projection silently:

| Entity | Writable fields |
| --- | --- |
| person | `first_name`, `last_name`, `title` |
| organization | `display_name`, `industry` |
| lead | `full_name` |
| deal | `name`, `expected_close_date`, `amount_minor` + `currency` (a pair — supply both or neither; `pipeline_id`/`stage_id` are read-only and always `null` in overlay) |
| activity | varies by engagement kind (`activityWriteSpecs`): `subject` + `body` for call/meeting/email/task, `body` only for note; plus `direction` (call, email), `duration_seconds` (call), or `due_at` (task) — `occurred_at` and `meeting_status` are read-only for every kind |

A `PATCH` touching only fields outside that table still answers **200 with the change never applied** —
the mapping drops what it doesn't recognize instead of rejecting it, so nothing in the response says so.
Confirm it directly:

```sh
curl -sS -X PATCH "http://localhost:8080/v1/deals/$DEAL" -b cookies.txt \
  -H 'content-type: application/json' -d '{"owner_id":"00000000-0000-0000-0000-000000000000"}'
# 200; re-read the deal and owner_id is unchanged — the field was never sent to HubSpot.
```

### Disconnect + teardown
```sh
curl -sS -X DELETE http://localhost:8080/v1/overlay/connection -b cookies.txt   # 202; purges the mirror
```

### Reconnect
A revoked connection revives in place through the same connect call — there is no separate reconnect
endpoint:
```sh
curl -sS -X POST http://localhost:8080/v1/overlay/connection -b cookies.txt \
  -H 'content-type: application/json' \
  -d '{"incumbent":"hubspot","region":"us","privateAppToken":"pat-XXXX"}'
```
It clears the tombstones the disconnect above left, so the next sweep mirrors those records again
instead of finding them suppressed. Trigger one (step 4) rather than waiting out the poll interval.

## Reset / re-seed
```sh
HUBSPOT_TOKEN=pat-XXXX scripts/overlay-hubspot-fixture.sh reset   # archive the fixture (only marker-tagged records)
HUBSPOT_TOKEN=pat-XXXX scripts/overlay-hubspot-fixture.sh seed    # fresh, identical fixture
HUBSPOT_TOKEN=pat-XXXX scripts/overlay-hubspot-fixture.sh whoami  # print the owner id + email
```

## Troubleshooting

- **Connected but the mirror is empty.** The owner-email rule above — your margince admin email must
  equal the fixture owner's email. Re-check with `… whoami`; fix `config/margince.yaml` + `make dev-fresh`.
- **`leads`/`emails`/engagement `403`/`400` skips in the `make dev` worker logs.** Expected, not a
  failure. The overlay requests read scope only for contacts/companies/deals; leads and the engagement
  classes (calls/meetings/emails/notes/tasks) are swept **best-effort** with no requested scope, so a
  portal that gates one of them (HubSpot 403s leads/emails, 400s some engagement endpoints on a starter
  portal) logs a "best-effort object class not accessible … skipping" line and moves on. The
  scope-backed classes (contacts/companies/deals → person/organization/deal) still mirror fully. A `403`
  on one of *those* three, by contrast, is a real token/scope problem and aborts the sweep.
- **`sync-status` shows `pending`/`stale`.** Give the poller a beat or `POST /overlay/reconcile` again;
  `backfillComplete: true` + `state: "fresh"` per class means it's caught up.
- **`budget.band` is `shed`.** Force-fresh reads are degrading to the mirror rather than spending live
  quota — expected under load, not an error.
