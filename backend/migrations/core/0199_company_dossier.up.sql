-- 0199 — the company dossier and its growth-fit assessment (DOSS-DDL-1/2).
--
-- TWO READ-MODEL CACHES, OWNED BY THE COMPOSITION LAYER. Both are assemblies of
-- facts owned elsewhere — profile fields, extracted facts, the source inventory
-- — so neither carries an audit row or an outbox event, and a full rebuild is
-- the remedy for any corruption. There is no business entity here to lose.
--
-- THE READER IS PART OF THE PRIMARY KEY, and the reader predicate is written
-- explicitly into every read rather than left to row-level security, which binds
-- the workspace and not the reader (DOSS-DDL-N-1/N-2).
--
-- Two independent reader-dependencies force that, and only one of them could be
-- summarized. A field mask is role-scoped and could be reduced to a signature,
-- so a per-company cache keyed by that signature was considered. Row scope over
-- CITED records cannot be: a claim's evidence labels name files and activities,
-- those records are scoped own/team/shared per reader, and a shared assembly
-- would disclose that such a record EXISTS to a reader who may not see it —
-- which is the disclosure DOSS-AC-11 forbids, and existence is the part row
-- scoping protects. No stable signature summarizes a reader's scope over an
-- open-ended cited set, so the safe key is the reader. The account brief already
-- pays this price for the same reason.

CREATE TABLE org_dossier (
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  -- The reader this assembly was generated FOR. Never a filter applied after
  -- the read: an assembly generated for one reader is never served to another,
  -- whatever their masks (DOSS-AC-N-2).
  user_id         uuid NOT NULL,
  organization_id uuid NOT NULL,
  -- Everything that could change the content: the assembled factual input, the
  -- prompt version and the model routing version (DOSS-PARAM-5). A change in any
  -- of the three invalidates, so a prompt revision cannot serve yesterday's
  -- assembly beside today's (DOSS-AC-14).
  fingerprint     text NOT NULL,
  -- Sections, sentences, natures and citations. Shaped by the wire contract
  -- rather than by columns: the payload is rendered whole and never queried
  -- into, and a column per section would pin the section vocabulary in DDL.
  payload         jsonb NOT NULL,
  -- Which lane produced it, or the deterministic floor. The surface says which,
  -- because a plainer answer from the floor is honest and a plainer answer
  -- passed off as the model's is not (DOSS-AC-7).
  generated_by    text NOT NULL,
  generated_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, user_id, organization_id),
  -- COMPOSITE references, carrying workspace_id into the key. A single-column
  -- reference lets a row name a user or a company in ANOTHER workspace: the
  -- target exists, so the database accepts it, and the only thing standing
  -- between that row and a cross-tenant read is application code remembering to
  -- filter. Carrying the workspace into the key makes the database refuse it.
  CONSTRAINT org_dossier_user_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT org_dossier_org_fkey FOREIGN KEY (workspace_id, organization_id)
    REFERENCES organization (workspace_id, id) ON DELETE CASCADE
);

CREATE TABLE org_growth_fit (
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  -- Per reader for the dossier's reasons AND one of its own: growth fit folds
  -- seat-dependent workspace context and makes recommendations.
  user_id         uuid NOT NULL,
  organization_id uuid NOT NULL,
  fingerprint     text NOT NULL,
  -- Band, completeness with both counts, factors, whitespace, angle, objections.
  payload         jsonb NOT NULL,
  generated_by    text NOT NULL,
  generated_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, user_id, organization_id),
  CONSTRAINT org_growth_fit_user_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT org_growth_fit_org_fkey FOREIGN KEY (workspace_id, organization_id)
    REFERENCES organization (workspace_id, id) ON DELETE CASCADE
);

-- Both are tenant tables, so both take FORCE row-level security with the
-- deny-on-unset semantics every other one carries: an unbound app.workspace_id
-- resolves the policy to NULL and sees nothing, rather than seeing everything.
ALTER TABLE org_dossier ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_dossier FORCE ROW LEVEL SECURITY;
CREATE POLICY org_dossier_ws ON org_dossier
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE org_growth_fit ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_growth_fit FORCE ROW LEVEL SECURITY;
CREATE POLICY org_growth_fit_ws ON org_growth_fit
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The composite FKs above cascade on organization delete, and Art. 17 erasure
-- deletes accounts. The primary key leads with user_id, so nothing supports
-- those FKs: without these the cascade sequentially scans both tables once per
-- deleted organization, and at one row per reader per company that is the whole
-- table each time. Migration 0143 adds the same index to org_brief for the same
-- reason.
CREATE INDEX org_dossier_organization_ix ON org_dossier (workspace_id, organization_id);
CREATE INDEX org_growth_fit_organization_ix ON org_growth_fit (workspace_id, organization_id);

COMMENT ON TABLE org_dossier IS
  'Read-model cache (DOSS-DDL-1). Keyed per READER: no assembly crosses readers, whatever their masks.';
COMMENT ON COLUMN org_dossier.user_id IS
  'The reader this assembly was generated for. Written into every read explicitly — RLS binds the workspace, not the reader.';
COMMENT ON TABLE org_growth_fit IS
  'Read-model cache (DOSS-DDL-2). Per reader: it folds seat-dependent workspace context and makes recommendations.';
