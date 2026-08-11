-- The installation's identity lives in `setting`, not on the workspace row
-- (ADR-0090/A135, ADR-0091 phase 4). Every reader moved across #521; these
-- three columns are the last of the dual write, and the mirror in
-- identity.UpdateInstallation retires with them.
--
-- Neither table needs a workspace binding here: `setting` is non-tenant by
-- design (0190) and `workspace` is outside RLS (0014), which is what lets the
-- repair below read one to write the other.

-- 1. Repair. 0191's backfill only ran where exactly one live workspace
-- existed, so a database that migrated with several has no rows at all. That
-- was survivable while the columns still answered; it is not survivable across
-- a DROP. Backfill anything still missing from the one live workspace.
INSERT INTO setting (key, value)
SELECT 'installation.name', to_jsonb(w.name)
  FROM workspace w
 WHERE w.archived_at IS NULL
   AND (SELECT count(*) FROM workspace WHERE archived_at IS NULL) = 1
ON CONFLICT (key) DO NOTHING;

INSERT INTO setting (key, value)
SELECT 'installation.timezone', to_jsonb(w.timezone)
  FROM workspace w
 WHERE w.archived_at IS NULL
   AND (SELECT count(*) FROM workspace WHERE archived_at IS NULL) = 1
ON CONFLICT (key) DO NOTHING;

INSERT INTO setting (key, value)
SELECT 'installation.base_currency', to_jsonb(rtrim(w.base_currency))
  FROM workspace w
 WHERE w.archived_at IS NULL
   AND (SELECT count(*) FROM workspace WHERE archived_at IS NULL) = 1
ON CONFLICT (key) DO NOTHING;

-- 2. Refuse rather than lose. If a live workspace still holds identity the
-- settings do not, dropping the columns would destroy the only copy — and the
-- ONE state the repair above cannot resolve is the one it is guarded on:
-- several live workspaces, where no single row can speak for the installation.
-- An operator resolves that down to one (ADR-0061 §3) and runs this again.
DO $$
DECLARE missing text; live int;
BEGIN
  SELECT count(*) INTO live FROM workspace WHERE archived_at IS NULL;
  IF live > 0 THEN
    SELECT string_agg(k, ', ') INTO missing
      FROM unnest(ARRAY['installation.name','installation.timezone','installation.base_currency']) AS k
     WHERE NOT EXISTS (SELECT 1 FROM setting WHERE setting.key = k);
    IF missing IS NOT NULL THEN
      RAISE EXCEPTION
        'installation identity would be lost: % has no stored setting, and this migration drops the column holding it. '
        'Resolve the installation down to exactly one live workspace (ADR-0061 §3) and re-run.', missing;
    END IF;
    -- Presence is not enough. With several live workspaces the settings speak
    -- for ONE installation while the others keep rows — deals and invoices
    -- carrying an fx_rate_to_base frozen against a base about to stop
    -- existing anywhere. The API already refuses to serve that state
    -- (ErrMultipleWorkspaces); this refuses to make it unrecoverable.
    IF live > 1 THEN
      RAISE EXCEPTION
        '% live workspaces: the installation settings can only speak for one of them, and dropping these '
        'columns would leave the others'' rows priced against a base nothing records. '
        'Resolve the installation down to exactly one live workspace (ADR-0061 §3) and re-run.', live;
    END IF;
  END IF;
END $$;

-- 3. Drop. The base_currency CHECK (workspace_base_currency_iso, 0002) goes
-- with its column; the settings entry carries the same rule in Go now.
ALTER TABLE workspace
  DROP COLUMN name,
  DROP COLUMN base_currency,
  DROP COLUMN timezone;
