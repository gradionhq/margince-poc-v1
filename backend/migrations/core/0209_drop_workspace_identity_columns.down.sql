-- Restore the columns and refill them from the settings rows, which are the
-- source of truth from 0190 onward. NOT NULL is re-established only after the
-- backfill, so an installation whose settings were never written comes back
-- with the same defaults 0002 declared rather than failing the rollback.
ALTER TABLE workspace
  ADD COLUMN name text,
  ADD COLUMN base_currency char(3),
  ADD COLUMN timezone text;

UPDATE workspace SET
  name = coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.name'), 'Organization'),
  timezone = coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.timezone'), 'UTC'),
  base_currency = coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.base_currency'), 'EUR');

ALTER TABLE workspace
  ALTER COLUMN name SET NOT NULL,
  ALTER COLUMN base_currency SET NOT NULL,
  ALTER COLUMN base_currency SET DEFAULT 'EUR',
  ALTER COLUMN timezone SET NOT NULL,
  ALTER COLUMN timezone SET DEFAULT 'UTC',
  ADD CONSTRAINT workspace_base_currency_iso CHECK (base_currency ~ '^[A-Z]{3}$');
