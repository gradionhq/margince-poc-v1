# Connect a HubSpot overlay

Flip the installation from `native` to `overlay` mode, backed by a HubSpot portal, and watch the mirror
hydrate. For the mental model (the two SoR modes, the mirror-as-cache, fail-closed visibility, what
branch 1 does and doesn't do), read [explanation/overlay-augmentation.md](../explanation/overlay-augmentation.md)
first.

**Read, continuous sync, and write-back.** HubSpot stays canonical; records flow into Margince's
mirror, and a write to an overlay-mode record is applied to HubSpot **first**, then re-mirrored
(incumbent-first, with a stored-baseline drift check). The mapped write verbs —
create/update/archive on person, organization, and deal — are live; `advance-deal`, `merge`, and
`promote-lead` still answer `unsupported_by_sor` (not yet wired for overlay). To test write-back
locally against an isolated HubSpot test account, see
[test-overlay-locally.md](test-overlay-locally.md).

> **Single-organization installation (ADR-0061/A107).** One installation serves one organization; the
> server resolves its singleton organization itself, so no request selects a tenant — there is no
> `X-Workspace-Slug` header. The `curl`s below carry only the session cookie. ("Workspace" still names
> the internal RLS tenant these tables are scoped by.)

## Prerequisites

- Admin or ops RBAC. Connecting, disconnecting, and reconciling an overlay connection are
  organization-wide destructive config (they flip the SoR mode and purge the mirror for every user),
  so — like quota configuration — they are gated `admin`/`ops`-only. Every role may *read* the
  connection status.
- A HubSpot portal you can register a private app in (Settings → Integrations → Private Apps).

## 1. Create a HubSpot credential with least-privilege scopes

Either a **private app** (Settings → Integrations → Private Apps → Create a private app) or an
**account-service key** ([Development → Keys → Service keys](https://developers.hubspot.com/docs/apps/developer-platform/build-apps/authentication/account-service-keys))
works — both authenticate as `Authorization: Bearer pat-…`, so the adapter treats them identically.
Grant only the read scopes the adapter needs:

- `crm.objects.contacts.read`, `crm.objects.companies.read`, `crm.objects.deals.read`
- `crm.schemas.contacts.read`, `crm.schemas.companies.read`, `crm.schemas.deals.read`
- **`crm.objects.owners.read`** — easy to miss, and required: the Owners API 403s without it, and the
  whole per-user visibility projection depends on resolving `hubspot_owner_id` to an email through it.

Grant object **write** scopes (`crm.objects.{contacts,companies,deals}.write`) only if you intend to
exercise write-back; a read-only connection needs none, and an over-scoped token is a needless
blast-radius increase.

Copy the generated token (`pat-…`) — HubSpot shows it once.

## 2. Connect the overlay

```sh
curl -X POST http://localhost:8080/v1/overlay/connection \
  --cookie 'crm_session=<admin or ops session>' \
  -H 'Content-Type: application/json' \
  -d '{"incumbent": "hubspot", "region": "eu1", "privateAppToken": "pat-…"}'
```

`region` routes EU portals to HubSpot's `eu1` (Frankfurt) host; use the region your portal actually
lives in. The response is the connection record (`incumbent`, `region`, `status: active`,
`connectedAt`, `scopes`) — **the token itself is never echoed**, here or on any later read. It is sealed
into the secret vault; the database stores only an opaque reference.

At most one incumbent connection can be active — a second `POST` while one is active answers
`409 incumbent_already_connected`.

**Connecting also seeds the user map.** On connect, Margince pulls the HubSpot **owners directory** and
matches each owner's email against your existing Margince users, writing a `mirror_user_map` row for
every match (a MATCH against existing users — it never *creates* a Margince account from HubSpot data).
An owner whose email matches no user gets no row (fail-closed); the admin `manual` map is the escape
hatch for those. The same match re-runs on every reconcile sweep, so users who join later get picked up.

## 3. Trigger the initial load and watch the mirror hydrate

The initial backfill (the full record load + associations) runs on the **reconcile sweep**, not
in-request on connect. The background poller runs it within the reconcile interval; to start it
immediately, trigger a sweep on demand:

```sh
curl -X POST http://localhost:8080/v1/overlay/reconcile \
  --cookie 'crm_session=<admin or ops session>'
```

The first sweep after a connect backfills every object class (checkpointed and resumable — a re-run
resumes, never re-lists from the start) and re-seeds the user map; later sweeps ride the modified-since
watermark. To bound the initial load against a large portal, set **`MARGINCE_OVERLAY_BACKFILL_LIMIT`**
(a per-object-class record cap; unset = uncapped, a non-integer/negative value is a boot error) on the
api and worker before boot — continuous-sync sweeps stay uncapped. Then poll sync-status until every
object class shows `backfillComplete: true` and `state: "fresh"`:

```sh
curl --cookie 'crm_session=<session>' \
  http://localhost:8080/v1/overlay/sync-status | jq '.objects'
```

Check the shared HubSpot rate budget alongside it:

```sh
curl --cookie 'crm_session=<session>' \
  http://localhost:8080/v1/overlay/budget | jq '{window, consumed, limit, band}'
```

`band` is `ok`/`warn`/`shed` — `shed` means force-fresh reads are currently degrading to the mirror
rather than spending live quota (see the explanation page's [budget meter
section](../explanation/overlay-augmentation.md#force-fresh-reads-the-budget-meter-and-shed-degrade)).

## 4. Keep it fresh (continuous sync)

The reconcile poller re-runs on its interval; you can also sweep on demand with the same
`POST /v1/overlay/reconcile` used above. Each sweep re-seeds the user map, backfills anything not yet
loaded (a cheap no-op once converged), then reconciles modified records against HubSpot: a diverged row
is overwritten by the incumbent's value (never the reverse) and emits `mirror.conflict`.

## 5. Disconnect

```sh
curl -X DELETE http://localhost:8080/v1/overlay/connection \
  --cookie 'crm_session=<admin or ops session>'
```

Answers `202` and queues teardown: the mirror replica, association edges, and the visibility
projection over them are purged in one transaction, and a suppression tombstone is left per erased
record so a stray in-flight sweep can't re-hydrate it. The connection's own audit trail (who
connected/disconnected, when) is retained — teardown purges incumbent-derived *record* content, not the
audit history of the connection itself. (Branch 1 builds no FTS index or embeddings over mirror content,
so there is nothing of that kind to purge yet — see
[explanation/overlay-augmentation.md](../explanation/overlay-augmentation.md#teardown-purge-and-tombstone).)

## Verify the connection end-to-end

Beyond watching `sync-status` reach `fresh`, confirm the mirror is actually faithful to the source
rather than just present:

1. **Field parity against the live portal.** Pick one mirrored record and diff it against HubSpot
   directly with the same credential:
   ```sh
   PID=$(curl -s --cookie 'crm_session=<session>' \
     http://localhost:8080/v1/people?limit=1 | jq -r '.data[0].id')
   curl -s --cookie 'crm_session=<session>' \
     http://localhost:8080/v1/people/$PID | jq '{full_name, freshness, trust_tier}'
   ```
   Expect `trust_tier: "external"` (T2, not `authoritative`) and an honest `freshness` — `authoritative:
   false` plus a real `last_synced_at`, never a false "live" claim.
2. **Fail-closed visibility.** A user whose email matched no HubSpot owner (so auto-seeding wrote no
   `mirror_user_map` row) must see **zero** mirrored rows (`GET /people` returns an empty list,
   `GET /people/{id}` answers 404, not 403 — existence-hiding). A user whose email *did* match an owner
   sees exactly their owned rows without any manual step; the admin `manual` map covers anyone the
   email match can't reach.
3. **Reconcile is incumbent-wins.** Edit a field in the HubSpot UI, `POST /overlay/reconcile`, and
   confirm the mirror took HubSpot's value — never the reverse.
4. **Teardown actually purges.** After disconnecting, confirm `overlay_mirror`/`overlay_association` are
   empty, a tombstone row exists per purged record, and the connection's own audit rows
   (`entity_type=incumbent_connection`) still exist.
