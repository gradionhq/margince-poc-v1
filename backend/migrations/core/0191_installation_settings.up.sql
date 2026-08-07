-- The installation's own settings become rows (ADR-0090/A135): name, timezone
-- and base currency move off the `workspace` columns and into `setting`,
-- behind a new `installation_settings` RBAC object.
--
-- Two of the three were never reachable by a human at all. An installation
-- that mistyped its base currency or timezone in margince.yaml on day one had
-- no way to correct it through the product — the gap ADR-0085 §7 names, and
-- the reason this ships with a surface rather than only a store.
--
-- Note what this migration proves about ADR-0090's claim. Adding a SETTING now
-- costs no DDL — that is the point of 0190. Adding a new RBAC OBJECT still
-- costs a policy backfill, exactly as 0121 paid it, because the closed object
-- set is seeded per installation. Settings that reuse an existing object are
-- free; this one introduces its own.

-- Backfill `installation_settings` into EXISTING installations' seeded
-- system-role documents (new ones get it from identity/internal/policy).
-- Posture mirrors capture_settings: everyone READS — a rep reading amounts
-- benefits from knowing which currency they are in — only admin/ops UPDATE.
-- No create/delete: these are singleton values, not a record kind, so both
-- stay FALSE against any future generic create/delete path.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,installation_settings}',
      '{"create":false,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'installation_settings')
      AND role.workspace_id = ws;

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,installation_settings}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('manager','rep','read_only')
      AND NOT permissions->'objects' ? 'installation_settings')
      AND role.workspace_id = ws;
  END LOOP;
END $$;

-- Carry the live installation's values across.
--
-- Unlike 0190's toggle, these are written UNCONDITIONALLY rather than only
-- when they differ from the registered default. The defaults here are
-- placeholders — "" for the name, EUR for the currency — not the value any
-- real installation is running on, so falling back to them would silently
-- re-base every roll-up and blank the organization's name. What an
-- installation already chose IS the value, whatever it equals.
--
-- Scoped to THE live workspace — the same row the runtime resolves as the
-- installation (ADR-0061 §3) — and carried across only when exactly one
-- exists. A database holding several is not an installation yet, and picking
-- one would hand it an identity an operator never chose; the values stay unset
-- until they resolve it, which the API requires before it will serve at all.
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

-- base_currency is char(3), which Postgres blank-pads; trim before storing so
-- the value round-trips as the three letters the ISO-4217 validator expects
-- rather than a padded string that would fail it on the next write.
INSERT INTO setting (key, value)
SELECT 'installation.base_currency', to_jsonb(rtrim(w.base_currency))
  FROM workspace w
 WHERE w.archived_at IS NULL
   AND (SELECT count(*) FROM workspace WHERE archived_at IS NULL) = 1
    ON CONFLICT (key) DO NOTHING;

-- The columns stay for now: readers switch first, the drop follows once
-- nothing reads them (issue #521, which now covers all four).
