-- A project has no capture privacy: narrow project.visibility to 'workspace'.
--
-- 0131 gave `project` a visibility column with the same
-- CHECK (visibility IN ('workspace','owner')) that 0095 gave person and
-- organization. That was vocabulary copied wholesale, not a decision about
-- projects: 0131's own header says project "joins the canonical record
-- vocabulary … without a new mechanism", and this column came along with the
-- rest of it.
--
-- Capture privacy exists for ONE thing (0095's header says so): a connector
-- auto-creating a record from somebody's mailbox, held to that person until a
-- human promotes it. Nothing auto-creates a project. There is exactly one
-- writer of the table — the human CreateProject path in
-- modules/deals/project.go — and it does not name this column, so every
-- project that has ever existed is 'workspace'. The capture module reads
-- projects (modules/capture/projectscope.go) and never inserts one.
--
-- So the state is representable and unreachable, which is the worst of both:
-- the read path either carries a scope arm for a row that never occurs, or
-- ignores a column that legitimately means "private" and silently discloses
-- the first one anybody sets. Rather than teach the read path a state the
-- product does not have, the state is removed.
--
-- ONE-WAY DOOR, stated plainly: if a project ever SHOULD be private before it
-- is real, this CHECK is what has to be widened again, and the widening must
-- come with the read-path arm and the writer in the same change. Do not add a
-- writer that sets 'owner' without first reverting this migration — the
-- constraint will refuse the insert, which is the intended failure. The column
-- itself is kept (dropping a NOT NULL column is the less reversible move, and
-- it costs nothing to leave a constant behind).
--
-- Every live row is already 'workspace' by the argument above, so no backfill
-- can be needed; the UPDATE below is the belt to that braces, and it is
-- idempotent. It runs BEFORE the constraint so a database that somehow holds
-- an 'owner' project is repaired rather than failing the migration — losing
-- one boolean on a row nobody could read is better than a deployment that
-- cannot migrate.

-- The ALTERs below take ACCESS EXCLUSIVE on `project`. Bounding the wait means
-- one long-running transaction stalls this migration instead of stalling every
-- write to the table behind it (core/0139 explains the rule).
SET LOCAL lock_timeout = '3s';

UPDATE project SET visibility = 'workspace' WHERE visibility <> 'workspace';

ALTER TABLE project DROP CONSTRAINT IF EXISTS project_visibility_check;
ALTER TABLE project ADD CONSTRAINT project_visibility_check
  CHECK (visibility = 'workspace');
