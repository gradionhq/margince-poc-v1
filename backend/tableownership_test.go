// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Table ownership as a fitness function: the import DAG is enforced three
// ways, but nothing in the import graph stops a package from writing SQL
// against a table it does not own. This test closes that gap — it walks the
// hand-written Go under internal/modules and internal/compose, extracts every
// INSERT/UPDATE/DELETE target from SQL string literals (plus the storekit
// Patch.Apply table argument), and asserts each module only writes its own
// tables. Cross-store writes exist by design (merge relinks, GDPR erasure,
// ingest materialization); each one is ratified below with a self-contained
// rationale — an entry without a rationale is a finding, not a pass, and a
// waiver that matches no remaining write is stale and fails too. SELECTs are
// out of scope: reads are governed by RLS and the platform/auth row-scope
// clauses, not by ownership.

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// storekitOwned marks the tables written ONLY through
// platform/database/storekit (Audit/Emit) or the migration runner — no
// walked package owns them, so any direct module write needs a waiver.
const storekitOwned = "internal/platform/database/storekit"

// extSecretsStoreDir is the second platform package this gate walks (after
// settingsStoreDir): the extension tier's secret namespace owns
// extension_secret, so leaving it outside the sweep would let a future
// second writer of that table land unnoticed. Named explicitly, for the
// same reason settingsStoreDir is — the rest of platform owns no rows, and
// widening to internal/platform would sweep in files this gate has not
// judged.
const extSecretsStoreDir = "internal/platform/extsecrets"

