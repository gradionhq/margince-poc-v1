-- Installation settings as rows (ADR-0090/A135). One row per setting; the
-- catalog — type, default, validator, RBAC object, audit verb, freeze probe —
-- lives in typed Go (platform/settings), not in this schema. Adding a setting
-- must not cost DDL, which is the whole point: 0121_capture_auto_enrich was an
-- ALTER TABLE plus an RBAC backfill for one boolean.
--
-- NON-TENANT, deliberately: no workspace_id, no RLS, on the same footing as
-- embed_store_binding (0114). One installation serves one organization and
-- refuses to start with more (ADR-0061 §3), so an installation setting is
-- per-installation by definition. ADR-0091 retires the tenant boundary
-- outright; this table is built without it rather than acquiring a column now
-- only to drop it then.
--
-- Consequence worth naming where a reader will meet it: with no RLS here, the
-- platform/auth object gate at the writer is the ONLY control on this table.
-- That gate is mandatory and covered by rbacgate_test.go — but nothing
-- backstops it.
CREATE TABLE setting (
  key        text        PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$'),
  value      jsonb       NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- The key shape is enforced here as well as in the fitness gate because the
-- `<module>.<name>` prefix is what makes ownership legible: a bare key would
-- read as belonging to nobody. The gate proves the prefix names a real module;
-- this CHECK only proves it has one.

COMMENT ON TABLE setting IS
  'Installation settings, one row per setting (ADR-0090/A135). Non-tenant by design. The catalog is platform/settings; an unregistered key here is a fitness-test failure.';

-- Carry the one setting that already had a home. workspace.capture_auto_enrich
-- (0121) is the first column to move; its value moves with it, so an
-- installation that turned auto-enrich OFF does not silently get it back.
--
-- Written only when it DIFFERS from the registered default (true): an unset
-- setting reads as its default, so seeding a row that merely restates the
-- default would add a row saying nothing and make "has anyone ever changed
-- this?" unanswerable.
--
-- The column itself is deliberately NOT dropped here. Readers switch first,
-- the drop follows once nothing reads it — which keeps this migration
-- reversible without a data-restoring down.
INSERT INTO setting (key, value)
SELECT 'capture.auto_enrich', to_jsonb(w.capture_auto_enrich)
  FROM workspace w
 WHERE w.capture_auto_enrich IS DISTINCT FROM true
 ORDER BY w.created_at
 LIMIT 1
    ON CONFLICT (key) DO NOTHING;
