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
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// TestRLS_coversEveryTenantTable is the fitness function for the "RLS on
// every tenant table" invariant: 0014 enrols tables from a hand-written
// list, and a hand-written list rots — a future migration that adds a
// workspace_id table but forgets the enrolment would ship without RLS,
// silently. Here the DATABASE is the source of truth: every base table
// carrying a workspace_id column must have ENABLE + FORCE row security
// and at least one policy, or this test names the stragglers.
// rlsExemptTables are the ratified non-RLS workspace_id tables. Every
// entry carries its rationale inline — an entry without one is a
// finding, and a stale entry (table gone or since enrolled) fails too.
var rlsExemptTables = gatekit.Waive(map[string]string{
	"booking_page":     "the slug→tenant RESOLVER (0036): it is read to discover which workspace to bind BEFORE any GUC exists, exactly like the workspace table itself (data-model §1.2); it carries no CRM record data — slug, workspace, host, revocation only",
	"preference_token": "the token→tenant RESOLVER (0048): the no-login preference center / RFC 8058 unsubscribe reads it to discover which workspace to bind BEFORE any GUC exists, exactly like booking_page; it carries no CRM record data beyond the person link + revocation",
})

func TestRLS_coversEveryTenantTable(t *testing.T) {
	defer rlsExemptTables.AssertAllMatched(t)

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
		  AND c.relkind IN ('r','p')
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
		if rlsExemptTables.Waived(t, name) {
			if enabled || forced {
				t.Errorf("table %s is RLS-exempt by rationale but HAS row security — retire the stale exemption", name)
			}
			continue
		}
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
			WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r','p')
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

// TestSchema_amountMinorBaseIsDatabaseGenerated is the fitness function for
// the formula-field boundary invariant (RD-AC-6, 0065): deal.amount_minor_base
// must be a database GENERATED column, never an application-computed or
// hand-maintained one — the DATABASE is the source of truth, so a future
// migration that quietly re-added it as a plain writable bigint (letting a
// write path set it directly) fails here rather than surviving unnoticed.
// The generation expression itself is checked structurally (both formula
// inputs are named), not restated verbatim, so the test stays robust to a
// harmless whitespace/parenthesization change in a later migration.
func TestSchema_amountMinorBaseIsDatabaseGenerated(t *testing.T) {
	ownerDSN, _ := dsns(t)
	owner := connect(t, ownerDSN)
	resetSchema(t, owner)
	migrateAll(t, owner)
	ctx := context.Background()

	var isGenerated, generationExpr string
	if err := owner.QueryRow(ctx, `
		SELECT is_generated, generation_expression
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'deal' AND column_name = 'amount_minor_base'`,
	).Scan(&isGenerated, &generationExpr); err != nil {
		t.Fatalf("querying deal.amount_minor_base from information_schema: %v", err)
	}
	if isGenerated != "ALWAYS" {
		t.Errorf("deal.amount_minor_base has information_schema.columns.is_generated=%q, want ALWAYS", isGenerated)
	}
	for _, want := range []string{"amount_minor", "fx_rate_to_base"} {
		if !strings.Contains(generationExpr, want) {
			t.Errorf("deal.amount_minor_base's generation expression %q does not reference %q", generationExpr, want)
		}
	}

	// pg_attribute is a second, independent catalog: attgenerated = 's' is
	// Postgres's STORED-generated marker (the only kind it currently
	// supports), cross-checking the information_schema view above.
	var attgenerated string
	if err := owner.QueryRow(ctx, `
		SELECT attgenerated FROM pg_attribute
		WHERE attrelid = 'deal'::regclass AND attname = 'amount_minor_base' AND NOT attisdropped`,
	).Scan(&attgenerated); err != nil {
		t.Fatalf("querying pg_attribute for deal.amount_minor_base: %v", err)
	}
	if attgenerated != "s" {
		t.Errorf("deal.amount_minor_base has pg_attribute.attgenerated=%q, want \"s\" (STORED)", attgenerated)
	}
}

// TestSchema_organizationOpenPipelineRollupIsSecurityInvoker closes the
// RD-AC-N-1 half of the same boundary proof: the cross-record roll-up MUST
// run as security_invoker (inheriting the caller's own RLS), never as the
// view owner's elevated privilege — a view created without the option, or
// with it later stripped by a careless CREATE OR REPLACE, would silently
// leak every workspace's pipeline total to every other workspace.
func TestSchema_organizationOpenPipelineRollupIsSecurityInvoker(t *testing.T) {
	ownerDSN, _ := dsns(t)
	owner := connect(t, ownerDSN)
	resetSchema(t, owner)
	migrateAll(t, owner)
	ctx := context.Background()

	var reloptions []string
	if err := owner.QueryRow(ctx, `
		SELECT COALESCE(reloptions, '{}') FROM pg_class
		WHERE relname = 'organization_open_pipeline_rollup' AND relnamespace = 'public'::regnamespace`,
	).Scan(&reloptions); err != nil {
		t.Fatalf("querying pg_class.reloptions for organization_open_pipeline_rollup: %v", err)
	}
	found := false
	for _, opt := range reloptions {
		if opt == "security_invoker=true" {
			found = true
		}
	}
	if !found {
		t.Errorf("organization_open_pipeline_rollup view reloptions %v do not include security_invoker=true", reloptions)
	}
}

// rowScopedFKDecisions is the classification: one entry per FK column
// naming a row-scoped business record. Values are prose for the reader;
// the map's completeness is the invariant.
var rowScopedFKDecisions = gatekit.Waive(map[string]string{
	// Client-supplied references — visibility-gated at the store:
	"site_read.organization_id":            "gated: auth.EnsureVisible in StartSiteRead (the one human entry point); Begin/Finish only re-address a row Start created, and GetSiteRead re-checks EnsureVisible on every read",
	"deal.organization_id":                 "gated: auth.EnsureLinkTarget in CreateDeal/UpdateDeal (H1)",
	"project.organization_id":              "gated: auth.EnsureLinkTarget in CreateProject/UpdateProject (H1) — the anchor company is client-supplied, so naming it is a read of it",
	"deal.partner_org_id":                  "gated: auth.EnsureLinkTarget in UpdateDeal (H1)",
	"organization.parent_org_id":           "gated: auth.EnsureLinkTarget in Create/UpdateOrganization (H1)",
	"activity_link.person_id":              "gated: auth.EnsureLinkTarget in LogActivity",
	"activity_link.organization_id":        "gated: auth.EnsureLinkTarget in LogActivity",
	"activity_link.deal_id":                "gated: auth.EnsureLinkTarget in LogActivity",
	"activity_link.lead_id":                "gated: auth.EnsureLinkTarget in LogActivity",
	"activity_link.project_id":             "gated: auth.EnsureLinkTarget in LogActivity — the link target is probed by its wire entity_type, so project rides the same gate as its siblings",
	"deal.project_id":                      "gated: auth.EnsureLinkTarget in CreateDeal/UpdateDeal (H1) — the anchor project is client-supplied, so naming it is a read of it",
	"lead.project_id":                      "gated: auth.EnsureLinkTarget in CreateLead/UpdateLead (H1)",
	"suggestion_dismissal.organization_id": "gated: auth.EnsureVisible in org360.Service.DismissSuggestion, inside the same transaction as the insert — dismissing advice about an account the caller cannot read would confirm it exists",
	"org_dossier.organization_id":          "gated: the dossier is assembled only after orgdossier.Service.Get runs the caller's OWN sidecar reads, and people.ListOrganizationProfileFields opens with auth.Require + ensureOrgReadable — a company the caller cannot read has no dossier written for it, and the row is keyed on that same caller",
	"org_growth_fit.organization_id":       "gated: same path as org_dossier — the assessment is written only after the caller's own gated sidecar reads succeed, and the row is keyed on that caller",
	"org_brief.organization_id":            "gated: the brief is written only after orgbrief.Service.Get runs the caller's own org360 Assemble, whose GetOrganizationTx does auth.Require + auth.EnsureVisible — an account the caller cannot read has no brief written for it, and the row is keyed on that same caller",
	// Owned child rows: the row is an attribute of its visible parent,
	// written only through the parent's own gated paths.
	"activity_link.activity_id": "child row: written only inside LogActivity for the new activity",
	// comms_outbound is delivery machinery for one activity, not a
	// standalone record (comms/doc.go): StageTx writes only inside the
	// caller's own transaction, alongside the activity write it reports on
	// (comms/store.go's StageInput doc). comms.Store carries no RBAC gate of
	// its own (see the internal/modules/comms waivers in rbacgate_test.go)
	// because the send action is admitted at that shared-transaction
	// activity write, not inside comms. Both send transports stage through
	// that one path and pass the activity id they just created, never an
	// externally-supplied reference.
	"comms_outbound.activity_id":                     "child row: written only inside the caller's own transaction, alongside the activity write it reports on",
	"consent_event.person_id":                        "child row: written through the person's own gated paths",
	"organization_domain.organization_id":            "child row: written through the organization's own gated paths",
	"organization_relationship_type.organization_id": "child row: written through the organization's own gated paths (the patch that sets relationship types, and the partner upsert)",
	// The disposition NAMES the organization its own verdict created, in the
	// same transaction that created it. There is no client-supplied reference
	// to gate: nothing outside the triage resolve ever writes this column, and
	// no human surface reads the row.
	"organization_domain_disposition.organization_id": "server-derived: set only by ResolveDomainTriage, to the organization that same transaction created or adopted through the gated dedupe chokepoint",
	"person_email.person_id":                          "child row: written through the person's own gated paths",
	"person_phone.person_id":                          "child row: written through the person's own gated paths",
	// telegram-oa design §6.4: the channel-aware ensure contract creates the
	// Person (owner_id NULL) and this identity satellite in the same
	// transaction, from the inbound message's own channel principal —
	// never from a client-supplied person_id.
	"person_channel_identity.person_id": "child row: written through the channel-aware ensure path alongside the person it resolves or creates",
	"person_consent.person_id":          "child row: written through the person's own gated paths",
	"person_consent.lead_id":            "gated: auth.EnsureVisible on the lead subject in consent Record (E12.20)",
	"consent_event.lead_id":             "gated: auth.EnsureVisible on the lead subject in consent Record (E12.20); proof rows append only inside that path",
	"consent_doi_token.person_id":       "child row: minted and consumed only inside RecordConsent's gated path",
	"preference_token.person_id":        "gated: auth.EnsureVisible on the recipient in PreferenceTokenForEmail — the id is server-derived from the send path's RLS-scoped email→person resolve, and the minted token is a bearer credential over that person, so the mint carries the same row-scope probe the sibling read does; the public surface reads the row as the token→tenant resolver before any principal exists",
	// Server-derived pointers: stamped from an operation's outcome,
	// never accepted from the request body.
	"lead.promoted_person_id":          "server-derived: stamped by PromoteLead",
	"person.merged_into_id":            "server-derived: stamped by MergePerson",
	"organization.merged_into_id":      "server-derived: stamped by MergeOrganization",
	"person.converted_from_lead_id":    "server-derived: stamped by PromoteLead",
	"deal_stage_history.deal_id":       "server-derived: appended by CreateDeal/AdvanceDeal",
	"project_phase_history.project_id": "server-derived: appended by CreateProject/AdvanceProjectPhase from the project row they just wrote or advanced, never from a request body",
	"brief_item.deal_id":               "server-derived: written only by the brief ranker from its own row-scoped candidate query, never from a request body",
	// The capture disposition ledger (CAP-DDL-8): capture writes the row in
	// the same transaction as the activity it just created, from that
	// activity's own id — a connector principal supplies message bytes, never
	// a record reference.
	"capture_pending_counterparty.activity_id": "server-derived: stamped by the capture Sink from the activity it just wrote",
	// Client-supplied edge endpoints — every one probed at the store:
	"relationship.person_id":                     "gated: auth.EnsureLinkTarget in CreateRelationship (H1)",
	"relationship.counterparty_org_id":           "gated: auth.EnsureLinkTarget in CreateRelationship (H1)",
	"relationship.organization_id":               "gated: auth.EnsureLinkTarget in CreateRelationship (H1)",
	"relationship.deal_id":                       "gated: auth.EnsureLinkTarget in CreateRelationship (H1)",
	"relationship.project_id":                    "gated: auth.EnsureLinkTarget on the project anchor in CreateRelationship (H1)",
	"partner.organization_id":                    "gated: auth.EnsureLinkTarget in UpsertPartner (H1)",
	"organization_profile_field.organization_id": "server-derived: the coldstart accept executor resolves the org from the staged source URL, never from a request body",
	"organization_fact.organization_id":          "child rows written only through the deepread accept effect, whose approval was staged from a visibility-checked read",
	"offer.deal_id":                              "gated: auth.EnsureLinkTarget in CreateOffer; every later offer read/write re-probes the deal (H1)",
	"offer.buyer_org_id":                         "gated: auth.EnsureLinkTarget in CreateOffer/UpdateOffer (H1)",
	"signal.resolved_org_id":                     "gated: the resolver attributes only to a caller-visible org (visibleCandidates → auth.EnsureLinkTarget)",
	"signal.resolved_person_id":                  "gated: consentedPerson links only a caller-visible person (auth.EnsureLinkTarget); else company-level",
	"signal_resolution.matched_org_id":           "child row: written only through Resolve's gated attribution — the org already passed auth.EnsureLinkTarget",
	"person_social.person_id":                    "child row: written only through the person store — CreatePerson mints the parent row itself, UpdatePerson passes auth.EnsureVisible first",
	// The dedupe review queue (DH-DDL-1): pair ids are server-derived —
	// recordDedupeCandidate stamps them from the ensure chokepoint's own
	// row-scoped fuzzy query, never from a request body; the disposition
	// endpoints address the candidate row, not the pair ids.
	"dedupe_candidate.left_person_id":           "server-derived: stamped by recordDedupeCandidate from the dedupe sweep's own row-scoped match query",
	"dedupe_candidate.right_person_id":          "server-derived: stamped by recordDedupeCandidate from the dedupe sweep's own row-scoped match query",
	"dedupe_candidate.left_org_id":              "server-derived: stamped by recordDedupeCandidate from the dedupe sweep's own row-scoped match query",
	"dedupe_candidate.right_org_id":             "server-derived: stamped by recordDedupeCandidate from the dedupe sweep's own row-scoped match query",
	"person_profile_field.person_id":            "server-derived: the enrich pass resolves the person from its own row-scoped connector-activity query (PO-DDL-12), never from a request body",
	"capture_auto_enrich_state.organization_id": "server-derived: the auto-enrich sweep keys the cursor on an org id its own row-scoped ListDueOrgs read produced (CAP-PARAM-7), never from a request body",
	// The signature pass's read cursor (PO-F-2a): both ids come from the
	// pass's own SignatureCandidates query — the person it just read for and
	// the activity whose body it just read — never from a request body.
	"person_signature_enrich_state.person_id":   "server-derived: stamped by the enrich pass from its own row-scoped candidate query",
	"person_signature_enrich_state.activity_id": "server-derived: stamped by the enrich pass from the activity that candidate query returned",
	// The interaction participants (ACT-DDL-3): neither id is ever carried on
	// a request body. Capture mints the activity in the same transaction and
	// resolves the counterparty through the ensure chokepoint's own row-scoped
	// lookup; a manual activity takes its person from a link the activities
	// store already put through auth.EnsureLinkTarget. Reads inherit the
	// activity's own visibility (the link walk), so a participant row never
	// discloses an activity its reader could not already open.
	"activity_participant.activity_id": "child row: written only beside the activity itself, inside the transaction that mints it",
	"activity_participant.person_id":   "server-derived: the counterparty the ensure chokepoint resolved, or a link the activities store already gated",
	// The interaction projection (CG-DDL-1) holds no fact of its own: every
	// row is folded from activity_participant rows by the consumer, and no
	// request body ever names a person here. Reads of it carry the person
	// predicate, so an edge never discloses a contact the caller cannot open.
	"graph_interaction_edge.person_id":        "derived projection: folded from participant rows by the graph-edge consumer, never written from a request",
	"activity_participant_replay.activity_id": "job bookkeeping: written by the system-principal replay pass sweeping every activity in the workspace, never from a request. The row records THAT an original was re-read and what the parse found — it returns no record to any caller and discloses nothing about the activity it names",
	// The LinkedIn ghost's match arms (CG-DDL-2). A ghost is not a record and
	// carries no client-supplied reference: the matcher resolves both ids from
	// its own row-scoped lookups, and a human confirming a suggestion
	// addresses the ghost row rather than naming a person.
	"linkedin_connection.matched_person_id": "server-derived: resolved by the ghost matcher's own row-scoped lookup, never from a request body",
	"linkedin_connection.matched_org_id":    "server-derived: resolved by the ghost matcher's own row-scoped lookup, never from a request body",
	// Cursor state, not a reference a reader follows: the account a producer
	// pass resolved for a conversation, compared for equality to decide whether
	// that conversation is owed a fresh reading. Resolved by the producer's own
	// three-arm walk inside a workspace transaction, never from a request body.
	"signal_thread_scan.resolved_org_id": "server-derived: the account the signal producer's own account walk resolved, never from a request body",
	// The finance mirror (FIN-DDL-2..4). Exactly ONE of these three is
	// client-supplied: the customer LINK is a human's mapping decision, so the
	// company it names is gated by auth.EnsureLinkTarget at the write, exactly
	// like an activity link. The invoice and payment rows never carry a
	// client-named company — the connector writes them, and it resolves the
	// organization by reading the link that human already made, so a mirrored
	// row can only land on a company somebody deliberately mapped.
	"finance_customer_link.organization_id": "schema only, no writer yet (#725): the mapping write does not exist, and when it lands it must put the named company through auth.EnsureLinkTarget — this entry is the obligation, not a record of one already met",
	"finance_invoice.organization_id":       "schema only, no writer yet (#725): the sync pass does not exist, and when it lands it must resolve the organization from the customer link rather than from any request body",
	"finance_payment.organization_id":       "schema only, no writer yet (#725): the sync pass does not exist, and when it lands it must resolve the organization from the customer link rather than from any request body",
})

// TestFK_rowScopedTargetsHaveVisibilityDecision derives the H1 obligation
// from the schema: an FK argument that names a row-scoped business record
// (person/organization/deal/lead/activity) is a READ of that record, so
// every such column must carry an explicit decision — client-supplied
// references are gated by a target-visibility probe (auth.EnsureLinkTarget
// or the activity link walk), server-derived pointers and owned child rows
// are named as such (in rowScopedFKDecisions). A new FK to a row-scoped
// table that nobody classified fails here, so the decision cannot be
// skipped silently.
func TestFK_rowScopedTargetsHaveVisibilityDecision(t *testing.T) {
	defer rowScopedFKDecisions.AssertAllMatched(t)

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
		  AND c.confrelid::regclass::text IN ('person','organization','deal','lead','activity','project')
		  AND a.attname <> 'workspace_id'
		ORDER BY 1, 2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	for rows.Next() {
		var srcTable, srcCol, target string
		if err := rows.Scan(&srcTable, &srcCol, &target); err != nil {
			t.Fatal(err)
		}
		key := srcTable + "." + srcCol
		if !rowScopedFKDecisions.Waived(t, key) {
			t.Errorf("FK %s -> %s has no visibility decision: a reference to a row-scoped record is a read of it — gate it (auth.EnsureLinkTarget) or classify it here", key, target)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