// tableOwners maps every core-migration table to the ONE module whose store
// owns its writes (module doc.go "Tables owned" declarations, kept in sync).
// This map is the hand-maintained artifact: a new table gets an owner here
// before its first write lands.
// gatekit:fixture the owning module path each table's writes are compared
// against — expected data, not a cost anyone is paying.
var tableOwners = map[string]string{
	// identity
	"workspace":                "internal/modules/identity",
	"app_user":                 "internal/modules/identity",
	"team":                     "internal/modules/identity",
	"team_membership":          "internal/modules/identity",
	"session":                  "internal/modules/identity",
	"passport":                 "internal/modules/identity",
	"auth_token":               "internal/modules/identity",
	"role":                     "internal/modules/identity",
	"role_assignment":          "internal/modules/identity",
	"record_grant":             "internal/modules/identity",
	"oauth_client":             "internal/modules/identity",
	"oauth_authorization_code": "internal/modules/identity",
	"oauth_grant":              "internal/modules/identity",
	"oauth_refresh_token":      "internal/modules/identity",
	"onboarding_wizard_state":  "internal/modules/identity",
	// people
	"person":        "internal/modules/people",
	"person_email":  "internal/modules/people",
	"person_social": "internal/modules/people",
	"person_phone":  "internal/modules/people",
	// The channel identity is a resolution key on the person, not connection
	// state: it answers "which Person is this Telegram user", so it lives with
	// the one dedupe implementation that resolves them.
	"person_channel_identity":        "internal/modules/people",
	"organization":                   "internal/modules/people",
	"organization_domain":            "internal/modules/people",
	"organization_relationship_type": "internal/modules/people",
	"signal_thread_scan":             "internal/compose",
	"relationship":                   "internal/modules/people",
	"partner":                        "internal/modules/people",
	"lead":                           "internal/modules/people",
	"organization_profile_field":     "internal/modules/people",
	"person_profile_field":           "internal/modules/people",
	// The signature pass's per-person read cursor (PO-F-2a): which mail was
	// already shown to the model, so the same empty signature is not re-read
	// every night.
	"person_signature_enrich_state": "internal/modules/people",
	"organization_fact":             "internal/modules/people",
	// What a mail domain is allowed to create. It governs ORGANIZATION
	// creation, which people owns, so the verdict lives with the records it
	// authorizes rather than with the capture path that asks the question.
	"organization_domain_disposition": "internal/modules/people",
	"site_read":                       "internal/modules/people",
	// DH-DDL-1: the pair verdicts live with the ONE dedupe implementation.
	"dedupe_candidate": "internal/modules/people",
	// deals (incl. the E03 offer engine: rate-card + versioned offers)
	"deal":               "internal/modules/deals",
	"pipeline":           "internal/modules/deals",
	"stage":              "internal/modules/deals",
	"deal_stage_history": "internal/modules/deals",
	// The project lives in the deals bounded context (ADR-0073): it is the
	// body of work the deals hang off, not a context of its own.
	"project":               "internal/modules/deals",
	"project_phase_history": "internal/modules/deals",
	"fx_rate":               "internal/modules/deals",
	"product":               "internal/modules/deals",
	"offer":                 "internal/modules/deals",
	"offer_line_item":       "internal/modules/deals",
	"offer_template":        "internal/modules/deals",
	// activities
	"activity":      "internal/modules/activities",
	"activity_link": "internal/modules/activities",
	// ACT-DDL-3: who was in the interaction. It belongs beside activity and
	// activity_link for the same reason they belong together — it is part of
	// what an activity IS, not a graph artifact derived from one.
	"activity_participant": "internal/modules/activities",
	// CG-DDL-1: the interaction projection. Owned by search per the ratified
	// module answer (ADR-0078 §2) — the graph capability lives inside the
	// search module, and a sibling would have to import its traversal
	// primitives, which a module may not do.
	"graph_interaction_edge": "internal/modules/search",
	// CG-DDL-2: LinkedIn ghosts. Owned by people because the work they exist
	// for is identity matching — the same dedupe rules, the same chokepoint.
	"linkedin_account":    "internal/modules/people",
	"linkedin_connection": "internal/modules/people",
	"attachment":          "internal/modules/activities",
	"booking_page":        "internal/modules/activities",
	// approvals (workspace_signing_key backs the approval-token JWS)
	"approval":              "internal/modules/approvals",
	"workspace_signing_key": "internal/modules/approvals",
	// consent (the DSR case queue and the retention-policy catalog are
	// consent's; the engines that EXECUTE them live in privacy)
	"consent_purpose":      "internal/modules/consent",
	"person_consent":       "internal/modules/consent",
	"consent_event":        "internal/modules/consent",
	"consent_doi_token":    "internal/modules/consent",
	"data_subject_request": "internal/modules/consent",
	"retention_policy":     "internal/modules/consent",
	"preference_token":     "internal/modules/consent",
	// capture
	"raw_capture":                  "internal/modules/capture",
	"capture_connection":           "internal/modules/capture",
	"capture_sync_state":           "internal/modules/capture",
	"capture_backfill":             "internal/modules/capture",
	"workspace_email_domain":       "internal/modules/capture",
	"capture_digest":               "internal/modules/capture",
	"capture_auto_enrich_state":    "internal/modules/capture",
	"capture_pending_counterparty": "internal/modules/capture",
	"capture_auto_enrich_budget":   "internal/modules/capture",
	// The workspace's own additions to and carve-outs from the shipped
	// consumer-mail baseline (CAP-PARAM-5).
	"capture_freemail_domain": "internal/modules/capture",
	// The workspace's bot channel: credentials, webhook secret and connection
	// status. It is a connection, so it sits with capture_connection under the
	// ONE connector.Sink rather than with the identities it delivers.
	"channel_connection": "internal/modules/capture",
	// The installation-settings table (ADR-0090/A135). Owned by the platform
	// mechanism rather than by any module, which is the point: a setting's
	// MEANING belongs to the module that declares its entry, but no module
	// owns the row shape — platform/settings is the one writer, and a module
	// reaching this table directly would be re-implementing the governance
	// (validator, freeze probe, per-entry audit verb) the entry already
	// carries. The unusual owner is therefore the invariant, not an exception.
	"setting": "internal/platform/settings",
	// The extension tier's secret namespace (ADR-0069): the mapping from an
	// extension's own key names onto keyvault refs. Owned by the platform
	// mechanism for the same reason `setting` is — no module owns the row
	// shape, and a second writer would be a second namespace wall, which is
	// the one thing this table exists to be. platform/extsecrets is
	// therefore a walked root below, so the ownership really is enforced.
	"extension_secret": "internal/platform/extsecrets",
	// search
	"embedding":           "internal/modules/search",
	"embed_store_binding": "internal/modules/search",
	// ai (voice DNA: the derived profile artifact + corpus manifest;
	// the tracing spine: per-call metadata + opt-in captured payload)
	"ai_usage":              "internal/modules/ai",
	"voice_profile":         "internal/modules/ai",
	"voice_corpus_source":   "internal/modules/ai",
	"voice_build":           "internal/modules/ai",
	"voice_profile_version": "internal/modules/ai",
	"voice_profile_delta":   "internal/modules/ai",
	"voice_learning_signal": "internal/modules/ai",
	"ai_call":               "internal/modules/ai",
	"ai_call_payload":       "internal/modules/ai",
	"ai_call_config":        "internal/modules/ai",
	"ai_feedback":           "internal/modules/ai",
	"ai_model_rate":         "internal/modules/ai",
	// agents (incl. the runner subpackage)
	"agent_run":  "internal/modules/agents",
	"runner_job": "internal/modules/agents",
	// automation (the deterministic trigger-and-action catalog)
	"workflow_run": "internal/modules/automation",
	"automation":   "internal/modules/automation",
	// signals (the warm-room signal spine + its append-only resolution log)
	"signal":            "internal/modules/signals",
	"signal_resolution": "internal/modules/signals",
	// collections
	"list":        "internal/modules/collections",
	"list_member": "internal/modules/collections",
	"tag":         "internal/modules/collections",
	"taggable":    "internal/modules/collections",
	"saved_view":  "internal/modules/collections",
	// privacy (the erasure suppression list is the module's own state;
	// its other writes are ratified waivers below)
	"erasure_suppression": "internal/modules/privacy",
	// customfields (the governed add-field engine's catalog)
	"custom_field": "internal/modules/customfields",
	// quotas (RD-T06: owner-XOR-team revenue targets)
	"quota":                "internal/modules/quotas",
	"webhook_subscription": "internal/modules/webhooks",
	"webhook_delivery":     "internal/modules/webhooks",
	// comms (outbound delivery machinery; the activity row is the
	// user-visible fact and stays owned by activities)
	"comms_outbound": "internal/modules/comms",
	// overlay (the HubSpot mirror cluster, ADR-0017 custom namespace —
	// design.md §4.2)
	"incumbent_connection":        "internal/modules/overlay",
	"overlay_mirror":              "internal/modules/overlay",
	"overlay_association":         "internal/modules/overlay",
	"mirror_user_map":             "internal/modules/overlay",
	"mirror_user_automap_block":   "internal/modules/overlay",
	"mirror_visibility":           "internal/modules/overlay",
	"overlay_write_ledger":        "internal/modules/overlay",
	"overlay_mirror_halt":         "internal/modules/overlay",
	"overlay_tombstone":           "internal/modules/overlay",
	"overlay_backfill_cursor":     "internal/modules/overlay",
	"overlay_reconcile_watermark": "internal/modules/overlay",
	"overlay_sync_state":          "internal/modules/overlay",
	// migration (the shared importer engine's run records, IEM-DDL-1;
	// native rows land through injected Writers, so the record tables'
	// owners are untouched)
	"import_run":        "internal/modules/migration",
	"import_record_map": "internal/modules/migration",
	// compose (HTTP replay protection is transport plumbing, not domain;
	// the brief read model is the cross-module ranker's own snapshot —
	// deals + people strength + activities compose only here)
	"idempotency_key": "internal/compose",
	"brief_run":       "internal/compose/briefs",
	"brief_item":      "internal/compose/briefs",
	// The company view's per-user visit baseline: view state, not a record
	// fact, so it is written without an audit row — the saved-view ruling.
	// The person view acknowledges visits into the SAME table (one baseline
	// per user per record, whatever kind of record it is), ratified below.
	"user_record_view": "internal/compose/org360",
	// Which activities have had their stored originals re-read for further
	// participants. Job bookkeeping about a background pass rather than a
	// fact about a customer, and the pass is composed here because it spans
	// the mail and calendar parsers that no single module may reach across.
	"activity_participant_replay": "internal/compose",
	// The rep's own "not this, not now" on a suggestion: per user, keyed on
	// the evidence it fired on. Same ruling — view state, no audit row.
	"suggestion_dismissal": "internal/compose/org360",
	// The account brief's per-user cache: derived content, regenerable at
	// any time, readable by nobody but its own user. Same ruling.
	"org_brief":      "internal/compose/orgbrief",
	"org_dossier":    "internal/compose/orgdossier",
	"org_growth_fit": "internal/compose/orgdossier",
	// platform: the audit+outbox pair has ONE sanctioned writer, and the
	// shared field-provenance layer (B-E02.12) is spelled once next to it.
	// system_log is the non-entity operational ledger written through
	// storekit.LogSystem, the same storekit-owned posture as audit_log.
	"audit_log":        storekitOwned,
	"event_outbox":     storekitOwned,
	"field_provenance": storekitOwned,
	"system_log":       storekitOwned,
}

