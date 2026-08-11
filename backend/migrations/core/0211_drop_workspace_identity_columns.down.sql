-- Restore the columns and refill them from the settings rows, which are the
-- source of truth from 0190 onward. NOT NULL is re-established only after the
-- backfill, so an installation whose settings were never written comes back
-- with fallback values chosen HERE ('Organization'/'UTC'/'EUR') rather than
-- failing the rollback — 0002 declared no default for name or base_currency,
-- so there is nothing to restore them to but a choice.
--
-- The up refuses to run with more than one live workspace, so the single set
-- of settings this reads genuinely speaks for the whole table.
ALTER TABLE workspace
  ADD COLUMN name text,
  ADD COLUMN base_currency char(3),
  ADD COLUMN timezone text;

UPDATE workspace SET
  name = coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.name'), 'Organization'),
  timezone = coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.timezone'), 'UTC'),
  base_currency = coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.base_currency'), 'EUR');

-- The CHECK is added NOT VALID and validated separately: a setting written by
-- raw SQL can hold something the 0002 constraint would reject, and a rollback
-- that aborted here would leave the columns added and the migration recorded
-- as un-run. NOT VALID admits the existing rows and still refuses new ones;
-- an operator repairs the value and runs VALIDATE CONSTRAINT.
--
-- No DEFAULT on base_currency: 0002 declared none, and inventing one here
-- would leave a rolled-back schema quietly accepting inserts the original
-- refused.
ALTER TABLE workspace
  ALTER COLUMN name SET NOT NULL,
  ALTER COLUMN base_currency SET NOT NULL,
  ALTER COLUMN timezone SET NOT NULL,
  ALTER COLUMN timezone SET DEFAULT 'UTC',
  ADD CONSTRAINT workspace_base_currency_iso CHECK (base_currency ~ '^[A-Z]{3}$') NOT VALID;
