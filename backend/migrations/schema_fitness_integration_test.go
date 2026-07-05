// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// Catalog-derived fitness functions over the migrated schema: the RLS
// coverage, composite-tenant-FK, and row-scoped-FK-visibility invariants
// are each derived from the DATABASE, not from a hand-maintained list —
// a new table or FK is enrolled the moment the migration creates it.
// Shares dsns/connect/resetSchema/migrateAll with
// schema_integration_test.go.

import (
	"context"
	"testing"
)

// TestRLS_coversEveryTenantTable is the fitness function for the "RLS on
// every tenant table" invariant: 0014 enrols tables from a hand-written
// list, and a hand-written list rots — a future migration that adds a
// workspace_id table but forgets the enrolment would ship without RLS,
// silently. Here the DATABASE is the source of truth: every base table
// carrying a workspace_id column must have ENABLE + FORCE row security
// and at least one policy, or this test names the stragglers.
func TestRLS_coversEveryTenantTable(t *testing.T) {
	ownerDSN, _ := dsns(t)
	owner := connect(t, ownerDSN)
	resetSchema(t, owner)
	migrateAll(t, owner)
	ctx := context.Background()

	rows, err := owner.Query(ctx, `
		SELECT c.relname,
		       c.relrowsecurity,
		       c.relforcerowsecurity,
		       EXISTS (SELECT 1 FROM pg_policies p
		               WHERE p.schemaname = 'public' AND p.tablename = c.relname)
		FROM pg_class c
		WHERE c.relnamespace = 'public'::regnamespace
		  AND c.relkind = 'r'
		  AND EXISTS (SELECT 1 FROM pg_attribute a
		              WHERE a.attrelid = c.oid AND a.attname = 'workspace_id' AND NOT a.attisdropped)
		ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("querying tenant tables: %v", err)
	}
	defer rows.Close()

	tenantTables := 0
	for rows.Next() {
		var name string
		var enabled, forced, hasPolicy bool
		if err := rows.Scan(&name, &enabled, &forced, &hasPolicy); err != nil {
			t.Fatal(err)
		}
		tenantTables++
		if !enabled || !forced {
			t.Errorf("table %s carries workspace_id but RLS is enable=%v force=%v — enrol it in the RLS migration", name, enabled, forced)
		}
		if !hasPolicy {
			t.Errorf("table %s has RLS flags but NO policy — it would deny everything, or worse, a later DISABLE would open it", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Vacuous-pass guard: the schema has dozens of tenant tables; finding
	// almost none means the detection query broke, not that the schema shrank.
	if tenantTables < 30 {
		t.Fatalf("found only %d workspace_id tables — the fitness query no longer sees the schema", tenantTables)
	}
}

// TestFK_tenantLocalReferencesAreComposite is the fitness function for the
// same-workspace-FK invariant (C4, data-model tenancy integrity): RLS
// bounds row VISIBILITY, but a plain `owner_id -> app_user(id)` FK does not
// prove the target lives in the SAME workspace — a bad app path, import
// job, or guessed UUID can plant a cross-tenant reference that passes the
// FK. Every FK from one workspace_id table to another must therefore carry
// workspace_id on both sides, so the database rejects a cross-workspace
// target by construction. Here the DATABASE is the source of truth: any
// tenant-local FK that omits workspace_id from its key is named. Exceptions
// (a FK to workspace(id) itself, the tenant root) are excluded.
func TestFK_tenantLocalReferencesAreComposite(t *testing.T) {
	ownerDSN, _ := dsns(t)
	owner := connect(t, ownerDSN)
	resetSchema(t, owner)
	migrateAll(t, owner)
	ctx := context.Background()

	// For each FK constraint whose owning table AND referenced table both
	// carry workspace_id, assert 'workspace_id' is among the referencing
	// columns. (A composite FK that includes workspace_id on the left must
	// include it on the right too — Postgres matches by position against the
	// referenced unique key — so checking the left side is sufficient.)
	rows, err := owner.Query(ctx, `
		WITH tenant_tables AS (
			SELECT c.oid, c.relname
			FROM pg_class c
			WHERE c.relnamespace = 'public'::regnamespace AND c.relkind = 'r'
			  AND EXISTS (SELECT 1 FROM pg_attribute a
			              WHERE a.attrelid = c.oid AND a.attname = 'workspace_id' AND NOT a.attisdropped)
		)
		SELECT con.conname, src.relname, ref.relname,
		       EXISTS (
		         SELECT 1 FROM unnest(con.conkey) AS k
		         JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k
		         WHERE a.attname = 'workspace_id'
		       ) AS includes_workspace
		FROM pg_constraint con
		JOIN tenant_tables src ON src.oid = con.conrelid
		JOIN tenant_tables ref ON ref.oid = con.confrelid
		WHERE con.contype = 'f'
		ORDER BY src.relname, con.conname`)
	if err != nil {
		t.Fatalf("querying tenant-local FKs: %v", err)
	}
	defer rows.Close()

	fks := 0
	for rows.Next() {
		var name, srcTable, refTable string
		var composite bool
		if err := rows.Scan(&name, &srcTable, &refTable, &composite); err != nil {
			t.Fatal(err)
		}
		fks++
		if !composite {
			t.Errorf("FK %s (%s -> %s) omits workspace_id — make it composite (workspace_id, <col>) REFERENCES %s(workspace_id, id) so a cross-workspace target is rejected by the database",
				name, srcTable, refTable, refTable)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Vacuous-pass guard: the schema has dozens of tenant-local FKs.
	if fks < 25 {
		t.Fatalf("found only %d tenant-local FKs — the fitness query no longer sees the schema", fks)
	}
}

// TestFK_rowScopedTargetsHaveVisibilityDecision derives the H1 obligation
// from the schema: an FK argument that names a row-scoped business record
// (person/organization/deal/lead/activity) is a READ of that record, so
// every such column must carry an explicit decision — client-supplied
// references are gated by a target-visibility probe (auth.EnsureLinkTarget
// or the activity link walk), server-derived pointers and owned child rows
// are named as such. A new FK to a row-scoped table that nobody classified
// fails here, so the decision cannot be skipped silently.
func TestFK_rowScopedTargetsHaveVisibilityDecision(t *testing.T) {
	// The classification. Values are prose for the reader; the map's
	// completeness is the invariant.
	decisions := map[string]string{
		// Client-supplied references — visibility-gated at the store:
		"deal.organization_id":          "gated: auth.EnsureLinkTarget in CreateDeal/UpdateDeal (H1)",
		"deal.partner_org_id":           "gated: auth.EnsureLinkTarget in UpdateDeal (H1)",
		"organization.parent_org_id":    "gated: auth.EnsureLinkTarget in Create/UpdateOrganization (H1)",
		"activity_link.person_id":       "gated: auth.EnsureLinkTarget in LogActivity",
		"activity_link.organization_id": "gated: auth.EnsureLinkTarget in LogActivity",
		"activity_link.deal_id":         "gated: auth.EnsureLinkTarget in LogActivity",
		// Owned child rows: the row is an attribute of its visible parent,
		// written only through the parent's own gated paths.
		"activity_link.activity_id":           "child row: written only inside LogActivity for the new activity",
		"consent_event.person_id":             "child row: written through the person's own gated paths",
		"organization_domain.organization_id": "child row: written through the organization's own gated paths",
		"person_email.person_id":              "child row: written through the person's own gated paths",
		"person_phone.person_id":              "child row: written through the person's own gated paths",
		"person_consent.person_id":            "child row: written through the person's own gated paths",
		"consent_doi_token.person_id":         "child row: minted and consumed only inside RecordConsent's gated path",
		// Server-derived pointers: stamped from an operation's outcome,
		// never accepted from the request body.
		"lead.promoted_person_id":       "server-derived: stamped by PromoteLead",
		"person.merged_into_id":         "server-derived: stamped by MergePerson",
		"organization.merged_into_id":   "server-derived: stamped by MergeOrganization",
		"person.converted_from_lead_id": "server-derived: stamped by PromoteLead",
		"deal_stage_history.deal_id":    "server-derived: appended by CreateDeal/AdvanceDeal",
		// Client-supplied edge endpoints — every one probed at the store:
		"relationship.person_id":           "gated: auth.EnsureLinkTarget in CreateRelationship (H1)",
		"relationship.counterparty_org_id": "gated: auth.EnsureLinkTarget in CreateRelationship (H1)",
		"relationship.organization_id":     "gated: auth.EnsureLinkTarget in CreateRelationship (H1)",
		"relationship.deal_id":             "gated: auth.EnsureLinkTarget in CreateRelationship (H1)",
		"partner.organization_id":          "gated: auth.EnsureLinkTarget in UpsertPartner (H1)",
	}

	ownerDSN, _ := dsns(t)
	owner := connect(t, ownerDSN)
	resetSchema(t, owner)
	migrateAll(t, owner)
	ctx := context.Background()

	rows, err := owner.Query(ctx, `
		SELECT c.conrelid::regclass::text AS src_table, a.attname AS src_col,
		       c.confrelid::regclass::text AS target_table
		FROM pg_constraint c
		JOIN unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord) ON true
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		WHERE c.contype = 'f'
		  AND c.confrelid::regclass::text IN ('person','organization','deal','lead','activity')
		  AND a.attname <> 'workspace_id'
		ORDER BY 1, 2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var srcTable, srcCol, target string
		if err := rows.Scan(&srcTable, &srcCol, &target); err != nil {
			t.Fatal(err)
		}
		key := srcTable + "." + srcCol
		seen[key] = true
		if _, decided := decisions[key]; !decided {
			t.Errorf("FK %s -> %s has no visibility decision: a reference to a row-scoped record is a read of it — gate it (auth.EnsureLinkTarget) or classify it here", key, target)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// The map must not carry dead entries either — a renamed column would
	// otherwise keep a stale decision alive forever.
	for key := range decisions {
		if !seen[key] {
			t.Errorf("decision map entry %s matches no FK in the live schema — remove or fix it", key)
		}
	}
}
