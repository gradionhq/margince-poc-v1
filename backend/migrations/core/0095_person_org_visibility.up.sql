-- Connector-created records start owner-visible until a human promotes them
-- (ADR-0063): visibility is a row property — 'workspace' (everyone in the
-- workspace, today's behavior and the default) or 'owner' (the capturing
-- user only, enforced by the row-scope clauses in platform/auth).
-- quarantined_at marks impersonation-suspect rows (punycode/homoglyph,
-- display-name↔email mismatch) held for the 🟡 review queue.

ALTER TABLE person ADD COLUMN visibility text NOT NULL DEFAULT 'workspace'
  CHECK (visibility IN ('workspace','owner'));
ALTER TABLE person ADD COLUMN quarantined_at timestamptz NULL;

ALTER TABLE organization ADD COLUMN visibility text NOT NULL DEFAULT 'workspace'
  CHECK (visibility IN ('workspace','owner'));
ALTER TABLE organization ADD COLUMN quarantined_at timestamptz NULL;
