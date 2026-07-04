-- 0022: the retrieval vector store (B-EP05.16, ADR-0021: single
-- Postgres + pgvector, no separate vector database). One row per
-- (entity, chunk): content-hash keyed so unchanged text is NEVER
-- re-embedded (ai-operational-spec §6), HNSW-indexed for cosine search.
-- Requires the pgvector image (pgvector/pgvector:pg16 in db-up).

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE embedding (
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  entity_type  text NOT NULL CHECK (entity_type IN ('person','organization','deal','lead','activity')),
  -- Polymorphic target, deliberately no FK (same stance as the audit
  -- log): a stale embedding row is pruned by the owning entity's
  -- lifecycle, not by referential cascade across five tables.
  entity_id    uuid NOT NULL,
  chunk_ix     int  NOT NULL DEFAULT 0,
  chunk_hash   text NOT NULL,
  model        text NOT NULL,
  -- 1024 matches the default embed lane (bge-m3 and the offline fake).
  -- A deployment binding a different-width embedder alters this column
  -- in a custom migration; mixed widths in one store cannot rank.
  embedding    vector(1024) NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, entity_type, entity_id, chunk_ix)
);

CREATE INDEX idx_embedding_hnsw ON embedding USING hnsw (embedding vector_cosine_ops);

-- Tenant table ⇒ RLS, same deny-on-unset policy as every other
-- (the coverage fitness test refuses a workspace_id table without it).
ALTER TABLE embedding ENABLE ROW LEVEL SECURITY;
ALTER TABLE embedding FORCE ROW LEVEL SECURITY;
CREATE POLICY embedding_tenant_isolation ON embedding
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
