-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion
--
-- notes's one table, and the file every next unit author copies — so it
-- says what is actually true of it in each of the two places it runs.
--
-- IN THE PRE-MERGE GATE (backend/tools/extmigrategate, `make
-- check-ext-migrations`) it is applied as a restricted ext_notes role minted
-- against a throwaway database, holding CREATE on the ext schema and nothing on
-- public beyond REFERENCES (id) on workspace. That is what keeps every line
-- below to the narrowest shape such a role can produce, and the gate re-reads
-- the resulting catalog to prove it.
--
-- AT RUNTIME there is NO ext_notes role. cmd/migrate opens ONE
-- margince_owner connection and issues no SET ROLE, so this table is created
-- and owned by margince_owner exactly as every core table is. Do not read the
-- gate's restriction as a production DDL boundary: what isolates this table in
-- production is the FORCE row level security and the workspace-bound policy
-- declared below, not its ownership. backend/migrations/core/0211_ext_schema
-- states the same thing at the schema, and issue #628 tracks minting the
-- per-unit runtime role that would make ownership mean something here.
--
-- The name is ext_notes_note, not note: the ext schema is shared by every
-- installed unit, so the unit namespace is what keeps two of them from
-- colliding or addressing each other's rows.
--
-- THE AUTHOR COLUMNS BELOW WERE ADDED TO THIS FILE IN PLACE, and that is an
-- EXCEPTION rather than the rule the sibling migrations state. 0002 and 0003
-- both say — correctly, and for every unit that has shipped — that an amended
-- 0001 never re-runs: dbmigrate keys on the version, so the change lands on
-- exactly the installations that did not need it and on none that do. That
-- reasoning holds; what makes it moot HERE is that this unit is still in heavy
-- development, backward compatibility is explicitly not required of it, and
-- every dev and UAT database carrying it is recreated rather than upgraded. A
-- unit that has been installed anywhere an operator would notice must take the
-- new number instead.

CREATE TABLE ext.ext_notes_note (
    id              uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    -- The tenant claim, and the column the policy below compares. The cascade
    -- is load-bearing rather than tidy: without it, deleting a workspace fails
    -- on this unit's rows, so erasing a tenant would stop at the first
    -- installed extension.
    workspace_id    uuid        NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    body            text        NOT NULL,
    -- WHO wrote it. Stamped by the handler from the invocation's Caller and
    -- never from the request body, because an author a client supplies is an
    -- author a client forges; the enforcing half of that is in note.go, this is
    -- only where it is kept.
    --
    -- NO FOREIGN KEY TO THE USER TABLE, and its absence is a property of the
    -- gate rather than an oversight. The ext_notes role this file is applied as
    -- holds REFERENCES (id) on workspace and NOTHING else on public, so a
    -- reference to a core user table here does not fail at review — it fails
    -- when the pre-merge gate applies the file, for every unit that copies this
    -- template. The id is therefore a plain uuid and the join, when a reader
    -- wants a name, is the core's to make. The practical cost is real and worth
    -- stating: nothing deletes these rows when the user is deleted, so an
    -- author_user_id can outlive the account it names and a reader must treat
    -- it as an id that may no longer resolve.
    --
    -- NULLABLE, because the tick has no author. A scheduled job's Caller is the
    -- zero value (CallerSystem, empty UserID) — there is no person behind it,
    -- and writing the zero uuid or a synthetic id would make "nobody wrote
    -- this" indistinguishable from a row whose author the reader simply cannot
    -- resolve.
    author_user_id  uuid,
    author_is_agent boolean,
    created_at      timestamptz NOT NULL DEFAULT now(),
    -- Both or neither. Split across two nullable columns, "an agent acting for
    -- nobody" (is_agent set, user id null) and "a person whose agent-ness is
    -- unknown" (the reverse) are both representable and neither means anything
    -- — so the database refuses them rather than leaving every reader to decide
    -- what a half-written author is. This is also what keeps the handler's
    -- both-or-neither stamping honest: getting it wrong is an error at the
    -- INSERT, not a row nobody notices.
    CONSTRAINT ext_notes_note_author_coherent
        CHECK ((author_user_id IS NULL) = (author_is_agent IS NULL))
);

-- ENABLE and FORCE, both, and FORCE is the load-bearing one HERE rather than a
-- belt-and-braces habit: the owner at runtime is margince_owner (see the header),
-- and ENABLE alone exempts a table's owner from its own policies. Without FORCE
-- the isolation would hold for margince_app and not for the role that owns the
-- table — which is also the role every migration and every operator psql session
-- arrives as.
ALTER TABLE ext.ext_notes_note ENABLE ROW LEVEL SECURITY;
ALTER TABLE ext.ext_notes_note FORCE ROW LEVEL SECURITY;

-- Exactly one policy, permissive, ALL commands, to PUBLIC. A second
-- permissive policy ORs with this one and can only widen it, and USING
-- without WITH CHECK would admit writes into another workspace.
CREATE POLICY ext_notes_note_tenant_isolation ON ext.ext_notes_note
    USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The app role runs the unit's handlers, under the policy above. TRUNCATE is
-- deliberately absent: it empties every tenant's rows without consulting the
-- policy's USING clause.
--
-- Note what this grant is NOT, since every unit issues one: margince_app is the
-- SHARED runtime role, so this line gives it to every unit's handlers, not only
-- to notes's. Within one workspace, another installed unit's SQL can read and
-- write this table. That is inside the tier's trusted-unit threat model (see
-- backend/pkg/extension/runtime.go) and it is what #628's per-unit role would
-- close; it is stated here because this file is the template.
GRANT SELECT, INSERT, UPDATE, DELETE ON ext.ext_notes_note TO margince_app;
