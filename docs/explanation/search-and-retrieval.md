# Search & retrieval — two arms, one row scope, two kinds of staleness

Search is how a typed query becomes a ranked list of records — and, one seam
deeper, how the AI layers ground their answers in the workspace's own data.

Two independent rankings run per query: a **lexical** arm (Postgres full-text
search over generated `search_tsv` columns) and a **vector** arm (semantic
similarity over pgvector embeddings), fused into one list by reciprocal rank
fusion. The rule that governs both: a search hit *is* a read, so the caller's
row-scope authority is compiled **into** the query rather than applied to its
results.

The second half of this page is the part that is easy to get wrong: an embedding
store can be stale in two different ways, and only one of them is a human's
decision. Where the embed lane's model comes from is
[ai-runtime.md](ai-runtime.md); who may see a row is
[authorization.md](authorization.md); the who-knows-whom projection this page
only borrows from is [relationship-graph.md](relationship-graph.md).

## The shape at a glance

```text
 query text
     │
     ├──────────── LEXICAL ARM ────────────┐        ┌──── VECTOR ARM ─────────────────┐
     │  one UNION branch per entity type   │        │  embed the query on the CURRENT │
     │  websearch_to_tsquery('simple',     │        │  binding (identity, dims)       │
     │    f_unaccent(q))                   │        │      │                          │
     │  ‖ f_fold_apostrophes(q)            │        │      ▼                          │
     │  ‖ german/english stems (activity)  │        │  embedding e JOIN <entity> t    │
     │      │                              │        │  WHERE e.model = $identity      │
     │      ▼                              │        │  1 - (e.embedding <=> $q)       │
     │  ts_rank_cd(t.search_tsv, …)        │        └──────────┬──────────────────────┘
     └──────────┬──────────────────────────┘                   │
                │   every branch of BOTH arms:                 │
                │   auth.Require(entity, read)  ← denied type contributes no branch
                │   + ScopeClause / ActivityScopeClause        │
                │   + archived_at IS NULL                      │
                ▼                                              ▼
            ┌──────────────── RRF fusion (k = 60) ────────────────┐
            │  score = Σ lanes 1/(k + rank)   ties → (type, id)   │
            └──────────────────────┬─────────────────────────────┘
                                   ▼
                         ranked hits (fused score)
```

- **Six searchable entity types**, declared once in `searchBranches`: `person`,
  `organization`, `deal`, `lead`, `project`, `activity`. Adding a searchable
  entity is a row there (plus the matching `embedText` / `pendingSources`
  entries) — the query builder derives the rest.
- **The lexical arm** ranks with `ts_rank_cd` over each table's generated
  `search_tsv` column, and pages by a keyset cursor on `(score DESC, rtype, id)`
  so the page boundary is stable under concurrent writes. Name fields parse
  `simple` and unaccented (`Muller` finds `Müller`, migration `0052`), OR-ed with
  the apostrophe-collapsed parse (`oreilly` finds `O'Reilly`, migration `0077`);
  the activity branch additionally ORs the German and English stemmed parses, so
  `Vertrag` reaches a row that stemmed `Verträge`.
- **The vector arm** is one row per `(entity, chunk_ix)` in `embedding`, ranked
  by cosine distance (`<=>`) and always filtered to the current embed identity
  (next section). Migration `0114` dropped the HNSW index deliberately — the
  identity-filtered per-branch query sequential-scans, and an index over a
  mixed-width column would not have been usable anyway.
- **Fusion is RRF**, `k = 60` (`rrfK`, ADR-0022 §6): each lane contributes
  `1/(k + rank)`, so an entity both lanes agree on outranks either lane's solo
  favourite. Both lanes are over-fetched to `3 × limit` — an entity ranked just
  past `limit` in each lane can still fuse into the top set — and each returned
  hit's `Score` is the **fused** score, not the lane score it arrived with.
- **The vector arm degrades honestly.** A nil embedder and a bound embedder whose
  `EmbedIdentity()` is `""` are the same shape from the query side: no live embed
  lane to rank against, so the fused path returns the lexical lane alone rather
  than calling into an unbound `Embed()`. A zero query vector is refused for a
  different reason — cosine against zero is `0/0 = NaN`, and a naive
  `ORDER BY sim DESC` sorts NaN *first*, silently outranking every real match.
  The same guard sits on the write side: a zero vector never reaches storage.

