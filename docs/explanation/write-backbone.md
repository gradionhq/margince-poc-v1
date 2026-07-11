# The write backbone — storekit, `audit_log` & the outbox

Every mutation in this backend commits **three rows in one transaction** — the domain row, an
`audit_log` row, and an `event_outbox` row — through code spelled once in `storekit`. A relay then
ships the outbox to the event bus, and consumers dedupe. That is one mechanism, not three, so this is
one document.

It is the deep reference behind the one-paragraph summary in
[architecture.md](architecture.md#the-write-shape) and the store call-site in
[backend-onboarding.md](backend-onboarding.md#how-a-store-reads-and-writes-the-shape).
Read those first if you want the short version.

## The shape at a glance

```
HTTP request / agent run / bus consumer
        │  (middleware binds actor + workspace + correlation_id onto ctx)
        ▼
  module Handler ──► Store.tx(ctx, fn)  ==  database.WithWorkspaceTx(ctx, pool, fn)
                          │  SET LOCAL app.workspace_id  (RLS bound)
                          ▼
        ┌───────────────── ONE transaction ─────────────────┐
        │  INSERT INTO <domain table> …           ← the change │
        │  storekit.Audit(…)  → INSERT audit_log  ← the record │  returns auditID
        │  storekit.Emit(…, auditID, …) → INSERT event_outbox  │  (envelope links auditID)
        └──────────────────── COMMIT ───────────────────────┘
                          │
                          ▼
   platform/events.Relay  ── polls event_outbox WHERE published_at IS NULL ORDER BY seq
                          ── XADD envelope → Redis stream  (gw:events:crm:<entity>)
                          ── UPDATE event_outbox SET published_at = now()
                          ▼
   consumer groups (cg:context-graph, cg:overnight-agent, …)
                          └─ each handler wrapped in events.Dedupe(event_id)
```

**Why three rows, one transaction.** The domain change, the proof it happened, and the event that
announces it either all commit or none do. There is no "write the row, then best-effort publish" dual
write to go wrong: the event is staged in the same Postgres transaction (the *transactional outbox*
pattern), and a separate relay moves it to the bus afterwards. A crash between commit and relay leaves
the row `published_at IS NULL` — the relay picks it up on the next poll.

---

## 1. `storekit` — the one spelling

`internal/platform/database/storekit/` holds the mechanics every module store shares; modules own
their tables and SQL, the invariants live here. The two functions that carry the write shape:

```go
// Writes the append-only audit_log row inside the mutation's tx; RETURNS its id
// so the paired event can carry it as trace.audit_log_id.
func Audit(ctx, tx, action, entityType string, entityID ids.UUID, before, after any) (ids.UUID, error)

// Same, plus operational evidence ABOUT the mutation (which policy fired, which
// inbound message triggered it) landing in audit_log.evidence — never in before/after.
func AuditWithEvidence(ctx, tx, action, entityType string, entityID ids.UUID, before, after any, evidence map[string]any) (ids.UUID, error)

// Stages the domain event in the transactional outbox, linked to the audit row.
func Emit(ctx, tx, auditID ids.UUID, eventType, entityType string, entityID ids.UUID, payload any) error
```

What they do, precisely (from `storekit.go`):

- **`Audit`** resolves the actor from context (`store: no actor bound` if missing — the middleware
  always binds one), reads the workspace, marshals `before`/`after`/`evidence` to JSON, mints a
  UUIDv7 id, and inserts the `audit_log` row — stamping `authorization_rule = auth.AuthzRule(p,
  entityType, action)` (which RBAC/scope rule allowed it). It returns the new row id.
- **`Emit`** resolves the actor and workspace, **requires** a `correlation_id` on the context
  (`store: no correlation id bound` otherwise), builds the full `events.Envelope` (see §3), attaches
  the `auditID` as `trace.audit_log_id` and a `causation_id` if the context carries one, resolves the
  stream with `StreamFor(eventType)`, runs `env.Validate()`, and inserts `(stream, envelope)` into
  `event_outbox`.
- **`CapturedBy(ctx)`** returns the authenticated principal's id — the server-derived provenance
  stamp. Provenance (`captured_by`) and the audit actor are **never** taken from a request body; a
  client that could set them could forge the P5 provenance signal.

**The before/after vs evidence discipline matters.** `before`/`after` are reserved for the *record's
own field images* — the field-history read (`GET /v1/field-history`) projects per-field diffs directly
from them. Operation metadata (a retention policy id, the inbound email that triggered a promotion)
goes in `evidence`. Folding metadata into `before`/`after` would make field-history show field changes
that never happened on the record.

**Concurrency for updates.** A by-id UPDATE of a versioned row never uses a bare `UPDATE`; it goes
through `storekit.Patch`:

```go
p := storekit.NewPatch()
p.Set("name", old.Name, in.Name)            // accumulates the SET list + the before/after audit diff
err := p.ApplyWithVersion(ctx, tx, "deal", id, version)   // version-CAS: 0 rows on a live row → ErrVersionSkew
// or p.ApplyGuarded(ctx, tx, "deal", id, in.IfVersion)   // CAS when a version is supplied, else LockRow+ApplyLocked
```

`Patch.Before()/After()` then feed straight into `Audit`, so the update, its audit diff, and its event
stay one story.

---

## 2. `audit_log` — the immutable spine

Defined in `migrations/core/0012_audit_log.up.sql` (actor vocabulary extended additively by `0016`,
action vocabulary by `0018`/`0053`):

| Column | Meaning |
|---|---|
| `id` | UUIDv7 PK (time-ordered) |
| `workspace_id` | tenant FK, `ON DELETE RESTRICT` — it **is** an RLS tenant table |
| `actor_type` | `CHECK IN ('human','agent','connector','system')` |
| `actor_id` | user uuid / agent id / connector name / `system` |
| `passport_id` | the Agent Seat Passport that authorized an agent action (nullable) |
| `on_behalf_of` | the human authority behind an agent/connector action (nullable FK `app_user`) |
| `action` | `CHECK IN ('create','update','archive','merge','promote','restore','export','erase','login','assign','advance_stage', …)` — extended additively |
| `entity_type` / `entity_id` | the subject (`entity_id` NULL for non-entity actions like login/export) |
| `before` / `after` | the record's field images (jsonb) — **field-history reads these** |
| `authorization_rule` | which RBAC/scope rule allowed the write |
| `evidence` | operation metadata (jsonb) |
| `occurred_at` | `timestamptz DEFAULT now()` |

**Append-only, enforced two ways.** A trigger raises loudly on any mutation, and the app role's grants
are revoked — so a tamper attempt *fails*, never silently no-ops:

```sql
CREATE TRIGGER trg_audit_no_mutate BEFORE UPDATE OR DELETE ON audit_log
  FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();   -- RAISE EXCEPTION … ERRCODE 'check_violation'
-- plus migrations/core/0015: REVOKE UPDATE, DELETE ON audit_log FROM margince_app;
```

The actor columns are the structured mirror of the envelope's `Actor` (§3) — the same four classes,
kept in lock-step by a fitness test so a fifth class can't slip onto the bus and break the mirror.

---

## 3. `event_outbox` — the transactional outbox

Defined in `migrations/core/0013_event_outbox.up.sql` (+ the `seq` column in `0016`):

| Column | Meaning |
|---|---|
| `id` | UUIDv7 PK |
| `stream` | destination stream key, e.g. `gw:events:crm:deal` |
| `envelope` | the full typed envelope (jsonb) |
| `seq` | `bigint GENERATED ALWAYS AS IDENTITY` — assigned at INSERT |
| `published_at` | NULL until the relay ships it |
| `created_at` | `timestamptz DEFAULT now()` |

It is **infra-owned and carries no RLS** — tenancy rides *inside* the envelope (`workspace_id`), not as
a row policy. The relay poll orders by **`seq`, not `created_at`**: `created_at` is transaction-*start*
time, so a long transaction could publish "before" a short one that committed earlier. `seq` is
assigned at INSERT, and because two transactions touching one entity serialize on its row lock,
per-entity `seq` order **is** commit order (no cross-entity order is promised, which is all the bus
needs).

### The envelope (`internal/shared/kernel/events/envelope.go`)

`Emit` builds this shape; the relay ships it verbatim; consumers decode `payload` per the catalog.

```go
type Envelope struct {
    EventID     ids.UUID        // UUIDv7 — the consumer-side idempotency key (dedupe on this)
    Type        string          // "<entity>.<verb>", e.g. "deal.created"
    Version     int             // payload schema version, stamped from VersionOf(Type)
    WorkspaceID ids.UUID        // the bus analogue of RLS
    OccurredAt  time.Time
    Actor       Actor           // {Type, ID, PassportID, OnBehalfOf} — mirrors audit_log
    Entity      EntityRef       // {Type, ID} — a REF, never the record body
    Payload     json.RawMessage // per-type; consumers read the record back under their own scopes
    Trace       Trace           // {CorrelationID, CausationID, AuditLogID}
}
```

`Envelope.Validate()` is the shared gate both sides run: it rejects an envelope with no event_id,
an unroutable `Type`, a version mismatch, no workspace, an unknown actor class, no entity ref, or an
incomplete trace. `Emit` runs it **before** the outbox insert, so a malformed event fails at the write
— never as a wedged relay row.

### The event catalog (`internal/shared/kernel/events/catalog.go`)

The enumerable set of `<entity>.<verb>` types, each mapped to its stream + payload version:

- **Ten V1 streams**, prefix `gw:events:crm:` — `person, organization, deal, lead, activity,
  approval, capture, coldstart, audit, identity`. Workspace is an envelope field, never a stream
  (per-tenant streams would explode key count).
- **Family routing:** a type whose entity segment isn't itself a stream rides its family — e.g.
  `consent.*` / `retention.*` → the `person` stream; `offer.*` / `pipeline.*` / `stage.*` → `deal`;
  `signal.*` → `capture`; `user.deactivated` / `role.changed` / `passport.revoked` → `identity`.
- **`StreamFor(type)`** routes; an unknown type is a programming error surfaced *before* the outbox
  write (an unroutable row would wedge the relay forever). **`VersionOf(type)`** is the single source
  of the payload version, so a future v2 bump happens in one place.

**Adding a new event type = adding one line to `catalog`** (plus its payload type). Miss it and
`Emit`/`Validate` fail loudly at the write.

---

## 4. The relay — outbox → bus

`internal/platform/events/relay.go`. It never *originates* an event; it only moves what a transaction
already committed:

- Inside `database.WithInfraTx` (the no-GUC helper — `event_outbox` has no RLS), it claims a batch:
  ```sql
  SELECT id, stream, envelope FROM event_outbox
   WHERE published_at IS NULL ORDER BY seq LIMIT $1 FOR UPDATE SKIP LOCKED
  ```
  `FOR UPDATE SKIP LOCKED` lets two replicas split the backlog without double-shipping.
- For each row it `XADD`s the envelope to its stream (capped `MAXLEN ~`), then
  `UPDATE event_outbox SET published_at = now()` for the shipped prefix. Idle poll ~200ms.
- **Where it runs:** inline in `cmd/api` by default (`--inline-relay=true`) — one process is a complete
  install — or standalone in `cmd/worker` for split deployments
  ([decisions/0005](../../decisions/0005-in-process-outbox-relay.md)). Domain code **never** `XADD`s
  directly.
- Delivery is **at-least-once** by design (a crash after XADD, before the `published_at` stamp,
  re-ships the row). Backlog + throughput are exported on `/metrics`
  (`margince_outbox_unpublished`, `margince_relay_published_total`), and when the relay is inline the
  bus is a `/readyz` dependency.

---

## 5. The consumer side — groups & dedupe

`internal/platform/events/subscriber.go` + `dedupe.go`. The catalog declares **seven** V1 consumer
groups; each sees every event once and scales horizontally inside the group. Because Redis groups
partition only by stream, the workspace and actor filters run in-process.

**What each group does — and which are live.** Only **three** are wired to a subscriber today, all in
`cmd/worker`; the other four are catalog-declared placeholders with no consumer yet (honest status —
the streams carry events, but nothing reads these groups):

| Group | Job | Status |
|---|---|---|
| `cg:context-graph` | maintain the pgvector retrieval embeddings as records change | **live** (worker; only when an embedder model is configured) |
| `cg:workflows` | dispatch the automation/workflow engine off matching events | **live** (worker) |
| `cg:overnight-agent` | on `approval.decided`, resume the parked Surface-B run with the human's answer | **live** (worker; only when a model is configured) |
| `cg:capture` | (reserved) | declared, no subscriber |
| `cg:flow-bridge` | (reserved) | declared, no subscriber |
| `cg:read-model` | (reserved) read-model projections | declared, no subscriber |
| `cg:audit-stream` | (reserved) the agent-action audit slice | declared, no subscriber |

**The workflow seam** (`ports/workflow`) is how `cg:workflows` reacts without a visual builder:
a `Handler` declares a `Spec` (name + trigger + tier), a pure `Match` predicate, a `Plan` that computes
a **typed `Effect`** *without applying it* (so dry-run/diff preview work), and an `Apply` that executes
it — 🟢 effects auto-execute, 🟡 effects need an approval token — idempotent on the handler's
idempotency key. Effects are a **closed** action set (`create_record`, `update_record`, `assign_owner`,
`advance_deal`, `send_email`, … — the anti-builder guard). `compose.NewWorkflowEngine` registers the
shipped starter workflows plus the system ones (lead routing/scoring).

Every handler is wrapped in `events.Dedupe`:

```go
func Dedupe(rdb, group, next) Handler   // key = "gw:dedupe:<group>:<event_id>", TTL 96h
```

The order is **run-then-mark**, and it's load-bearing: a mark written *before* the effect would, on a
crash in between, survive as a claim with no effect — the redelivery would be swallowed and the event
silently lost. Marking *after* means a crash can only cause a re-run, which the **authoritative**
idempotency layer absorbs as a no-op. That layer is effects upserting by natural key
(`uq_activity_source` and kin) — Dedupe is an optimization over it, never a correctness substitute.
The 96h TTL sits above the stream retention horizon so a long-offline consumer can't re-see an event
after its dedupe entry expired.

---

## 6. Correlation & causation (the trace)

The `Trace` on every envelope lets you reconstruct one operation as a single story:

- **`correlation_id`** groups every event of one originating operation. It is minted **once** per HTTP
  request by the chassis middleware (`internal/platform/httpserver/chassis.go`:
  `principal.WithCorrelationID(r.Context(), ids.NewV7())`) and once per agent run
  (`compose/runnerservice.go`). A background job that emits must bind its own — `Emit` errors without
  one.
- **`causation_id`** is the `event_id` that *caused* this event (nil for the first in a chain) — set
  from `principal.CausationEvent(ctx)` when a consumer re-binds its triggering event before doing more
  work. Correlation is the whole operation; causation is the parent edge.
- **`audit_log_id`** links the event back to the exact audit row written in its transaction.

---

## 7. The invariants, and the gates that hold them

You don't have to remember these — a fitness test fails your PR if you break one (see
[backend-onboarding.md](backend-onboarding.md#the-gates-that-judge-your-pr-fitness-functions)):

| Rule | Gate |
|---|---|
| Every audited mutation also emits an outbox event (in the same function) | `writeshape_test.go` |
| `audit_log` / `event_outbox` are written only through `storekit` | `tableownership_test.go` |
| A by-id UPDATE of a versioned row carries a concurrency guard | `updateguard_test.go` |
| The `audit_log` `action`/`actor_type` CHECK sets equal their Go enums | `enumsync_test.go` |
| `audit_log` is a tenant table with RLS ENABLE+FORCE+policy | `rls_coverage_integration_test.go` |
| Errors are classified by SQLSTATE, never `Error()` text | `errmatch_test.go` |

---

## Rules of thumb

- **Never `INSERT` into `audit_log` or `event_outbox` directly** — always `storekit.Audit` / `Emit`,
  in the domain write's transaction.
- **Actor and `captured_by` are server-stamped** from the authenticated principal, never a request
  body.
- **`before`/`after` are the record's field images; `evidence` is operation metadata** — keep them
  separate or field-history lies.
- **A new event type needs a `catalog` entry** (and its payload type) — otherwise `StreamFor` /
  `Validate` fail at the write, which is where you want to find out.
- **Bind a `correlation_id`** on any write path the HTTP/runner middleware doesn't cover (a bespoke
  background job).

## Where the code lives

| | |
|---|---|
| Write shape (`Audit`, `AuditWithEvidence`, `Emit`, `Patch`) | `internal/platform/database/storekit/{storekit,patch}.go` |
| Envelope + catalog (types, streams, versions, groups) | `internal/shared/kernel/events/{envelope,catalog}.go` |
| The relay | `internal/platform/events/relay.go` |
| Consumer subscriber + dedupe | `internal/platform/events/{subscriber,dedupe}.go` |
| `audit_log` DDL + immutability | `migrations/core/0012_audit_log.up.sql` (+ `0016`/`0018`/`0053`) |
| `event_outbox` DDL + `seq` | `migrations/core/0013_event_outbox.up.sql` (+ `0016`) |
| App-role grants (audit_log revoke) | `migrations/core/0015_app_role_grants.up.sql` |
| Correlation minting | `internal/platform/httpserver/chassis.go`, `internal/compose/runnerservice.go` |
