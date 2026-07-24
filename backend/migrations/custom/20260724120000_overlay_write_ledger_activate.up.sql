-- 20260724120000_overlay_write_ledger_activate (OVA-DDL-6 / OVA-AC-3): wire the
-- reserved overlay_write_ledger for echo suppression, and add the mirror-halt
-- flag the value-hash collision guard raises.
--
-- The ledger stores, per property we wrote back, the SHA-256 value-hash
-- (OVA-PARAM-4 — the fast index/identity of the written value) AND the
-- canonicalized value itself. The value is required, not redundant: a hash
-- collision is by definition two DIFFERENT values sharing one hash, so hash-only
-- storage cannot detect one — the stored value is what lets the consumer confirm
-- an inbound value that HASHES like our write actually EQUALS it (echo) versus
-- differs (a collision ⇒ flag + halt, never a silent mis-suppression). It is a
-- 24h-bounded (OVA-PARAM-3), workspace-scoped duplicate of already-mirrored
-- same-tenant data — no new PII category — purged with the mirror on teardown.
ALTER TABLE overlay_write_ledger ADD COLUMN IF NOT EXISTS value_canonical text NOT NULL DEFAULT '';

-- The spec keys the ledger on (object, external_id, property, value-hash): a
-- rapid A→B write-back must keep A's entry open (until A's echo is recognized)
-- rather than clobbering it, so value_hash joins the primary key. The reserved
-- table's PK omitted it; widen it here (the table has never been populated).
ALTER TABLE overlay_write_ledger DROP CONSTRAINT overlay_write_ledger_pkey;
ALTER TABLE overlay_write_ledger
  ADD PRIMARY KEY (workspace_id, object_class, external_id, property, value_hash);

-- overlay_mirror_halt is the fail-safe the collision guard sets (OVA-AC-3 /
-- UC-E18-02 F2): a detected value-hash collision flags the workspace's mirror as
-- halted, and both the webhook receiver and the re-fetch worker then refuse to
-- process further signals for it — never silently mis-suppressing a real
-- external change. The flag is cleared today only by disconnect/teardown (which
-- purges the whole overlay); an operator-facing unhalt is a tracked follow-up.
-- One row per workspace (the halt is workspace-wide).
CREATE TABLE overlay_mirror_halt (
  workspace_id uuid PRIMARY KEY REFERENCES workspace(id) ON DELETE RESTRICT,
  reason text NOT NULL,
  detected_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE overlay_mirror_halt ENABLE ROW LEVEL SECURITY;
ALTER TABLE overlay_mirror_halt FORCE ROW LEVEL SECURITY;
CREATE POLICY overlay_mirror_halt_tenant_isolation ON overlay_mirror_halt
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- No opened_at index: the consumer's Classify looks up by the full primary key
-- (workspace_id, object_class, external_id, property, value_hash) and then
-- narrows on opened_at on that single row, and the genuine-change DELETE hits
-- the PK prefix — both served by the PK index. A window-scan index belongs with
-- the periodic pruner that would use it (a tracked follow-up), not ahead of it.
