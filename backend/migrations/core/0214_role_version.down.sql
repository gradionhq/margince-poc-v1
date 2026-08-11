-- Restore the plain updated_at trigger BEFORE dropping the column: the
-- version-bumping variant assigns NEW.version, so leaving it attached to a table
-- with no version column would make every later UPDATE of a role fail at run
-- time rather than at migration time.
DROP TRIGGER IF EXISTS trg_role_updated ON role;
CREATE TRIGGER trg_role_updated BEFORE UPDATE ON role
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE role DROP COLUMN IF EXISTS version;