// crossStoreWrites are the ratified writes outside the writer's own tables,
// keyed "module-dir:table". Every entry carries its rationale inline so the
// gate is self-contained on a clean checkout.
var crossStoreWrites = gatekit.Waive(map[string]string{
	// One visit baseline per user per record, and a person is a record. A
	// second table keyed the same way would be the same fact under a second
	// name, and the two would answer "when did you last look at this?"
	// differently the first time one write path changed.
	"internal/compose/person360:user_record_view": "the person view's visit acknowledgement rides org360's table because since-last-visit is one fact per (user, record) — migration 0184 widened its entity_type CHECK to admit a person rather than adding a parallel table that would drift",

	// people's merge/promotion relink rows across aggregates inside their
	// single transaction — the primary aggregate owns the single-tx
	// cross-aggregate write, because a merge that could half-commit its
	// relinks would corrupt referential history.
	"internal/modules/people:deal":                 "merge/promote relink deal FK rows in the single transaction",
	"internal/modules/people:project":              "org merge re-anchors the merged-away company's projects onto the survivor in the same transaction (PROJ-LIFE-4) — the anchor is NOT NULL ... ON DELETE RESTRICT, so a project cannot stay behind, and leaving it turns the survivor's deals un-editable against the deal_project_same_org trigger",
	"internal/modules/people:activity_link":        "merge/promote relink timeline links in the single transaction",
	"internal/modules/people:attachment":           "org merge carries the document library's account pointer onto the survivor in the same transaction — organization_id is a denormalized READ path nothing else maintains, so a file left on the dissolved company is filed under a record that no longer exists and reads to a user as the contract having vanished",
	"internal/modules/people:activity_participant": "capture records the counterparty by ADDRESS because no person exists yet — the creation gate runs after that transaction commits, and for a suppressed sender never runs at all. linkActivityToPerson is the one chokepoint every ensure path reaches AND the one that has already settled the person against a merge, so naming that party is the same write, on the same row, in the same transaction as the link",
	"internal/modules/people:list_member":          "merge relinks list memberships (and archive purges them) in the single transaction",
	"internal/modules/people:taggable":             "merge relinks tag rows (and archive purges them) in the single transaction",
	"internal/modules/people:person_consent":       "merge carries the survivor's consent state in the single transaction",
	"internal/modules/people:consent_event":        "merge re-points the append-only consent proof log in the single transaction",

	// activities maintains the deal-timeline denormalization where the
	// activity lands: deal.last_activity_at moves in the same transaction
	// as the activity insert or the two drift.
	"internal/modules/activities:deal": "deal.last_activity_at is denormalized from the timeline; it must move in the activity's own transaction",

	// The outbound-send reconcile folds the provider's own captured echo of a
	// message this workspace sent into the send's own row. That fold is one
	// indivisible act — links copied, review moved, key released, row archived
	// — run in one best-effort transaction the delivery store may roll back
	// whole. The queued counterparty review is part of it: a human verdict the
	// survivor does not re-queue, which must not be left asking about a message
	// the workspace can no longer see.
	"internal/modules/activities:capture_pending_counterparty": "the message-identity absorb re-points the archived echo's queued counterparty dispositions onto the surviving send, in the same transaction as the rest of the fold — a sibling call would have to write on this very transaction anyway, so routing it through capture would buy a hop and no isolation, while splitting the fold across two owners would let half of it survive a rollback",

	// capture is the ONE connector.Sink (interfaces.md §1): one transaction
	// per inbound record writes raw original + normalized domain row, so a
	// crash can never keep evidence without the record or vice versa.
	"internal/modules/capture:activity":             "the connector sink materializes the normalized activity in the same transaction as its raw_capture original",
	"internal/modules/capture:activity_link":        "the connector sink links the materialized activity in the same ingest transaction",
	"internal/modules/capture:activity_participant": "the connector principal is the ONLY place the mailbox owner is known (capture_connection is per-user-per-provider); by the time any other module sees the activity its captured_by reads connector:gmail and the human behind it is unrecoverable, so the participant rows commit in the same ingest transaction as the activity they describe",
	"internal/modules/capture:lead":                 "the connector sink materializes inbound leads in the same transaction as their raw_capture original",

	// deals' archive purges the archived deal's collection memberships in
	// the same transaction — a dangling list/tag row would resurrect the
	// deal in segment queries.
	"internal/modules/deals:list_member":  "archiving a deal removes its list memberships in the archive transaction",
	"internal/modules/deals:taggable":     "archiving a deal removes its tag rows in the archive transaction",
	"internal/modules/deals:relationship": "archiving a deal archives its stakeholder relationships in the archive transaction — a live relationship to an archived deal would leak it into row-scope walks",

	// privacy is the module whose JOB is crossing stores: a data-subject
	// obligation (erasure Art. 17, retention ADR-0011) must reach every
	// table holding the subject in ONE transaction per record — the
	// sanctioned single-transaction exception; routing each purge
	// through the owning module's API would trade the atomicity that IS
	// the guarantee for boundary hygiene.
	"internal/modules/privacy:person":                       "erasure/retention anonymize the person row in place in the single erasure transaction (Art. 17)",
	"internal/modules/privacy:person_email":                 "erasure deletes the subject's email channel rows in the single erasure transaction",
	"internal/modules/privacy:preference_token":             "erasure deletes the subject's preference-center token in the single erasure transaction — it is a live capability over their consent record on a session-less edge, and anonymize-in-place means 0048's ON DELETE CASCADE never fires, so an erased subject would keep accruing consent rows through the capability the erasure certifies destroyed",
	"internal/modules/privacy:activity_participant":         "erasure nulls the subject's person and address arms on the interaction participants in the single erasure transaction — the address arm exists precisely for a party who never became a record, so it survives the person_email purge and would keep the erased address readable and re-matchable; the ROW is kept where other participants remain, because the other people in that conversation are not the subject",
	"internal/modules/identity:linkedin_connection":         "deactivation deletes the departing user's imported LinkedIn network in the single deactivation transaction — it is their private address book of third parties whose only tie to this installation was that person's employment, so it cannot outlive the employment; doing it here keeps it atomic with the session and passport revocation rather than leaving a window in which the account is gone and the address book is not",
	"internal/modules/privacy:linkedin_connection":          "erasure deletes the subject's LinkedIn ghosts in the single erasure transaction — a ghost holds the subject's name, employer and address, imported from a colleague's export without the subject ever being asked, and it is invisible to every person-keyed clause because a ghost is not a person row",
	"internal/modules/privacy:graph_interaction_edge":       "erasure drops the subject's interaction edges in the single erasure transaction rather than leaving it to the cg:graph-edge consumer — an Art. 17 obligation discharged by an event is one that fails silently when the bus is behind, and the projection holds who corresponded with the subject, how often and how recently",
	"internal/modules/privacy:capture_pending_counterparty": "erasure drops the capture dispositions keyed on the subject's own address in the single erasure transaction — left behind, they would keep the erased address readable and still answering the capture gates",
	"internal/modules/privacy:person_social":                "erasure and retention delete the subject's social-handle rows in the same anonymization transaction",
	"internal/modules/privacy:voice_learning_signal":        "the nightly retention sweep erases over-age draft plaintext in place; the counters row survives (voice_draftread.go stamps the per-row deadline)",
	"internal/modules/privacy:person_phone":                 "erasure deletes the subject's phone channel rows in the single erasure transaction",
	"internal/modules/privacy:person_channel_identity":      "erasure and the retention anonymizer delete the subject's channel-identity rows in the single erasure/per-record transaction — the identity is the key an inbound message would re-bind them by, so it has to go in the same commit that hashes it onto the suppression list",
	"internal/modules/privacy:lead":                         "erasure/retention anonymize the subject's segregated lead rows in the same transaction",
	"internal/modules/privacy:activity":                     "retention archives/erases over-age timeline rows, and Art. 17 erasure redacts subject-only activity subject/body, in the single erasure/per-record transaction",
	"internal/modules/privacy:attachment":                   "Art. 17 erasure deletes attachments hung off the subject or a subject-only activity in the single erasure transaction",
	"internal/modules/privacy:deal":                         "retention archives over-age lost deals per its audited per-record transaction",
	"internal/modules/privacy:embedding":                    "erasure/retention purge the subject's vectors — a similarity probe must not reconstruct erased text",
	"internal/modules/activities:embedding":                 "the ADR-0072 noise redaction drops the vectors built from the mail it just nulled, in the same transaction — an embedding of redacted text is that text in another shape, and leaving it would let a similarity probe reconstruct what the workspace decided not to retain. Same obligation as the privacy waiver above, at the other place content is destroyed; the embed lane cannot do it itself because it never observes an archived row",
	"internal/modules/privacy:raw_capture":                  "erasure purges raw provider payloads carrying the subject's identifiers in the single erasure transaction",
	"internal/modules/privacy:person_profile_field":         "Art. 17 and the retention sweep delete the subject's enrichment sidecar inside the single erasure transaction, beside field_provenance and ai_feedback: anonymize-in-place leaves the person row standing, so nothing cascades here, and the row holds the subject's title and employer with the verbatim sentence naming them",
	"internal/modules/privacy:ai_feedback":                  "Art. 17 deletes the subject's correction ledger inside the single erasure transaction, exactly as it does field_provenance beside it: the ledger holds a human-typed value ABOUT the subject, and a claim nobody may now assert anything about has nothing left to suppress",
	"internal/modules/privacy:ai_call_payload":              "erasure purges captured AI payloads mentioning the subject's identifiers, and retention ages every payload out at 365d — the special-category-adjacent content, deleted in the single erasure/per-record transaction while the ai_call metadata row survives",
	"internal/modules/privacy:ai_call":                      "retention erases embedding-kind ai_call trace rows past their fixed 90-day cap (spec §4) in the single erasure/per-record transaction — a fixed operational cap, not an admin-editable retention_policy row",
	"internal/modules/privacy:field_provenance":             "Art. 17 erasure deletes the subject's field-origin metadata in the single erasure transaction — provenance must not outlive the fields it annotates",
	"internal/modules/privacy:comms_outbound":               "the send log stores a second copy of an outbound message's recipients, subject and body, so Art. 17 erasure and the retention erase action scrub it in the SAME transaction that scrubs the activity it belongs to — routing it through comms would let the timeline row commit as a tombstone while the delivery still served the whole message",

	// direct audit_log/event_outbox writers: these paths need columns
	// storekit's writer does not carry.
	"internal/modules/approvals:audit_log":    "approval evidence stamps passport_id/on_behalf_of, columns storekit's writer does not carry; same append-only table, this module's own writer",
	"internal/modules/approvals:event_outbox": "approvals stages its events with the full envelope (passport actor fields) storekit.Emit does not carry; still outbox-only publishing",

	// the non-production admin data-reset orchestration (compose, cross-module
	// by nature — it sweeps every module's workspace_id tables in one
	// transaction) must clear the workspace's staged events alongside the
	// domain rows it just deleted: event_outbox has no workspace_id column
	// (tenancy lives in the envelope) and no owning module's store call could
	// scope a delete to "this workspace's queued events" without compose
	// growing a dependency on every module it sweeps.
	"internal/compose:event_outbox": "the reset orchestration clears this workspace's staged events in the same transaction as the domain sweep, so no relay ever wakes on an envelope pointing at a row the reset just deleted",

	// identity owns login/failed-login, which land in system_log (a login
	// mutates no record). They fire before/without an authenticated
	// principal for storekit.LogSystem to stamp — bootstrap and failed
	// logins have no session yet — so identity appends the append-only rows
	// directly.
	"internal/modules/identity:system_log": "login and failed-login land in system_log but fire before/without an authenticated principal for storekit.LogSystem to stamp; identity appends the append-only rows itself",

	// overlay's Connect/Disconnect flip workspace.x_sor_mode/x_incumbent —
	// columns overlay's OWN fork-owned migration added
	// (migrations/custom/20260716120000_overlay.up.sql, ADR-0054 §7's
	// fork-owned custom namespace), not identity's core schema. The
	// x_overlay_iff_incumbent CHECK requires both columns to change
	// together, in the SAME transaction as the incumbent_connection
	// row write (Connect) or its purge (Disconnect) — routing this
	// through identity would split that atomicity across a sibling
	// call, the same single-transaction rationale privacy's erasure
	// entries above document.
	"internal/modules/overlay:workspace": "flips its own fork-owned x_sor_mode/x_incumbent columns atomically with the connection row write, inside the same transaction (the x_overlay_iff_incumbent CHECK demands both change together)",
})

