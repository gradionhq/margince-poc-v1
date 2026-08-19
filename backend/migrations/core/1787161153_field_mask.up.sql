-- Field masks: the columns a role reads as withheld. Until now the masks were
-- a dead key in role.permissions that nothing read; this is the authoritative
-- table, and enforcement follows in platform/auth (MaskedFields) and the
-- stores that map a row onto the wire.
--
-- A mask is keyed by ROLE KEY, not role id: the system roles exist once per
-- installation under fixed keys, so a seed can name them without knowing
-- their ids, and a custom role carrying the same key inherits the rule.
--
-- condition says when the mask holds: `always`, or `outside_write_authority`
-- — the caller may read the record but it is not theirs to change, so the
-- sensitive column is withheld while the rest stays readable. That second
-- condition is what makes a workspace-readable deal safe to show every rep:
-- they see the deal exists and who owns it; the amount is the owner's team's.
SET LOCAL lock_timeout = '3s';

CREATE TABLE field_mask (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  role_key   text NOT NULL,
  object     text NOT NULL,
  field      text NOT NULL,
  condition  text NOT NULL CHECK (condition IN ('always', 'outside_write_authority')),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (role_key, object, field)
);

-- The one mask the shared-identity decision makes load-bearing: a rep reads
-- every deal in the workspace, and another team's amount is not theirs.
INSERT INTO field_mask (role_key, object, field, condition)
VALUES ('rep', 'deal', 'amount_minor', 'outside_write_authority');
