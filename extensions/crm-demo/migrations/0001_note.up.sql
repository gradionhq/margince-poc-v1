-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- crm-demo's one table, and the file every next unit author copies — so it
-- says what is actually true of it in each of the two places it runs.
--
-- IN THE PRE-MERGE GATE (backend/tools/extmigrategate, `make
-- check-ext-migrations`) it is applied as a restricted ext_crm_demo role minted
-- against a throwaway database, holding CREATE on the ext schema and nothing on
-- public beyond REFERENCES (id) on workspace. That is what keeps every line
-- below to the narrowest shape such a role can produce, and the gate re-reads
-- the resulting catalog to prove it.
--
-- AT RUNTIME there is NO ext_crm_demo role. cmd/migrate opens ONE
-- margince_owner connection and issues no SET ROLE, so this table is created
-- and owned by margince_owner exactly as every core table is. Do not read the
-- gate's restriction as a production DDL boundary: what isolates this table in
-- production is the FORCE row level security and the workspace-bound policy
-- declared below, not its ownership. backend/migrations/core/0202_ext_schema
-- states the same thing at the schema, and issue #628 tracks minting the
-- per-unit runtime role that would make ownership mean something here.
--
-- The name is ext_crm_demo_note, not note: the ext schema is shared by every
-- installed unit, so the unit namespace is what keeps two of them from
-- colliding or addressing each other's rows.

CREATE TABLE ext.ext_crm_demo_note (
    id           uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    -- The tenant claim, and the column the policy below compares. The cascade
    -- is load-bearing rather than tidy: without it, deleting a workspace fails
    -- on this unit's rows, so erasing a tenant would stop at the first
    -- installed extension.
    workspace_id uuid        NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    body         text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- ENABLE and FORCE, both, and FORCE is the load-bearing one HERE rather than a
-- belt-and-braces habit: the owner at runtime is margince_owner (see the header),
-- and ENABLE alone exempts a table's owner from its own policies. Without FORCE
-- the isolation would hold for margince_app and not for the role that owns the
-- table — which is also the role every migration and every operator psql session
-- arrives as.
ALTER TABLE ext.ext_crm_demo_note ENABLE ROW LEVEL SECURITY;
ALTER TABLE ext.ext_crm_demo_note FORCE ROW LEVEL SECURITY;

-- Exactly one policy, permissive, ALL commands, to PUBLIC. A second
-- permissive policy ORs with this one and can only widen it, and USING
-- without WITH CHECK would admit writes into another workspace.
CREATE POLICY ext_crm_demo_note_tenant_isolation ON ext.ext_crm_demo_note
    USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The app role runs the unit's handlers, under the policy above. TRUNCATE is
-- deliberately absent: it empties every tenant's rows without consulting the
-- policy's USING clause.
--
-- Note what this grant is NOT, since every unit issues one: margince_app is the
-- SHARED runtime role, so this line gives it to every unit's handlers, not only
-- to crm-demo's. Within one workspace, another installed unit's SQL can read and
-- write this table. That is inside the tier's trusted-unit threat model (see
-- backend/pkg/extension/runtime.go) and it is what #628's per-unit role would
-- close; it is stated here because this file is the template.
GRANT SELECT, INSERT, UPDATE, DELETE ON ext.ext_crm_demo_note TO margince_app;
