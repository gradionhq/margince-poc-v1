-- 20260730120000_flip_and_import_run: the overlay→native flip substrate
-- (fork-owned, ADR-0017 custom namespace).
--
-- import_run is the migration engine's run record (IEM-DDL-1 arrival
-- shape): one row per import run, carrying the per-run mapping, the
-- resumable checkpoint cursor, and the report reference. The connector
-- enum carries 'mirror' beyond the chapter's four values — the flip runs
-- the engine against the frozen mirror snapshot (OVA-WIRE-8), a source
-- IEM-DDL-1 never named; disclosed upstream as a spec-fill.
CREATE TABLE import_run (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  connector text NOT NULL CHECK (connector IN ('csv','hubspot','salesforce','bundle','mirror')),
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending','validating','awaiting_approval','running','complete','failed')),
  mapping jsonb NOT NULL DEFAULT '{}'::jsonb,   -- effective object kind + column map, per run
  source_ref text NOT NULL,                     -- connector-specific source context (snapshot id, bundle ref)
  -- The run report (disposition + skips + disclosures) inline: IEM-DDL-1
  -- pins a blobstore report_ref, but no blob substrate is wired here yet —
  -- the ref column lands with the direct importer under the IEM-GAP-2
  -- contract extension, which owns reconciling the two run-record shapes.
  report jsonb NULL,
  error text NULL,                              -- why a failed run stopped (resumable, not a dead end)
  checkpoint integer NOT NULL DEFAULT 0,        -- absolute offset into source rows; 0 = not started
  source text NOT NULL,                         -- provenance (DM-CONV-11)
  captured_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_import_run_ws ON import_run (workspace_id, status);

ALTER TABLE import_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE import_run FORCE ROW LEVEL SECURITY;
CREATE POLICY import_run_tenant_isolation ON import_run
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The flip's freeze/seal state (B-E18.26): the preflight seals a frozen
-- mirror snapshot only while every readiness check is green, and any
-- blocker unseals it again (UC-E18-04 F1 — a failed preflight is a no-op
-- return to a healthy overlay). Lives on overlay_sync_state (one row per
-- overlay workspace) rather than a new table: the seal is sync-lifecycle
-- state, and ADR-0071 mints no new table.
ALTER TABLE overlay_sync_state ADD COLUMN flip_snapshot_id text NULL;
ALTER TABLE overlay_sync_state ADD COLUMN mirror_frozen_at timestamptz NULL;
