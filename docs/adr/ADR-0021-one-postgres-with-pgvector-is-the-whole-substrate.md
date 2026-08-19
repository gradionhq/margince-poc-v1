# ADR-0021 — One Postgres with pgvector holds the context graph; a graph database is not on the roadmap

**Status:** Active
**Decided:** 2026-06-17

## The decision

Relationships, full-text search and vector search all live in one PostgreSQL
instance: real foreign keys, a typed `relationship` table, `tsvector` for text
and `pgvector` for embeddings. We add no second datastore. A dedicated graph
database is adopted only if a named, measured condition fires, and only after
the tuning ladder — covering indexes, cached read models, precomputed results,
partitioning — is exhausted. A guess that we might one day want graph queries is
never enough.

## Why

The queries this product actually runs are shallow: fixed-depth joins over one
or two hops with aggregation, on an edge set that is close to bipartite. The
patterns that reward a native graph engine — finding paths of arbitrary length,
community detection, centrality — are not in scope. A second datastore is a
permanent operational cost that buys nothing at this size, and it would put
customer data in a second place that the self-hosting and data-residency story
then has to account for.

## What it binds in this repository

- `backend/migrations/core/0022_embeddings.up.sql` creates the `vector`
  extension and the `embedding` table: one row per entity and chunk, keyed by
  content hash so unchanged text is never re-embedded, with an HNSW index for
  cosine search. Vector width is 1024, matching the default embedding lane.
- `backend/migrations/core/0007_relationship.up.sql` creates the `relationship`
  table — the typed edges that are the graph.
- `backend/internal/modules/search/` holds the whole substrate in one module:
  `embedding.go` and `reembed.go` for vectors, `graph.go`, `graphedge.go` and
  `graphactivity.go` for traversal, `fuse.go` for combining the two, and
  `retriever.go` as the entry point.
- `backend/internal/modules/search/perfbench.go` and `perfbench_test.go` are the
  benchmark that makes the trigger measurable rather than a matter of opinion.
  `make bench-perf` runs the performance lane; `make perfdoc` records it.
- Later migrations extend the same store rather than moving it:
  `0114_embedding_identity`, `0115_embedding_reindex_rbac`,
  `0174_embed_reindex_fanout`.
- A deployment binding an embedder of a different width alters the column in a
  custom migration; mixed widths in one store cannot rank against each other.

## History

Adopted from the retired specification, decided 2026-06-17. Rewritten in plain
language 2026-08-19. This record named the trigger that an earlier decision left
open with the words "only if multi-hop reasoning at scale proves insufficient".
The source states four conditions, any one of which opens a new decision record:
context-graph assembly latency above 300 ms at the 95th percentile for a
mid-market installation; a committed feature needing variable-depth traversal of
three hops or more that a recursive query cannot serve inside that budget; a
committed query with whole-graph fan-out that tuning cannot rescue; or an
installation crossing roughly five million relationship edges or fifty million
activity links while the benchmark shows the budget breached. The numbers are
calibration starting values, to be revised in the open by amending this record
rather than adjusted quietly.