// sqlWriteTargets extracts write-statement table names from one SQL (or
// SQL-carrying format) string. UPDATE requires a SET clause so prose and
// `DO UPDATE SET`/`FOR UPDATE` never match; INSERT/DELETE are unambiguous.
var (
	insertRe = regexp.MustCompile(`(?is)\binsert\s+into\s+([a-z_][a-z0-9_]*)`)
	deleteRe = regexp.MustCompile(`(?is)\bdelete\s+from\s+([a-z_][a-z0-9_]*)`)
	updateRe = regexp.MustCompile(`(?is)\b(do\s+|for\s+)?update\s+([a-z_][a-z0-9_]*)\s+(?:[a-z_][a-z0-9_]*\s+)?set\b`)
)

func sqlWriteTargets(literal string) []string {
	var tables []string
	for _, m := range insertRe.FindAllStringSubmatch(literal, -1) {
		tables = append(tables, strings.ToLower(m[1]))
	}
	for _, m := range deleteRe.FindAllStringSubmatch(literal, -1) {
		tables = append(tables, strings.ToLower(m[1]))
	}
	for _, m := range updateRe.FindAllStringSubmatch(literal, -1) {
		if m[1] != "" { // ON CONFLICT … DO UPDATE / SELECT … FOR UPDATE — not a new target
			continue
		}
		tables = append(tables, strings.ToLower(m[2]))
	}
	return tables
}