**Two entry points, and they are not the same query.** `GET /v1/search` runs the
**lexical arm alone** (`Store.Search`) — ranked, cursor-paged, every result
stamped `trust_tier: authoritative` (the provenance grade the contract puts on
natively-held records, as opposed to `external` mirror data). The **fused** path (`Store.HybridSearch`) is
reached through the `shared/ports/retrieval` seam (`search.Retriever`), which is
what the AI layers ground on: `cmd/api` wires it with the resolved model path's
embedder for the offer-draft surface, `cmd/worker` wires it as the Surface-B
runner's grounding. The agent intent tools in `compose/registry.go` are
constructed with a **nil** embedder on purpose — they ground on the context-graph
walk, which needs no embed lane.

## Row scope is not a filter on the results

A search hit *is* a read, so the caller's authority is not applied to a result
set — it is compiled into the query that produces it:

- **Object RBAC picks the branches.** `branchScope` runs
  `auth.Require(ctx, entity, read)` per entity type; a type the role cannot read
  contributes **no UNION branch at all**, silently and without an error, so
  search can never out-see the per-entity lists. If every requested type is
  denied, the answer is an empty page — not a 403 that would disclose which types
  exist.
- **Row scope rides the same branch.** `auth.ScopeClauseFor` (or
  `auth.ActivityScopeClause` for the activity branch's link walk) is appended to
  that branch's `WHERE`, in the same `database.WithWorkspaceTx` transaction that
  binds the RLS GUC. Both arms use the identical helper — fusion adds no
  visibility of its own.
- **The context walk gates the same way.** `anchorProfile` is the existence *and*
  visibility gate for the whole assembly (`auth.EnsureVisible`, then a real row
  read whose `pgx.ErrNoRows` becomes `apperrors.ErrNotFound`), and every hop-2
  candidate is probed individually with `auth.VisibleTo`. The walk widens
  context, never authority.

A row-scope miss therefore answers **404, not 403** — a record you cannot see is
indistinguishable from one that does not exist, which is the same existence-hiding
posture every single-record read takes. See
[authorization.md](authorization.md).

## Embedding identity — `provider/model@dimensions`

`Embedder.EmbedIdentity()` returns the current binding's stamp and its expected
vector width: the identity is the string `<provider>/<model>@<dims>`, and it is
written into `embedding.model` on every row. It is cheap by contract — no API
call — which is what lets every read, every job guard and the readiness probe
compare against it.

**Retrieval filters to the current identity, and that predicate is load-bearing
twice.** For correctness: rows embedded in an older model's space must not rank
against a query embedded in the new one. And for crash-avoidance: the
`embedding.embedding` column is an **unbounded** `vector` (migration `0114`), so
a bare `e.embedding <=> $query` against a differently-sized row raises
*"different vector dimensions"* — the identity filter excludes those rows before
the projection ever computes `<=>`.

**The write side is content-hash keyed, identity-aware.** `UpsertEmbedding` reads
the stored `(chunk_hash, model)` first: unchanged text under an unchanged binding
costs **no model call**. A text match under a *changed* binding still re-embeds —
skipping on the hash alone would leave a row stamped with a model no longer
serving the workspace, indistinguishable from a live one. The insert is a CAS on
the hash that was read, so a concurrent writer that already moved the row past it
wins rather than being clobbered.

Together these give the property the whole ops story rests on: **correctness
never depends on a reindex finishing.** A row that is stale, or absent, or stored
at another width is *hidden* from retrieval — never served as if current — and
every already-current row keeps answering queries while a rebuild runs.

An **unbound** embed lane (`--ai-fake`, or a routing config that never bound
`embeddings:`) is a legitimate deployment shape, not an error: no binding marker
is seeded, `/readyz` reports `unknown`, the three reindex operations stay their
generated 501, neither drift-sweep job registers, and the fused query degrades to
lexical. Which model serves the lane, and its `dimensions`, are runtime config —
[ai-runtime.md](ai-runtime.md) and
[../reference/configuration.md](../reference/configuration.md).

## Two kinds of staleness, two answers

| What is stale | How it is detected | Who fixes it |
|---|---|---|
| An entity with no embedding row **under the current identity**, while the store's populated identity **matches** the configured one | the pending scan (`NOT EXISTS` against `embedding` at the current identity) | the periodic **drift sweep** — no operator, no confirm |
| The configured identity **differs** from `embed_store_binding.populated_identity` | the marker read (one PK lookup, no scan) | an operator, through **preview → confirm** |

Both are "rows missing at the current identity", and it would be tempting to heal
them with one mechanism. They are kept apart because their *cost* differs: the
first is spend the system already committed to and failed to make, the second is
a rebuild of the whole corpus that a human should see priced first.

### Drift — the bus lost the event, a worker sweep heals it

`EmbedGen` subscribes to `cg:context-graph` and re-embeds an entity whose content
changed (`.created`, `.updated`, `.captured`, `.promoted`, `.merged`). That bus
is **at-least-once**, and at-least-once is not at-least-once *delivered to a
process that survives*: a worker that dies between ack and write leaves an entity
with no embedding row, invisible to the vector arm until something re-embeds it —
which, without a sweep, means until a human confirms a reindex they never caused.

- **`embed_drift_sweep`** — the dispatcher, cadence **15 minutes**, fans out one
  child per workspace and does no tenant work itself. The contract's own reason
  states the budget it is buying: an empty pass is a handful of indexed
  `NOT EXISTS` probes per workspace, and fifteen minutes is how long a lost embed
  event may keep a record out of semantic search.
- **`embed_drift_workspace`** — one workspace's pass; no wall-clock timeout (the
  pass is bounded by the pending backlog, each embed by the model lane's own
  per-call timeout), three attempts, since the dispatcher's tick *is* the real
  retry cadence.

