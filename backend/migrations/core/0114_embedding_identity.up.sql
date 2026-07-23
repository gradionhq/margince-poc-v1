-- ADR-0068/#1074: embeddings become identity-stamped and width-configurable.
-- Pre-production, disposable derived data: wipe legacy "embed-lane" rows and widen the column.
TRUNCATE embedding;
DROP INDEX idx_embedding_hnsw;                              -- dead weight: SimilarEntities seq-scans
ALTER TABLE embedding ALTER COLUMN embedding TYPE vector;  -- unbounded: any width, no future ALTER

-- Deployment-level binding marker. NON-tenant (no workspace_id, no RLS) — the ai_call_config
-- posture (0100_ai_call_attempts). Records what the store is populated under + the job lifecycle.
CREATE TABLE embed_store_binding (
  singleton          boolean     PRIMARY KEY DEFAULT true CHECK (singleton),
  populated_identity text        NOT NULL,   -- provider/model@dims; updated ONLY by job completion
  status             text        NOT NULL DEFAULT 'idle' CHECK (status IN ('idle','reembedding')),
  updated_at         timestamptz NOT NULL DEFAULT now()
);