// owningDir normalizes a package dir to its ownership unit: the module root
// under internal/modules (subpackages share their module's ownership), or
// internal/compose.
// isIntegrationTagged reports whether the file builds only under the
// integration tag — the test lane's scaffolding (harnesses, fixtures).
// The ownership and write-shape obligations bind PRODUCTION writes; an
// integration-tagged file can never reach a shipped binary, and its
// seeding writes are the suites' own fixtures.
func isIntegrationTagged(path string) bool {
	f, err := os.Open(path) // #nosec G304 -- path is a *.go file from walking the trusted source tree
	if err != nil {
		return false
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			panic(cerr) // a leaked fd in a test helper is a bug, not a condition
		}
	}()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "//go:build integration" {
			return true
		}
		if line != "" && !strings.HasPrefix(line, "//") {
			return false // past the header — build constraints must precede it
		}
	}
	return false
}

func owningDir(pkgDir string) string {
	if strings.HasPrefix(pkgDir, "internal/modules/") {
		parts := strings.SplitN(pkgDir, "/", 4)
		return strings.Join(parts[:3], "/")
	}
	return pkgDir
}

type tableWrite struct {
	pos   string // file:line for the finding
	table string
}

// collectTableWrites walks every non-test module/compose source file and
// records each SQL write target (string literals plus storekit's
// Patch.Apply table argument) under its owning directory.
func collectTableWrites(t *testing.T) map[string][]tableWrite {
	t.Helper()
	writes := map[string][]tableWrite{} // owning dir → writes
	fset := token.NewFileSet()
	for _, root := range []string{"internal/modules", "internal/compose", settingsStoreDir, extSecretsStoreDir} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") ||
				isIntegrationTagged(path) {
				return err
			}
			path = filepath.ToSlash(path)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			owner := owningDir(filepath.ToSlash(filepath.Dir(path)))
			record := func(pos token.Pos, tables []string) {
				for _, table := range tables {
					writes[owner] = append(writes[owner], tableWrite{pos: fset.Position(pos).String(), table: table})
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.BasicLit:
					if node.Kind != token.STRING {
						return true
					}
					text, err := strconv.Unquote(node.Value)
					if err != nil {
						return true
					}
					record(node.Pos(), sqlWriteTargets(text))
				case *ast.CallExpr:
					// storekit's versioned patch: Patch.Apply(ctx, tx, table,
					// id, ifVersion) issues the UPDATE — the table rides as
					// the third argument.
					sel, ok := node.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Apply" || len(node.Args) < 4 {
						return true
					}
					if lit, ok := node.Args[2].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if table, err := strconv.Unquote(lit.Value); err == nil {
							record(node.Pos(), []string{strings.ToLower(table)})
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return writes
}

func TestEveryPackageOnlyWritesTablesItOwns(t *testing.T) {
	defer crossStoreWrites.AssertAllMatched(t)

	writes := collectTableWrites(t)

	for owner, ws := range writes {
		for _, w := range ws {
			declared, known := tableOwners[w.table]
			if !known {
				t.Errorf("%s: %s writes table %q which has no declared owner — add it to tableOwners in %s",
					w.pos, owner, w.table, "backend/tableownership_test.go")
				continue
			}
			if declared == owner {
				continue
			}
			key := owner + ":" + w.table
			if crossStoreWrites.Waived(t, key) {
				continue
			}
			t.Errorf("%s: %s writes table %q owned by %s — move the write into the owning module, or ratify it in crossStoreWrites[%q] with a self-contained rationale",
				w.pos, owner, w.table, declared, key)
		}
	}
}