Both are declared in `backend/api/jobs.yaml` and registered by
`compose/embeddriftsweep.go` under a condition **stricter** than the contract's
`when: [Embedder]` can express: the lane must also be *bound* (a non-empty
`EmbedIdentity()`), because a configured-but-unbound lane seeds no marker and
there is nothing to compare a row against.

The sweep's own properties:

- **Idempotent by the same skip-compare that makes reindex resumable.** It calls
  `UpsertEmbedding`, so an entity a concurrent ordinary embed already handled
  costs nothing, and a retried job is free.
- **It re-reads the entity's current source text at heal time.** The pending scan
  collects **ids only**; `healEntity` re-reads the text in its own transaction
  before embedding. Embedding the scan's snapshot would store obsolete text under
  the current identity — and the row would then never look pending again, even
  though its embedding is wrong. An entity archived or blanked since the scan is
  simply no longer pending: skipped, not an error.
- **It runs as the system principal.** An index repaired through one caller's row
  scope would silently leave out records that caller cannot see, for everybody.

### A changed binding keeps its preview → confirm

When the operator swaps the embed binding, the spend is theirs to decide, so the
flow stays human:
`GET /v1/embeddings/reindex/preview` (the ADR-0020 scope-before-the-spend
estimate — always `estimate_quality: heuristic`, a `SUM(octet_length(text))/4`
work-shape floor over the pending set, plus each workspace's budget-impact band)
→ `POST /v1/embeddings/reindex`, admin/ops-only and `x-agent-access: human-only`.
The confirm claims the marker and enqueues the fleet-wide run in **one**
transaction, so a claim can never outlive a job that was never queued.

**The sweep must no-op there by construction, and does — three refusals, in
order:**

1. the configured identity is `""` (no bound lane) → nothing to heal under;
2. `populated_identity ≠ configured` → this is the binding-change case, not
   drift;
3. `status = 'reembedding'` → a fleet-wide run already holds the marker.

That marker read happens **per workspace, inside the workspace's own pass** —
not once for the fleet — so a reindex claimed (or a binding swapped) after the
dispatcher enumerated stops the remaining workspaces rather than racing the
fleet-wide job. The sweep never writes the marker at all.

The run the confirm starts is `embed_reindex` (a dispatcher with **no tick** —
a reindex is a human's confirm, never a cadence) fanning out to
`embed_reindex_workspace` (queue `ai_capture`, five attempts, no wall-clock
timeout). What makes it *one* run is the marker: the confirm claims it under a
freshly minted run id, the dispatcher seeds the pending workspace set and
enqueues the children in the same transaction, each child leaves the set at a
terminal outcome, and the child that empties it hands the marker back. Every
marker write **fences on the run id**, so a straggler of a finished run cannot
move the marker of the run that replaced it. Three consequences worth knowing:

- **A mid-flight binding change is detected, not absorbed.** The job carries the
  identity in force at claim time; a worker whose live embedder no longer matches
  it fails with `ErrIdentityDrift` → `river.JobCancel`, because what the fleet
  needs is a *new* run under the current config, not this row's remaining
  attempts.
- **`populated_identity` means "the identity the last run was released under"** —
  not "every workspace was re-embedded under it". A run releases when no
  workspace has an outcome left to reach, and *exhausted attempts* is one of
  those outcomes. `/readyz`'s `active` inherits exactly that weaker claim; the
  pending counts on the status endpoint are what tell an operator the difference.
- **A stuck marker has a way back.** A discarded or cancelled child can leave the
  marker held with nothing running (a workspace job is exempt from River's
  rescuer by declaring no timeout). A **forced** confirm steals a marker whose
  last progress is older than an hour (`reembedStaleAfter`); a healthy pass
  refreshes that timestamp around every leg of its own work, which is what makes
  the window meaningful — and what makes "last progress N ago" a real signal in
  the settings card.

### Why the banner keys on the mismatch alone

`frontend/src/app/embedreindexbanner.tsx` renders when — and only when —
`configured_identity !== populated_identity`. It is deliberately **not**
qualified by `status`, even though a banner shown during a healthy rebuild is
redundant: suppressing it while `status = "reembedding"` would also hide a
rebuild whose job was drift-cancelled or attempt-discarded and left the marker
stuck, which is precisely the state the settings card's force-rebuild recovery
affordance exists for. A redundant banner during a healthy rebuild is the cheaper
failure.

Identity-*matched* pending entities are not the banner's business either — the
drift sweep is already fixing them, and a banner about drift the system is
repairing trains an admin to ignore the banner. The pending detail lives in the
settings card (`frontend/src/screens/embedreindex.tsx`) instead. The banner's own
status read is gated client-side on `embedding_reindex:read` — the same grant the
server checks, asked *before* issuing a query that could only 403.

## The context graph

`GET /v1/records/{entity_type}/{id}/context` assembles the picture *around* one
record rather than a ranked list of records — the affordance the AI layers and
the `catch_me_up_on` / `prep_for_meeting` intent tools consume through the same
`retrieval` seam, every item provenance-stamped.

The walk is **fixed-depth by construction**, two joins rather than a traversal
that can wander: anchor profile → the anchor's linked activities (hop 1, split
into `recent_touches` and `open_tasks`) → those activities' *other* link targets
(hop 2, emitted as `related_people` / `related_organizations` / `related_deals`).
Every leg reads at most 50 rows before ranking trims to `max_items` (default 5,
capped at 25), so an anchor with thousands of links costs about what one with
fifty costs. Anchors are the four non-activity searchable types the contract's
path enum names — `person`, `organization`, `deal`, `lead` — derived from
`searchBranches` rather than kept as a parallel list; an activity is a link, not
a thing links hang off. A `lead` has no `activity_link` neighborhood at all, so
its context is honestly its profile alone.

Ranking follows the retrieval-ranking weights
`0.60·similarity + 0.30·recency + 0.10·source_trust` with an id-ascending
tie-break; recency halves every 30 days, and source trust ladders `manual` 1.0 >
`mcp` 0.7 > captured/connector content 0.4. Graph items carry no query
similarity — there is no query — so their rank is recency × trust over the same
weights.

A **person** anchor additionally carries a `who_knows` section: which colleagues
actually interact with this contact, warmest first, each with its band and
interaction count so a model handed the list cannot just pick the first name.
That section reads the `graph_interaction_edge` projection, which is its own
mechanism with its own maintenance rules — see
[relationship-graph.md](relationship-graph.md).

## Reference

| Concern | Where |
|---|---|
| Lexical index | generated `search_tsv` columns on `person`, `organization`, `deal`, `activity`, `lead`, `project` (migrations `0004`–`0009`, `0131`); linguistics `0052`, apostrophe folding `0077` |
| Vector store | `embedding` (migration `0022`; identity stamp + unbounded `vector` + corpus wipe in `0114`) — tenant table, RLS ENABLE+FORCE |
| Binding marker | `embed_store_binding` (`0114`, run/identity/pending-set fan-out shape in `0174`) — **non-tenant**, no `workspace_id`, no RLS |
| Relationship projection | `graph_interaction_edge` (`0158`) — see [relationship-graph.md](relationship-graph.md) |
| RBAC object | `embedding_reindex` (`read`/`update`, admin + ops only; backfilled by `0115`) |
| Job kinds | `embed_drift_sweep` → `embed_drift_workspace` (periodic, 15m) · `embed_reindex` → `embed_reindex_workspace` (on demand) — declared in `backend/api/jobs.yaml` |
| Routes | `GET /v1/search` · `GET /v1/records/{entity_type}/{id}/context` · `GET /v1/embeddings/reindex/status` · `GET /v1/embeddings/reindex/preview` · `POST /v1/embeddings/reindex` |
| Conflict codes on confirm | `reindex_running` · `reindex_not_needed` · `reindex_identity_drift` (all 409) |
| `/readyz` embed line | `active` · `needs_reindex` · `reembedding` · `unknown` (no lane, or the marker read failed) — never gates readiness |
| Agent access | `/v1/search` is a 🟢 `search_records` read under a passport; the context walk and all three reindex operations are `x-agent-access: human-only` |
| Knobs (embed binding, `dimensions`, reindex operations, provider caveats) | [../reference/configuration.md](../reference/configuration.md) |

## Rules of thumb

- **A search hit is a read.** Authority is compiled into the query, never applied
  to its results — so a denied entity type contributes no branch, and a row-scope
  miss answers 404 rather than 403.
- **Never rank across embed identities.** Every vector read filters to the
  current `provider/model@dims`; a row from an older binding is hidden, not
  served at the wrong width.
- **The vector lane degrades, it does not error.** No embedder, an unbound
  identity, or a zero query vector falls back to the lexical arm — a search that
  returns fewer kinds of answer beats one that returns none.
- **Drift heals itself; a binding change asks a human.** An entity pending under
  the *current* identity is a lost event and the sweep re-embeds it unprompted. A
  *changed* binding is a spend decision, so it keeps preview → confirm.
- **Correctness never waits on a reindex.** A half-rebuilt store hides stale rows
  rather than ranking them, so the system is always honest mid-rebuild.

## Where the code lives

| | |
|---|---|
| Lexical query + keyset cursor | `internal/modules/search/store.go` (`Search`, `searchBranches`, `branchScope`) |
| Vector write + similarity read | `internal/modules/search/embedding.go` (`UpsertEmbedding`, `SimilarEntities`) |
| RRF fusion | `internal/modules/search/fuse.go` (`HybridSearch`, `fuseRankedResults`, `rrfK`) |
| Event-driven indexer | `internal/modules/search/embedgen.go` (`EmbedGen`, `embedText`) |
| Drift sweep (store half) | `internal/modules/search/driftsweep.go` (`SweepWorkspaceEmbeddingDrift`, `healEntity`) |
| Drift sweep (jobs) | `internal/compose/embeddriftsweep.go` |
| Binding marker + pending scan | `internal/modules/search/binding.go` (`SeedBinding`, `PopulatedIdentity`, `ReindexNeeded`, `pendingSources`) |
| Reindex run (store half) | `internal/modules/search/reembed.go` (`ReembedWorkspace`, `ErrIdentityDrift`) |
| Reindex transport + jobs | `internal/compose/embedreindextransport.go`, `internal/compose/jobs_embedreindex.go` |
| Readiness line | `internal/compose/embedreadyz.go` |
| Context graph walk | `internal/modules/search/graph.go`, `handlers_context.go` |
| Retrieval seam | `internal/shared/ports/retrieval` ← `internal/modules/search/retriever.go` |
| SPA surfaces | `frontend/src/app/embedreindexbanner.tsx` (advisory banner) · `frontend/src/screens/embedreindex.tsx` (settings card, preview/confirm/rebuild) |

## Where to go next

[authorization.md](authorization.md) (the gate every branch calls) ·
[ai-runtime.md](ai-runtime.md) (where the embed lane's model comes from) ·
[write-backbone.md](write-backbone.md) (the outbox the indexer consumes) ·
[relationship-graph.md](relationship-graph.md) ·
[../reference/configuration.md](../reference/configuration.md).
