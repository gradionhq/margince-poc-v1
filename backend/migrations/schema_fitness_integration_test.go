// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations_test

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
	headSchema(t, owner)
	ctx := context.Background()

	var isGenerated, generationExpr string
	if err := owner.QueryRow(
		ctx, `
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
	if err := owner.QueryRow(
		ctx, `
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
	headSchema(t, owner)
	ctx := context.Background()

	var reloptions []string
	if err := owner.QueryRow(
		ctx, `
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
	"site_read.organization_id":       "gated: auth.EnsureVisible in StartSiteRead (the one human entry point); Begin/Finish only re-address a row Start created, and GetSiteRead re-checks EnsureVisible on every read",
	"deal.organization_id":            "gated: auth.EnsureLinkTarget in CreateDeal/UpdateDeal (H1)",
	"project.organization_id":         "gated: auth.EnsureLinkTarget in CreateProject/UpdateProject (H1) — the anchor company is client-supplied, so naming it is a read of it",
	"deal.partner_org_id":             "gated: auth.EnsureLinkTarget in UpdateDeal (H1)",
	"commission_entry.deal_id":        "gated: auth.EnsureLinkTarget in accrueTx — an entry priced against a deal the caller cannot open would be unreadable the moment it was written",
	"commission_entry.partner_org_id": "gated: auth.EnsureLinkTarget in accrueTx — naming the partner an entry pays is a read of that organization",
	"organization.parent_org_id":      "gated: auth.EnsureLinkTarget in Create/UpdateOrganization (H1)",
	"activity_link.person_id":         "gated: auth.EnsureLinkTarget in LogActivity",
	"activity_link.organization_id":   "gated: auth.EnsureLinkTarget in LogActivity",
	"activity_link.deal_id":           "gated: auth.EnsureLinkTarget in LogActivity",
	"activity_link.lead_id":           "gated: auth.EnsureLinkTarget in LogActivity",
	"activity_link.project_id":        "gated: auth.EnsureLinkTarget in LogActivity — the link target is probed by its wire entity_type, so project rides the same gate as its siblings",
	"deal.project_id":                 "gated: auth.EnsureLinkTarget in CreateDeal/UpdateDeal (H1) — the anchor project is client-supplied, so naming it is a read of it",
	"deal_document_hide.deal_id":      "gated: auth.EnsureWritable(deal) in activities.setDealDocumentHidden — the deal is the route's own {id}, and hiding a file from its Files area changes what that deal lists, so the caller must be able to change the deal, not merely see it; the attachment half is then checked against THIS caller's view of the area, so a miss of either reads as not-found",
	"deal_room.deal_id":               "gated: auth.EnsureWritableLive in createRoomTx — STRONGER than the EnsureLinkTarget its siblings take, deliberately. The deal a room is opened on is client-supplied, so naming it is a read of it; but opening a room also starts showing that deal to an outside party, which visibility alone does not authorize. A room on a deal the caller could merely see would publish that deal's existence, and its editorial text, to buyers",
	"contract.organization_id":        "gated: auth.EnsureLinkTarget in createContractTx (H1) — the counterparty is client-supplied, so naming it is a read of it",
	// The deal and project links carry a SECOND obligation the sibling columns
	// above do not, and it is the reason this table's gate is not just a copy.
	// A contract's row visibility is INHERITED from its deal (falling back to
	// its organization), so a deal belonging to another company would publish
	// this agreement to everyone who can see that deal. Two independent "can
	// you see it" probes cannot catch that — only asking whether the two name
	// the same company can, which is what ensureLinksShareOrganization adds on
	// top of the visibility gate, on create and on every patch that moves a link.
	//
	// That inheritance runs in the other direction too, and these entries are
	// deliberately not where that half lives: seeing an agreement through its
	// anchor is one question, changing one is another. Every mutating contract
	// path goes through writableContract, which takes auth.EnsureWritable on
	// this same anchor — so a `read` share of the deal opens the agreement and
	// stops there (#1373). What is classified here stays the REFERENCE, which
	// is what a foreign key is.
	"contract.deal_id":                         "gated: auth.EnsureLinkTarget via ensureLinksVisible in createContractTx AND UpdateContract, plus ensureLinksShareOrganization (ADR-0109 §8)",
	"contract.project_id":                      "gated: the same pair as contract.deal_id — ensureLinksVisible then ensureLinksShareOrganization, on create and on patch",
	"lead.project_id":                          "gated: auth.EnsureLinkTarget in CreateLead/UpdateLead (H1)",
	"suggestion_dismissal.organization_id":     "gated: auth.EnsureVisible in org360.Service.DismissSuggestion, inside the same transaction as the insert — dismissing advice about an account the caller cannot read would confirm it exists",
	"org_dossier.organization_id":              "gated: the dossier is assembled only after orgdossier.Service.Get runs the caller's OWN sidecar reads, and people.ListOrganizationProfileFields opens with auth.Require + ensureOrgReadable — a company the caller cannot read has no dossier written for it, and the row is keyed on that same caller",
	"org_growth_fit.organization_id":           "gated: same path as org_dossier — the assessment is written only after the caller's own gated sidecar reads succeed, and the row is keyed on that caller",
	"org_brief.organization_id":                "gated: the brief is written only after orgbrief.Service.Get runs the caller's own org360 Assemble, whose GetOrganizationTx does auth.Require + auth.EnsureVisible — an account the caller cannot read has no brief written for it, and the row is keyed on that same caller",
	"person_brief.person_id":                   "gated: the person-side twin of org_brief — the brief is written only after personbrief.Service.Get runs the caller's own person360 Assemble, whose GetPersonTx does auth.Require + auth.EnsureVisible, and the row is keyed on that same caller",
	"person_moment_dismissal.person_id":        "gated: auth.RequireHuman + auth.Require + auth.EnsureVisibleLive in person360.Service.DismissMoment, inside the same transaction as the insert — dismissing a card about a contact the caller cannot read would confirm they exist",
	"consent_qualifying_event.person_id":       "gated: the event is recorded only on a path that already holds the person — a captured inbound activity, an inquiry, or a named human typing an exchange on the record's own surface, each of which took the person read before it could name them",
	"consent_existing_customer_flag.person_id": "gated: the §7(3) flag is set only from the person's own consent surface, whose handler resolves the person through the consent store's gated read before any row is written",
	"lead_manual_signal.lead_id":               "gated: auth.EnsureVisibleLive on the lead runs in the SAME transaction as the insert (people/leadmanualsignal.go) — recording a judgement signal against a lead the caller cannot read would confirm it exists",
	// conversation_claim's table shipped ahead of its writer (ADR-0097 D1):
	// the DDL exists so the page's reads and the demo seed have a shape to
	// bind to, and the extraction task that fills it is still to come. These
	// three entries record that the gate has judged NOTHING yet — the claim
	// writer must take the person read and the activity read when it lands,
	// and this classification is what makes that obligation impossible to
	// forget rather than a decision already taken.
	"conversation_claim.person_id":          "PENDING WRITER: no code writes this table yet. The production writer must open with auth.Require(person, read) + auth.EnsureVisibleLive before it may name a person, and this entry is replaced with the gate it actually takes",
	"conversation_claim.source_activity_id": "PENDING WRITER: no code writes this table yet. A claim cites the activity it was read from, so the writer must gate that activity read (auth.EnsureLinkTarget or the activity scope clause) — citing a message the caller cannot open would disclose it",
	"conversation_claim.task_activity_id":   "PENDING WRITER: no code writes this table yet. The task an extracted commitment creates is written through the tasks substrate's own gated path, and this entry is replaced with that gate when the routing edge lands",
	// Owned child rows: the row is an attribute of its visible parent,
	// written only through the parent's own gated paths.
	"organization_geocode_state.organization_id": "child row: the sidecar for an organization's own coordinate lookup, and no caller ever names the organization it keys on. " +
		"The row is created by enqueueGeocode inside UpdateOrganization's transaction, for the id that write was already addressing (people/organization.go), " +
		"and touched afterwards only by AddressForGeocode and recordGeocodeAfter (people/geocode.go). " +
		"WHAT THIS EXCEPTION RESTS ON IS CURRENTLY NON-EXECUTING, and that is the honest statement of its cost: all three of those statements filter " +
		"organization.workspace_id, a column core 1787047322 dropped three days before geocode.go shipped, so every one fails with SQLSTATE 42703 (#2173). " +
		"No read reaches this FK today because no read on this path runs at all. When #2173 is fixed the ADR-0091-consistent repair DELETES that predicate " +
		"rather than restoring the column, at which point this reason is void and nothing here would fail — gatekit checks the key, never the rationale — so " +
		"#2173 carries a note to revisit this entry, and it must be re-derived against whatever replaces the predicate rather than carried over.",
	"activity_link.activity_id":  "child row: written only inside LogActivity for the new activity",
	"lead_score_history.lead_id": "child row: one point in a lead's own score series, appended only from inside the lead's gated write paths — CreateLead/CreateLeadTx (lead:create), UpdateLead (lead:update) and RecomputeLeadScore (lead:update), each of which has already admitted the caller before the append runs in its transaction",
	// comms_outbound is delivery machinery for one activity, not a
	// standalone record (comms/doc.go): StageTx writes only inside the
	// caller's own transaction, alongside the activity write it reports on
	// (comms/store.go's StageInput doc). comms.Store carries no RBAC gate of
	// its own (see the internal/modules/comms waivers in rbacgate_test.go)
	// because the send action is admitted at that shared-transaction
	// activity write, not inside comms. Both send transports stage through
	// that one path and pass the activity id they just created, never an
	// externally-supplied reference.
	"comms_outbound.activity_id": "child row: written only inside the caller's own transaction, alongside the activity write it reports on",
	// A scheduled send names the conversation its reply will join, and that
	// reference is CLIENT-SUPPLIED — the rep picks the thread. It is gated at
	// the same moment an immediate reply's anchor is, by the same code:
	// scheduleSend runs the whole of prepareSend before it freezes anything,
	// and prepareSend resolves the origin through SendOrigin.resolve. A rep who
	// cannot read the thread cannot schedule a reply to it, and learns that at
	// the keyboard rather than at fire.
	"scheduled_send.anchor_activity_id": "gated: SendOrigin.resolve inside prepareSend, which scheduleSend runs in full before writing the row — the same gate an immediate reply passes",
	// The activity the fire PRODUCED, written by releaseInTx from the id
	// sendPreparedTx just created in that same transaction. Never an
	// externally-supplied reference: no caller can name it, because it does not
	// exist until the send commits.
	"scheduled_send.activity_id":                     "child row: written only inside the fire transaction, from the activity id that transaction just created",
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
	// The licensed-data-provider platform (ADR-0101). A run names the subject
	// it spends credits on, so admitting one IS a read of that person: QueueRun
	// takes auth.EnsureVisible inside the queueing transaction, before any
	// fence, price or reservation. Without it a rep could name any person id
	// and buy data on a record outside their scope.
	"provider_run.person_id": "gated: auth.EnsureVisible in QueueRun, inside the transaction that inserts the run — the object grant alone answers \"may this role read people\", never \"may this caller see THIS person\"",
	// The claim is a child of the run: it is written only by the domain's
	// claim sink, from the run's own person_id, and never from a request body.
	"person_provider_claim.person_id": "child row: written by the claim hand-off from the run's own subject, which QueueRun already gated; the fence is re-run immediately before every write (PI-AC-7)",
	// telegram-oa design §6.4: the channel-aware ensure contract creates the
	// Person (owner_id NULL) and this identity satellite in the same
	// transaction, from the inbound message's own channel principal —
	// never from a client-supplied person_id.
	"person_channel_identity.person_id": "child row: written through the channel-aware ensure path alongside the person it resolves or creates",
	"person_consent.person_id":          "child row: written through the person's own gated paths",
	"person_consent.lead_id":            "gated: auth.EnsureVisible on the lead subject in consent Record (E12.20)",
	"consent_event.lead_id":             "gated: auth.EnsureVisible on the lead subject in consent Record (E12.20); proof rows append only inside that path",
	"consent_doi_token.person_id":       "child row: minted and consumed only inside RecordConsent's gated path",
	"preference_token.person_id":        "gated: auth.EnsureVisible on the recipient in PreferenceTokenForEmail — the id is server-derived from the send path's workspace-predicated email→person resolve, and the minted token is a bearer credential over that person, so the mint carries the same row-scope probe the sibling read does; the public surface reads the row as the token→tenant resolver before any principal exists",
	// Server-derived pointers: stamped from an operation's outcome,
	// never accepted from the request body.
	"lead.promoted_person_id":     "server-derived: stamped by PromoteLead",
	"lead.qualified_deal_id":      "server-derived: stamped by QualifyLead with the id of the deal the same transaction just created through deals.CreateDealTx, under the caller's own deal:create grant — never a request-supplied reference",
	"person.merged_into_id":       "server-derived: stamped by MergePerson",
	"organization.merged_into_id": "server-derived: stamped by MergeOrganization",
	// The same shape as its two siblings above, through the same writer
	// (archiveMergedAway) and the same admission (mergePair): the survivor id
	// arrives from the caller, and is stamped only after auth.EnsureWritable
	// has admitted the loser and auth.WritableBy the survivor. A lead either
	// end of the pair cannot be written to answers before the pointer is set.
	"lead.merged_into_id":              "gated then stamped: MergeLead puts BOTH leads through mergePair's row-scope writability probes before archiveMergedAway writes the pointer",
	"person.converted_from_lead_id":    "server-derived: stamped by PromoteLead",
	"deal_stage_history.deal_id":       "server-derived: appended by CreateDeal/AdvanceDeal",
	"deal_forecast_history.deal_id":    "server-derived: appended by UpdateDeal for the deal it has just written, and only after auth.EnsureVisible admitted that deal at the top of the same transaction — the id never comes from a request body",
	"project_phase_history.project_id": "server-derived: appended by CreateProject/AdvanceProjectPhase from the project row they just wrote or advanced, never from a request body",
	"brief_item.deal_id":               "server-derived: written only by the brief ranker from its own row-scoped candidate query, never from a request body",
	// The capture disposition ledger (CAP-DDL-8): capture writes the row in
	// the same transaction as the activity it just created, from that
	// activity's own id — a connector principal supplies message bytes, never
	// a record reference.
	"capture_pending_counterparty.activity_id": "server-derived: stamped by the capture Sink from the activity it just wrote",
	// The attribution ladder's candidate ledger: both ends are the ladder's
	// own reads — the activity capture just wrote, and a project the rung read
	// under the caller's project grant and row scope (readableProjectScope +
	// the finder's ScopeClauseFor), never a request body.
	"project_link_candidate.activity_id": "server-derived: stamped by the capture Sink's attribution ladder from the activity it just wrote",
	"project_link_candidate.project_id":  "gated then stamped: read under the caller's project row scope by the uncertain rung's finder before the candidate is written",
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
	// recordDedupeCandidate is their only writer, and both ids come from the writing
	// path's own match query, never from a request body. Which query varies more than
	// a single phrase can carry: the fuzzy tier for a near match, an exact identity
	// lane at confidence 1.0 for a shared phone or a lane conflict. What every path
	// shares is that the ids are ones it resolved itself, and that is the invariant
	// this waiver rests on — not a transaction boundary, which the identity-conflict
	// writer deliberately does not share with its caller.
	//
	// The disposition endpoints accept only a winner_id already equal to one of the
	// stored pair ids, both of which the read below has just gated.
	//
	// The detector applies NO row-scope predicate — no auth.ScopeClauseFor, no owner
	// term — so it does pair a caller's record with one they cannot see. That is
	// deliberate: a duplicate you cannot see is still a duplicate, and scoping the
	// detector would blind it to exactly the pairs worth catching. What keeps such a
	// pair from being DISCLOSED is the queue read, which requires BOTH sides to pass
	// the caller's row scope (dedupeVisibilityClause, and EnsureVisible per side in
	// GetDedupeCandidate). All six entries rest on that read, held by
	// TestDedupeQueueHidesPairsOutsideTheCallersRowScope and, per entity type and
	// per side, TestDedupeQueueHidesAPairTheCallerCanOnlyHalfSee.
	"dedupe_candidate.left_person_id":  "server-derived: stamped by recordDedupeCandidate from the writing path's own match query",
	"dedupe_candidate.right_person_id": "server-derived: stamped by recordDedupeCandidate from the writing path's own match query",
	"dedupe_candidate.left_org_id":     "server-derived: stamped by recordDedupeCandidate from the writing path's own match query",
	"dedupe_candidate.right_org_id":    "server-derived: stamped by recordDedupeCandidate from the writing path's own match query",
	"dedupe_candidate.left_lead_id":    "server-derived: stamped by recordDedupeCandidate from fuzzyLead's own match query",
	"dedupe_candidate.right_lead_id":   "server-derived: stamped by recordDedupeCandidate from fuzzyLead's own match query",
	// Two writers, two different things holding them, so both are named: the
	// enrich pass has no caller to scope against, and the research accept has
	// one. This census is keyed by COLUMN, so it cannot notice a second writer
	// of an already-waived column — the text is the only control, and a waiver
	// naming one writer would read as covering both.
	"person_profile_field.person_id":            "server-derived for the enrich pass — it resolves the person from its own connector-activity query (PO-DDL-12) as the system principal, so what holds it is the absence of a caller; gated for SaveResearchClaims, whose person id IS the request path's — auth.EnsureWritableLive inside the write's own transaction",
	"capture_auto_enrich_state.organization_id": "server-derived: the auto-enrich sweep keys the cursor on an org id its own ListDueOrgs read produced (CAP-PARAM-7), never from a request body — a background pass with no caller to scope against",
	// The signature pass's read cursor (PO-F-2a): both ids come from the
	// pass's own SignatureCandidates query — the person it just read for and
	// the activity whose body it just read — never from a request body.
	"person_signature_enrich_state.person_id":   "server-derived: stamped by the enrich pass from its own candidate query, as the system principal — no caller, so no scope clause to apply",
	"person_signature_enrich_state.activity_id": "server-derived: stamped by the enrich pass from the activity that candidate query returned",
	// The interaction participants (ACT-DDL-3): neither id is ever carried on
	// a request body. Capture mints the activity in the same transaction and
	// resolves the counterparty through the ensure chokepoint's own lookup — which
	// carries no scope clause either, and does not need one: capture runs without a
	// caller to scope against, and the read side is what gates disclosure. A manual
	// activity takes its person from a link the activities
	// store already put through auth.EnsureLinkTarget. Reads inherit the
	// activity's own visibility (the link walk), so a participant row never
	// discloses an activity its reader could not already open.
	"activity_participant.activity_id": "child row: written only beside the activity itself, inside the transaction that mints it",
	"activity_participant.person_id":   "server-derived: the counterparty the ensure chokepoint resolved, or a link the activities store already gated",
	// The named audience of a limited activity. Written only by the audience
	// endpoint, which has put the activity through the content gate first.
	"activity_audience_member.activity_id": "child row: written only by the audience endpoint, beside the audience column it qualifies, after auth.EnsureActivityContentVisible",
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
	// The transcript a reading was made of. Client-supplied — it is the routed
	// id of the activity the rep pressed "read for next steps" on — and gated
	// on the way in: StartTranscriptReadQueued resolves it through the module's
	// own readActivity, which composes auth.ActivityContentClause, so a
	// transcript the caller cannot see answers ErrNotFound before any row is
	// written. Every later read of the run record (GetTranscriptRead,
	// LatestTranscriptRead, ReadTranscript) re-probes the same way rather than
	// trusting the stored pointer.
	"transcript_read.activity_id": "client-supplied and gated: every path resolves the activity through readActivity's ActivityContentClause walk, so an unseeable transcript is ErrNotFound rather than a readable run record",
	// The retention floor's evidence (A165, migration 0289). Both columns are
	// SERVER-DERIVED and neither has a writer yet — the table shipped ahead of
	// the pass that fills it (#1557). activity_id is the record being held, and
	// the row exists only because that record qualified; deal_id is the
	// transaction that qualified it, resolved from the deal the activity is
	// already linked to rather than from anything a caller sends. When the
	// writer lands it must derive both from rows the actor could already see —
	// this entry is the obligation, not a record of one already met.
	"activity_retention_evidence.activity_id": "server-derived: every writer names the activity it is already writing — the deal stamp sweeps the activities linked to the concluding deal, the project stamp takes the one whose link it just wrote, and the erasure's legacy arm takes the rows it already selected. None of the three reads an activity id off a request body",
	"activity_retention_evidence.deal_id":     "server-derived: the qualifying deal is the one whose own conclusion triggered the stamp, never supplied. ON DELETE SET NULL beside a frozen deal_name, so the evidence still answers after the deal is gone",
	"activity_retention_evidence.project_id":  "server-derived: the qualifying project is the one whose link the same transaction just wrote, and that link's target went through auth.EnsureLinkTarget before it landed — so the reference is a read the writer already gated. ON DELETE SET NULL beside a frozen project_name, on the same terms as the deal pair beside it",
	"finance_customer_link.organization_id":   "schema only, no writer yet (#725): the mapping write does not exist, and when it lands it must put the named company through auth.EnsureLinkTarget — this entry is the obligation, not a record of one already met",
	"finance_invoice.organization_id":         "schema only, no writer yet (#725): the sync pass does not exist, and when it lands it must resolve the organization from the customer link rather than from any request body",
	"finance_payment.organization_id":         "schema only, no writer yet (#725): the sync pass does not exist, and when it lands it must resolve the organization from the customer link rather than from any request body",
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
	headSchema(t, owner)
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
