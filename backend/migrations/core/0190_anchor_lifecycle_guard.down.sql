-- Reverses 0190's GUARDS. The anchor becomes archivable and mergeable again.
--
-- What this does NOT undo: 0190 also cleared is_anchor from rows already
-- archived or merged away, and which row that was is not restored here. The
-- flag had answered nothing since the day such a row was retired — the resolver
-- and the partial unique index both ignore an archived anchor — so no behaviour
-- comes back either way, but the record of which organization it used to be
-- lives in audit_log rather than in this file.

DROP TRIGGER IF EXISTS organization_refuse_anchor_retirement ON organization;
DROP FUNCTION IF EXISTS organization_refuse_anchor_retirement();

ALTER TABLE organization
  DROP CONSTRAINT IF EXISTS organization_anchor_is_permanent;
