# ADR-0069 — The embedding width is operator config, and a rebind is a governed reindex

**Status:** Active — the config key, the unbounded column, the identity filter,
the reindex routes and the drift sweep are built. Deleting the vectors a
superseded binding leaves behind is **not built**; see below.

**Decided:** 2026-07-22

## The decision

The width of an embedding vector is the operator's choice, set as
`embeddings.dimensions` in the routing file, between 1 and 2000, defaulting to
1536. It is validated when the router is built, so a bad value stops both the
API and worker roles at boot. The 2000 ceiling is what pgvector can index.

The database column is an unbounded `vector` and stores mixed widths. Each row
carries the real embedding identity as `provider/model@dims`, and every
retrieval query filters on the identity currently configured. Rows from an
older binding are excluded before any distance is computed, so a width change
needs no migration — a config edit and a restart.

Changing the binding never re-embeds automatically. The system says so in the
log, on the readiness endpoint and in an operator banner, then waits. An
administrator previews the cost, confirms, and a background job repopulates the
corpus. Correctness never depends on that job running. Losing an embedding
under an unchanged binding is a different event and heals itself: a periodic
worker sweep re-embeds what the at-least-once event bus dropped, through the
ordinary embed path, with no human in the loop.

## Why

A fixed width hides the failure it is supposed to prevent. Swap a binding for a
different model of the same width and every dimension still lines up, so
nothing errors — the results are just quietly wrong, ranked in the old model's
space against new-space queries. Stamping the identity and filtering on it
turns that silence into a visible exclusion.

Re-embedding a whole corpus is one model call per entity, paid on the
customer's own provider bill, so an operator has to agree to it. A lost embed
event is the ordinary per-entity embed that already runs on every edit with no
consent step. Making a person press a button to finish work the system already
owed only leaves the work undone.

## What it binds in this repository

- `backend/migrations/core/0022_embeddings.up.sql` created the store with a
  fixed `vector(1024)` column. `0114_embedding_identity.up.sql` widened it to
  an unbounded `vector`, dropped the unused index, and added the
  `embed_store_binding` marker: one row, no tenant column, naming the identity
  the store holds and whether a rebuild is running.
  `0115_embedding_reindex_rbac.up.sql` backfills the `embedding_reindex`
  permission object — read and update for the admin and ops roles only, nothing
  for anyone else, with the confirm route marked human-only in the contract.
  `0174_embed_reindex_fanout.up.sql` reshaped the marker to track which run
  owns it and which workspaces that run still owes.
- `Dimensions` is parsed and range-checked in
  `backend/internal/modules/ai/routing.go`; `config/ai-routing.schema.json`
  bounds it at 2000, and `config/ai-routing.example.yaml` shows it in use.
- `backend/internal/modules/search/binding.go` owns the marker and the
  claim-and-release around a run; `reembed.go` and `pending.go` do the work.
  `driftsweep.go` is the self-healing sweep, and runs only when the configured
  identity matches the populated one and no fleet-wide run is live.
- The routes `/embeddings/reindex/status`, `/embeddings/reindex/preview` and
  `/embeddings/reindex` in `backend/api/crm.yaml` are wired in
  `backend/internal/compose/embedreindextransport.go`, estimated by
  `costestimate/embedreindex.go`, run by `jobs_embedreindex.go`, and reported
  by `embedreadyz.go`; `embeddriftsweep.go` schedules the sweep. The estimate
  is advisory and never blocks, and it discloses the effect on the workspace's
  own AI utilisation next to the money figure.


## What is owed

A binding swap leaves old-identity vectors in the table. They are filtered out
of every retrieval and never ranked, but they are still stored and can hold
personal data. Two cases: a live entity whose swap is not yet confirmed, and an
entity archived before the swap, which the reindex job skips because it walks
only live entities. Both go on erasure or when retention deletes the row. The
follow-up the original record named is a retention sweep that deletes rows
whose identity no longer matches the populated one. Nothing in the tree
implements it, and its design is not final.

## History

Adopted from the retired specification, decided 2026-07-22. Rewritten in plain
language 2026-08-19.

Amended 2026-08-01 after the build showed the failure it addresses: 42 entities
lost their embeddings to acknowledged-but-unwritten events under an unchanged
binding, and the only recovery was a person pressing a button. The amendment
split the signal into its two causes and let the matched half heal itself. The
record also declined an open question: no catalogue of model capabilities is
pulled from providers to discover embedding widths, because the width is a
property of the operator's binding and lives in the routing file.
