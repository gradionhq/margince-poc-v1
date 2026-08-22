// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The write-shape obligation as a fitness function: every mutation that
// writes an audit row commits a paired outbox event on the same static call path
// (data-model §11, events.md §4.2 — spelled once in storekit), across
// modules AND the composition layer. A mutation that audits without
// emitting silently exempts itself from the event backbone; this test
// turns that from a reviewer memory into a gate. Exceptions are explicit,
// keyed by package path + function so a same-named function elsewhere is
// never silently waived, and each carries the decision that ratified it —
// an allow-list entry without a reason is a finding, not a pass. A waiver
// that no longer matches an audit-only function is stale and fails too.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// auditOnlyWrites are the ratified audit-without-event functions, keyed
// by "package-dir:FuncName". Every entry carries the rationale for the
// waiver inline, so the gate is self-contained on a clean checkout; an
// entry without a rationale is a finding, not a pass. When the spec's
// events.md gains the missing event types, wiring storekit.Emit into
// these mutations removes the entries.
var auditOnlyWrites = gatekit.Waive(map[string]string{
	"internal/modules/dealrooms:openSession":               "a buyer exchanging their invitation for a session. The closed catalog (events.md §5) defines no deal_room.session_* type and the closed-verb law forbids inventing one build-side; the nearest verb, deal_room.participant_invited, would announce an admission that already happened. Nothing downstream is left stale: the seller's roster reads has_signed_in and last_seen_at from the session rows themselves. The audit row answers \"when did this person first get in\", which a dispute about who saw what reads. Buyer engagement as a published signal is its own slice and owns the event when it lands",
	"internal/modules/dealrooms:SignOut":                   "the same session ending at the buyer's own hand. Audit-only on the same ground as openSession: no deal_room.session_* verb exists, the roster reads the row, and the revocation that matters to subscribers — the seller's — already emits deal_room.participant_revoked",
	"internal/modules/activities:startTranscriptReadInTx":  "the run record for reading a meeting transcript is operational reading STATUS, not a record fact — the closed catalog (events.md \u00a75) defines no transcript_read.* type and the closed-verb law forbids inventing one build-side. The reading is the same species as the deep-read dossier (createOrJoinSiteRead, below) and waived on identical ground: what the reading produces lands as staged approvals, each emitting its own event when it is accepted. Nothing downstream could act sooner either — the only consumer is the job enqueued in this same transaction, which carries the row id. The audit row answers \"who asked for this transcript to be read\"",
	"internal/modules/activities:FinishTranscriptRead":     "the same run record reaching a terminal state, written by the worker. Audit-only on the same ground as StartTranscriptReadQueued: no transcript_read.* verb exists in the closed catalog, and the proposals the reading staged already emitted approval.requested as they were staged — announcing the reading separately would report one act twice. The polling client reads the row itself, sooner than a subscriber could tell it. The audit row carries the outcome and how many proposals it produced, which is what a spend or quality question reads",
	"internal/modules/activities:scheduleSend":             "a rep committing to send a message later (ADR-0104/A155). The closed catalog (events.md \u00a75) defines no scheduled_send.* type and the closed-verb law forbids inventing one build-side; activity.captured is the nearest candidate and would be a lie, because the whole point of the design is that NO activity exists until the message fires. Nothing downstream is left stale either: the only consumer is the timer, which is enqueued in this same transaction and carries the row id. The audit row is what answers \"who agreed to send this, and when did they say\" — which is the question a scheduled message raises",
	"internal/modules/activities:RescheduleInTx":           "moving a pending message to a different moment. Audit-only on the same ground as scheduleSend: no scheduled_send.* verb exists in the closed catalog, and the fresh timer this enqueues in the same transaction is the only thing that had to learn about the move. The audit row carries both moments, which is what answers \"who pushed this to Monday\"",
	"internal/modules/activities:CancelInTx":               "a rep withdrawing a message before it fires. Audit-only on the same ground as scheduleSend: no scheduled_send.* verb exists in the closed catalog, and nothing was ever announced to retract — no activity and no delivery were written when it was scheduled. The timer needs no telling either: it wakes, reads a row that is no longer pending, and does nothing. The audit row answers \"who called it off\"",
	"internal/modules/activities:releaseInTx":              "a scheduled message becoming a real send. The send it performs emits activity.captured in this SAME transaction (sendPreparedTx \u2192 logActivityInTx), so the fact IS announced — announcing the scheduled row separately would report one send as two. The audit row records which intention produced which activity, which is the link the timeline itself does not carry",
	"internal/modules/activities:holdInTx":                 "a scheduled message refused at fire and handed back to a human. No scheduled_send.* verb exists in the closed catalog, and no activity was written to attach one to — the message did not send, which is the entire fact. Nothing downstream can act on it either: the rep is the only actor who can resolve a hold, and they read the row. The audit row carries the reason, which is what the rep and any later investigation both need",
	"internal/modules/integrations:queueOne":               "queueing a run is a decision to SPEND, not a fact about a record. The closed catalog (events.md \u00a75) defines no provider_run.* type and the closed-verb law forbids inventing one build-side; the platform deliberately emits nothing of its own, and the surfaces poll the run instead (PI-EVT-1 makes person.created a CONSUMED event, not a published one). Nothing downstream could act on it either: the values a subscriber would want do not exist yet — the run has not been submitted, let alone answered — and when they do arrive they are written by the owning domain as claims, which is where a consumer belongs. The audit row carries what a spend investigation reads: which subject, which provider, which trigger",
	"internal/modules/integrations:Connect":                "binding, rotating or reconfiguring a licensed data provider is installation configuration, not a record fact. The closed catalog (events.md \u00a75) defines no provider_connection.* type and the closed-verb law forbids inventing one build-side; it is also the same family as capture_settings and the other ADR-0090 configuration surfaces, which events.md \u00a75.3c ratifies as audit-only. Nothing downstream is left stale either: a run resolves the connection at queue time and freezes what it read, so a subscriber could not act on the change any sooner than the next run already does. The audit row carries the whole fact a spend investigation reads — who connected which provider, under which policy — and deliberately carries neither the key nor a balance",
	"internal/modules/integrations:Disconnect":             "the mirror of Connect, and audit-only for the same reason: the connection is installation configuration with no event type in the closed catalog. The cancellation it performs is visible where it matters — the cancelled runs are rows the surfaces already read — and the epoch bump it writes is what stops in-flight work, which no subscriber could do faster",
	"internal/modules/integrations:UpdateConfig":           "a policy patch on the same configuration row, audit-only on the same events.md \u00a75.3c ground as Connect. The audit image carries both sides of the change, which is what answers \"who widened what this run was allowed to spend\" — the question a surprised customer actually asks",
	"internal/modules/people:SetDomainAdmission":           "blocking a domain or taking one back is capture POSTURE, not a fact about a record — the same family as capture_settings and the other ADR-0090 configuration surfaces that events.md §5.3c ratifies as audit-only, and the closed catalog (events.md §5) defines no domain_disposition.* type for it. Nothing downstream is left stale: the decision is read by the triage sweep from the row itself on its next pass, which no subscriber could do sooner. The audit row carries what the question \"who unblocked this, and what was it before\" needs — the before-image, the new admission, the reason and the human source",
	"internal/modules/people:WriteProviderClaims":          "the arrival of purchased provider values on a person (ADR-0101). Audit-only because the closed catalog (events.md §5) carries no claim verb and the closed-verb law forbids inventing one build-side; person.updated is the nearest candidate and its changed_fields is a REQUIRED, typed, BOUNDED delta over the fields a human patches — a claim is not among them and cannot be expressed as one, so emitting it would publish an empty required delta. The canonical person row is deliberately unchanged too, which is the whole point of keeping claims beside it: announcing person.updated would tell every subscriber a record moved when none did. The audit row is what answers \"which of this subject's records did the provider touch\" — the derivation the provenance rule depends on — and carries the claim KEYS and the run, never the purchased values",
	"internal/modules/integrations:writeStatusThrough":     "the same configuration row again, written by the worker rather than a human: a provider refusal (credential rejected, out of credits, rate limited) recorded on the connection so the settings card states it without a re-probe. Audit-only on the same events.md §5.3c ground as Connect and Disconnect — the closed catalog defines no provider_connection.* type and the closed-verb law forbids inventing one build-side. Nothing downstream is left stale: a run resolves the connection at lease time and reads this status there, sooner than any subscriber could act. The audit image carries what an investigation reads — which provider, which closed safe status code, when — and deliberately carries neither the key nor a balance",
	"internal/modules/identity:applyRoleObjectGrant":       "an admin editing one role's CRUD on one RBAC object. The closed catalog (events.md §5) carries exactly one role verb, role.changed, and its payload is a MEMBER's assignment move (`{user_id, from_role, to_role, by}`, api/public-events.yaml) — it cannot express \"this role's grants moved\" for the unknown set of members who hold it, and the closed-verb law forbids inventing one build-side. Emitting it per holder would also publish a false statement: nobody's assignment changed. Nothing downstream is left stale by the omission either — /me's authorization snapshot is documented as a snapshot rather than an authority, clients refetch it on focus and after any 403, and the gate itself re-resolves the merged policy on every call. The audit row carries the whole fact (actor, role, object, both grant images), which is what a permission-change investigation reads",
	"internal/modules/ai:Record":                           "a human's verdict on a derived claim (AIRT-SCHEMA-1). The closed catalog (events.md \u00a75) defines no ai_feedback.* type and the closed-verb law forbids inventing one build-side. Nothing downstream reacts to a verdict either: the ledger is CONSULTED at re-derivation time, so a subscriber would have nothing to do that the next read does not already do. The audit row carries the attribution a \"corrected by you\" marker and any later dispute both need",
	"internal/modules/activities:insertCapturedAttachment": "a file arriving on a captured message is part of that message, and the capture already emits activity.captured for it in the same transaction. The closed catalog (events.md \u00a75) defines no attachment.* creation type, and the closed-verb law forbids inventing one build-side; announcing the file separately would report one arrival as two. The audit row is still owed and still written — Art. 15 assembly and the audit trail read rows, not events, and a file that reached us with no record of arriving is the gap that matters",
	"internal/modules/capture:auditProjectAttribution":     "the project attribution ladder filing one freshly captured message under a project. Waived on the same ground as insertCapturedAttachment above: the filing is part of that message ARRIVING, and the capture already emitted activity.captured for it moments earlier — announcing it again would report one arrival as two, and every subscriber would react twice. activity.updated is the only candidate type and it belongs to the activities module, which owns what it MEANS (TestEveryEventTypeHasOneEmittingModule); capture does not get to redefine it as \"a message arrived, again\". A consumer of activity.captured may indeed read the activity before this link lands, and that is the design rather than a gap: attribution is optional and most messages get none, so nothing may treat a message's project as settled at arrival. What reads a project's timeline reads activity_link, which is this row. The audit row is still owed and still written: \"how did this message end up on this project\" is exactly the question the ladder raises, and the audit row's principal is what says the ladder rather than a human answered it",
	"internal/modules/activities:RedactCapturedNoiseTx":    "the ADR-0072 noise redaction nulls content on a row that is ALREADY archived, and the one consumer that would not have reacted to the archive — the embedding lane — is handled directly: the redaction deletes the vectors itself, so no subscriber is left holding derived content. The closed catalog (events.md \u00a75) defines no activity.redacted type, and re-announcing an invisible row's content change would tell the rest nothing they can act on",
	"internal/modules/activities:auditIdentityReKey":       "the outbound-send reconcile moves a sent message onto the transport identity its provider actually stamped. activity.updated is the only candidate event and its changed_fields is a REQUIRED, typed, BOUNDED delta over the fields a human patches (subject, body, occurred_at, due_at, remind_at, assignee_id, is_done, a relinked target) — the natural key is not among them and cannot be expressed as one, so emitting it would publish an empty required delta: an event that says a change happened and cannot say what. The closed catalog (events.md §5, shared/kernel/events/catalog.go) defines no transport-identity verb either, and the closed-verb law forbids inventing one build-side. Recorded upstream (P3): the fix is a contract change — a typed identity delta or a discrete reconciliation event — not a build-side substitute",
	"internal/modules/privacy:Create":                      "the retention-policy authoring surface (GCS-WIRE-1..4). The closed catalog (events.md \u00a75) carries retention.applied — the ENGINE's statement that it acted on a RECORD, with that record as the event's entity — and no retention_policy.* type; emitting it for a config edit would publish a claim that a subject's data moved when nothing did. events.md \u00a75.3c ratifies exactly this family as audit-only, the same footing as capture_settings (EVT-NOEVT-3). Nothing downstream is left stale either: the nightly pass re-reads every enabled policy at the start of each run, so the next pass sees the change without being told. The audit row carries both field images, which is what a \"who widened the window?\" investigation reads",
	"internal/modules/privacy:Update":                      "the retention-policy authoring surface (GCS-WIRE-1..4). The closed catalog (events.md \u00a75) carries retention.applied — the ENGINE's statement that it acted on a RECORD, with that record as the event's entity — and no retention_policy.* type; emitting it for a config edit would publish a claim that a subject's data moved when nothing did. events.md \u00a75.3c ratifies exactly this family as audit-only, the same footing as capture_settings (EVT-NOEVT-3). Nothing downstream is left stale either: the nightly pass re-reads every enabled policy at the start of each run, so the next pass sees the change without being told. The audit row carries both field images, which is what a \"who widened the window?\" investigation reads",
	"internal/modules/privacy:Delete":                      "the retention-policy authoring surface (GCS-WIRE-1..4). The closed catalog (events.md \u00a75) carries retention.applied — the ENGINE's statement that it acted on a RECORD, with that record as the event's entity — and no retention_policy.* type; emitting it for a config edit would publish a claim that a subject's data moved when nothing did. events.md \u00a75.3c ratifies exactly this family as audit-only, the same footing as capture_settings (EVT-NOEVT-3). Nothing downstream is left stale either: the nightly pass re-reads every enabled policy at the start of each run, so the next pass sees the change without being told. The audit row carries both field images, which is what a \"who widened the window?\" investigation reads",
	"internal/modules/activities:setDealDocumentHidden":    "hiding or unhiding a captured file on ONE deal's Files area. The file, the message and the company library are untouched — the row only says which deal lists it — and the closed catalog defines no attachment.* type, on the same ground as UploadAttachment and ArchiveAttachment above. The audit row names the deal and the attachment, which answers \"who took this off the deal\"",
	"internal/modules/dealrooms:PreviewRoom":               "the seller looking at their own room as a buyer. The preview seat is the seller's own and invisible to buyers; the catalog's participant_invited and credential_reissued events are about buyers being admitted, and announcing a rep's preview as one would count the rep as an invited buyer in every subscriber. The audit row names the seat and says preview",
	"internal/modules/dealrooms:NoteLinkRequest":           "a buyer asking the public page for a new link, stamped on their seat so the seller can see the ask and hand over a link when no mail can go. Nothing about the room or its content changed — the audit row names the seat and the anonymous requester, and the closed catalog announces credentials only when one is actually reissued (deal_room.participant_credential_reissued, which the reissue half of the same call still emits)",
	"internal/modules/dealrooms:addDocumentTx":             "putting an attachment of the deal in front of the buyer. A room document is editorial: buyer-visible only once a human publishes, and deal_room.published announces the manifest a buyer reads has changed. Emitting here would announce a document the buyer cannot yet see and, for a room never published, never will. The audit row answers \"who decided the buyer should see this file\"",
	"internal/modules/dealrooms:UpdateDocument":            "renaming, regrouping or reordering that same entry, audit-only on the identical ground: editorial, published by the next release. The audit image carries both sides of the change",
	"internal/modules/dealrooms:RemoveDocument":            "taking the entry out of the room, audit-only on the identical ground as addDocumentTx: the buyer stops seeing it at the next publish, the row survives so a release that named it still resolves, and the audit row names the attachment that was withdrawn",
	"internal/modules/signals:NarrowDerivedForActivity":    "the audience-change corrector narrowing a derived signal to its capture owner (or archiving an ownerless one). The closed catalog (events.md §5) carries signal.detected and signal.resolved, neither of which says \"this summary's audience narrowed\", and the closed-verb law forbids inventing one build-side — announcing the narrowing would also broadcast the existence of what a human just limited. The signal surfaces read the rows, and the same transaction drops the thread's scan watermark so the next extraction pass re-reads under the new audience; the ONE deliberate asymmetry is that a WIDENED activity never widens an already-narrowed signal — its live fingerprint blocks a duplicate, and disclosure is the direction that must never happen by side effect. The audit row answers \"why did this summary vanish from my colleagues' view\"",
	"internal/modules/collections:CreateSavedView":         "saved views are per-user view state, not record facts — events.md §5.3c ratifies this config family as audit-only and defines no saved_view.* type",
	"internal/modules/collections:UpdateSavedView":         "saved views are per-user view state, not record facts — events.md §5.3c ratifies this config family as audit-only and defines no saved_view.* type",
	"internal/modules/collections:ArchiveSavedView":        "saved views are per-user view state, not record facts — events.md §5.3c ratifies this config family as audit-only and defines no saved_view.* type",
	"internal/modules/collections:CreateList":              "lists are ratified audit-only in V1 — events.md \u00a75.3c defines no list.* types and none is added",
	"internal/modules/collections:ArchiveList":             "lists are ratified audit-only in V1 — events.md \u00a75.3c defines no list.* types and none is added",
	"internal/modules/collections:AddMember":               "lists are ratified audit-only in V1 — events.md \u00a75.3c defines no list.* types and none is added",
	"internal/modules/people:CreateLeadSource":             "the administered lead vocabularies are workspace configuration, not record facts — the same events.md §5.3c family as tags, lists and pipeline config; the closed catalog defines no lead_source.* or lead_disqualify_reason.* type and the closed-verb law forbids inventing one build-side. Nothing downstream is left stale: the pick lists are read from the rows on every render, and the scorer reads the weights at scoring time",
	"internal/modules/people:UpdateLeadSource":             "the administered lead vocabularies are workspace configuration, not record facts — the same events.md §5.3c family as tags, lists and pipeline config; the closed catalog defines no lead_source.* or lead_disqualify_reason.* type and the closed-verb law forbids inventing one build-side. Nothing downstream is left stale: the pick lists are read from the rows on every render, and the scorer reads the weights at scoring time",
	"internal/modules/people:DeleteLeadSource":             "the administered lead vocabularies are workspace configuration, not record facts — the same events.md §5.3c family as tags, lists and pipeline config; the closed catalog defines no lead_source.* or lead_disqualify_reason.* type and the closed-verb law forbids inventing one build-side. Nothing downstream is left stale: the pick lists are read from the rows on every render, and the scorer reads the weights at scoring time",
	"internal/modules/people:CreateLeadDisqualifyReason":   "the administered lead vocabularies are workspace configuration, not record facts — the same events.md §5.3c family as tags, lists and pipeline config; the closed catalog defines no lead_source.* or lead_disqualify_reason.* type and the closed-verb law forbids inventing one build-side. Nothing downstream is left stale: the pick lists are read from the rows on every render, and the scorer reads the weights at scoring time",
	"internal/modules/people:UpdateLeadDisqualifyReason":   "the administered lead vocabularies are workspace configuration, not record facts — the same events.md §5.3c family as tags, lists and pipeline config; the closed catalog defines no lead_source.* or lead_disqualify_reason.* type and the closed-verb law forbids inventing one build-side. Nothing downstream is left stale: the pick lists are read from the rows on every render, and the scorer reads the weights at scoring time",
	"internal/modules/people:DeleteLeadDisqualifyReason":   "the administered lead vocabularies are workspace configuration, not record facts — the same events.md §5.3c family as tags, lists and pipeline config; the closed catalog defines no lead_source.* or lead_disqualify_reason.* type and the closed-verb law forbids inventing one build-side. Nothing downstream is left stale: the pick lists are read from the rows on every render, and the scorer reads the weights at scoring time",
	"internal/modules/collections:CreateTag":               "tags are ratified audit-only in V1 — events.md \u00a75.3c defines no tag.* types and none is added",
	"internal/modules/collections:ArchiveTag":              "tags are ratified audit-only in V1 — events.md \u00a75.3c defines no tag.* types and none is added",
	"internal/modules/collections:ApplyTag":                "tags are ratified audit-only in V1 — events.md \u00a75.3c defines no tag.* types and none is added",
	"internal/modules/collections:RemoveTag":               "the same ratification as ApplyTag beside it: tags are audit-only in V1, events.md \u00a75.3c defines no tag.* types, and taking a tag off does not become an event because putting one on is not",
	"internal/modules/consent:CreateDSR":                   "the closed catalog (events.md \u00a75) defines no dsr.* type; the closed-verb law forbids inventing one build-side",
	"internal/modules/consent:UpdateDSR":                   "the closed catalog (events.md \u00a75) defines no dsr.* type; the closed-verb law forbids inventing one build-side",
	"internal/modules/consent:finalizeErasureFulfil":       "the audit-only finalize step of FulfilErasure: the closed catalog (events.md \u00a75) defines no dsr.* type; the closed-verb law forbids inventing one build-side (the erase side effect emits its own person.* event inside privacy.ErasePerson)",
	"internal/modules/consent:IssueDoubleOptIn":            "the closed catalog (events.md \u00a75) defines no consent.doi_issued type; the later grant (recordConsent) emits consent.changed \u2014 issuance is attributable via its audit row",
	"internal/modules/automation:Create":                   "automation config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no automation.* type; the runs it produces are separately recorded in workflow_run",
	"internal/modules/automation:Update":                   "automation config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no automation.* type; the runs it produces are separately recorded in workflow_run",
	"internal/modules/automation:Archive":                  "automation config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no automation.* type; the runs it produces are separately recorded in workflow_run",
	"internal/modules/webhooks:CreateSubscription":         "webhook-subscription config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no webhook_subscription.* type; the deliveries it produces are separately recorded in webhook_delivery",
	"internal/modules/webhooks:UpdateSubscription":         "webhook-subscription config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no webhook_subscription.* type; the deliveries it produces are separately recorded in webhook_delivery",
	"internal/modules/webhooks:RotateSecret":               "webhook-subscription config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no webhook_subscription.* type; the rotation is attributable via its audit row",
	"internal/modules/webhooks:ArchiveSubscription":        "webhook-subscription config is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no webhook_subscription.* type; archive stops delivery and is attributable via its audit row",
	"internal/modules/webhooks:requireReplay":              "a human-initiated delivery replay is ratified audit-only \u2014 it re-attempts an existing webhook_delivery; the closed catalog (events.md \u00a75) defines no webhook.* type",
	"internal/modules/capture:Add":                         "the workspace consumer-mail, own-domain and exclusion lists are capture config, ratified audit-only on the same ruling as the capture-settings write (EVT-NOEVT-3): the closed catalog (events.md \u00a75) defines no verb for a list entry, and the closed-verb law forbids inventing one build-side",
	"internal/modules/capture:Remove":                      "withdrawing a consumer-mail, own-domain or exclusion list entry, same ratified audit-only posture as Add",
	"internal/platform/settings:SetRawTx":                  "an installation-settings write is ratified audit-only (EVT-NOEVT-3, ADR-0090/A135) \u2014 the closed catalog (events.md \u00a75) defines no settings type, and the closed-verb law forbids inventing one build-side. This is the SAME ruling the capture-settings write carried before ADR-0090 moved it here, generalized: it now covers every setting rather than one column, so a new setting inherits the posture instead of arguing it again. A setting whose change genuinely needs a consumer is the case for adding a catalog verb upstream, not for emitting an unlisted one here",
	"internal/modules/capture:auditLifecycle":              "connector lifecycle (connect/reconnect/disconnect) is audit-only because the kernel models NO entity kind for capture_connection \u2014 storekit.Emit needs one, and the closed catalog (events.md \u00a75) defines no connector.* type. Inventing one build-side would break the closed-verb law. Attribution, which is what the privacy boundary actually needs, is carried by the audit row",
	"internal/modules/deals:CreateProduct":                 "the rate-card is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no product.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:UpdateProduct":                 "the rate-card is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no product.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:ArchiveProduct":                "the rate-card is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no product.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:UpdateOffer":                   "draft-offer edits are ratified audit-only \u2014 events.md \u00a75.3 defines only lifecycle offer.* types (created/sent/accepted/rejected/superseded), no offer.updated",
	"internal/modules/deals:ArchiveOffer":                  "offer archive is ratified audit-only \u2014 events.md \u00a75.3 defines no offer.archived type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:SetPdfAssetRef":                "persisting the rendered PDF's blob ref is ratified audit-only \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.rendered, and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:AddOfferLineItem":              "draft line edits are ratified audit-only \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.updated",
	"internal/modules/deals:UpdateOfferLineItem":           "draft line edits are ratified audit-only \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.updated",
	"internal/modules/deals:RemoveOfferLineItem":           "draft line edits are ratified audit-only \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.updated",
	"internal/modules/deals:AcceptOfferLineItem":           "accepting a staged draft line (E03.21a) is a draft line edit \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.updated",
	"internal/modules/deals:AddStagedOfferLines":           "staging AI-drafted draft lines (E03.21a) is a draft line edit \u2014 events.md \u00a75.3 defines only lifecycle offer.* types, no offer.updated; a staged line is invisible until AcceptOfferLineItem, which carries the same ratified waiver",
	"internal/modules/deals:CreateOfferTemplate":           "offer templates are workspace-authored config, the Product/Quota precedent \u2014 the closed catalog (events.md \u00a75) defines no offer_template.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:UpdateOfferTemplate":           "offer templates are workspace-authored config, the Product/Quota precedent \u2014 the closed catalog (events.md \u00a75) defines no offer_template.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/deals:ArchiveOfferTemplate":          "offer templates are workspace-authored config, the Product/Quota precedent \u2014 the closed catalog (events.md \u00a75) defines no offer_template.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/people:createOrJoinSiteRead":         "the deep-read dossier is operational crawl status, not a record fact \u2014 the closed catalog (events.md \u00a75) defines no site_read.* type; the facts a read produces land through its staged proposals, each emitting its own event on accept",
	"internal/modules/people:setDedupeDispositionTx":       "the queue verdict is review-flow state, not a record fact \u2014 the closed catalog (events.md \u00a75) defines no dedupe.* type; the merge arm's person.merged/organization.merged carries the bus-visible outcome",
	"internal/modules/people:reopenDedupeCandidateTx":      "the queue verdict is review-flow state, not a record fact \u2014 the closed catalog (events.md \u00a75) defines no dedupe.* type; the merge arm's person.merged/organization.merged carries the bus-visible outcome",
	"internal/modules/identity:CreateRecordGrant":          "the closed catalog (events.md \u00a75) defines no grant.* type; the closed-verb law forbids inventing one build-side",
	"internal/modules/identity:RevokeRecordGrant":          "the closed catalog (events.md \u00a75) defines no grant.* type; the closed-verb law forbids inventing one build-side",
	"internal/modules/identity:mintPassport":               "minting an Agent Seat Passport is ratified audit-only \u2014 the closed catalog (events.md \u00a75.6a) defines passport.revoked and NO issuance counterpart, so the only bus-visible passport fact is its death. That asymmetry is deliberate on the consumer side (a long-lived holder must drop a credential that died; nobody has to react to one being born) and the closed-verb law forbids inventing passport.issued build-side. The mint is attributable via its audit row; the missing type is raised upstream (P3)",
	"internal/modules/identity:auditLend":                  "the record of WHICH passport a human lent to a client is ratified audit-only — the closed catalog (events.md §5) defines no consent or lend fact at all, and the one type that would fit structurally, audit.appended, is declared in the contract as having no emit site and an EMPTY payload, so it could name neither the passport nor the client. It sits beside issueGrant and mintPassport, the two credential writes this same lend produces when the code is redeemed, and carries the same waiver for the same reason; the addition (an OAuth-consent verb covering the lend, the issuance and the revoke cascade) is raised upstream (P3)",
	"internal/modules/identity:issueGrant":                 "the OAuth consent record is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no oauth_grant.* type, and passport.revoked is the one identity-credential verb it does define. The grant it records issues a passport that carries the same waiver, so nothing on the bus is lost that the passport's own lifecycle would not already carry; the addition (an issuance verb, and oauth.grant_revoked for the revoke cascade) is raised upstream (P3)",
	"internal/modules/signals:AcknowledgeTx":               "the same ratified audit-only posture as UpdateSignal, whose triage path this is the approval-effect twin of \u2014 events.md \u00a75.11 defines only signal.detected/signal.resolved, no signal.updated, and the closed-verb law forbids inventing one build-side",
	"internal/modules/signals:UpdateSignal":                "human triage (status/severity) is ratified audit-only \u2014 events.md \u00a75.11 defines only signal.detected/signal.resolved (raw\u2192entity attribution, emitted by the resolver), no signal.updated, and the closed-verb law forbids inventing one build-side",
	"internal/modules/signals:ArchiveSignal":               "signal archive is ratified audit-only \u2014 events.md \u00a75.11 defines no signal.archived type and the closed-verb law forbids inventing one build-side",
	"internal/modules/activities:UploadAttachment":         "attachments are ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no attachment.* type and a polymorphic attachment has no single stream to ride; the closed-verb law forbids inventing one build-side",
	"internal/modules/activities:ArchiveAttachment":        "attachments are ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no attachment.* type and a polymorphic attachment has no single stream to ride; the closed-verb law forbids inventing one build-side",
	"internal/modules/activities:UpdateAttachmentMetadata": "attachments are ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no attachment.* type and a polymorphic attachment has no single stream to ride; the closed-verb law forbids inventing one build-side. This write sets what a document MEANS (category, title, state, pin) and moves no bytes, so it has even less to publish than the upload beside it",
	"internal/modules/customfields:createInTx":             "the closed catalog (events.md \u00a75) defines no custom_field.* type \u2014 the spec's custom-fields.md \u00a7Events ratifies the audit entry as the add/rename/retire trail, and a cross-object catalog change has no single family stream to ride (the attachments precedent)",
	"internal/modules/customfields:Rename":                 "the closed catalog (events.md \u00a75) defines no custom_field.* type \u2014 the spec's custom-fields.md \u00a7Events ratifies the audit entry as the add/rename/retire trail",
	"internal/modules/customfields:Retire":                 "the closed catalog (events.md \u00a75) defines no custom_field.* type \u2014 the spec's custom-fields.md \u00a7Events ratifies the audit entry as the add/rename/retire trail",
	"internal/modules/customfields:setOptionsInTx":         "the closed catalog (events.md \u00a75) defines no custom_field.* type \u2014 the spec's custom-fields.md \u00a7Events ratifies the audit entry as the add/rename/retire trail",
	"internal/modules/ai:writeModelRate":                   "model prices are ratified audit-only (EVT-NOEVT-3) \u2014 the closed catalog (events.md \u00a75) defines no ai_model_rate.* type and the price sheet is workspace config recomputed price-on-read, the same ruling as the deals-owned product rate-card; inventing a build-side type or stream would violate the closed catalog (P3)",
	"internal/modules/deals:writeFxRate":                   "FX rates are ratified audit-only (EVT-NOEVT-3) \u2014 the closed catalog (events.md \u00a75) defines no fx_rate.* type and the rate sheet is workspace config recomputed price-on-read, the same ruling as the deals-owned product rate-card (CreateProduct is audit-only); an fx_rate.* verb on the deal stream would be a build-side invention the closed catalog forbids (P3)",
	"internal/modules/quotas:CreateQuota":                  "quota targets are ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no quota.* type (E09's forecast.period_closed is a period-close fact, deferred with its work package) and the closed-verb law forbids inventing one build-side",
	"internal/modules/quotas:UpdateQuota":                  "quota targets are ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no quota.* type (E09's forecast.period_closed is a period-close fact, deferred with its work package) and the closed-verb law forbids inventing one build-side",
	"internal/modules/quotas:ArchiveQuota":                 "quota targets are ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no quota.* type (E09's forecast.period_closed is a period-close fact, deferred with its work package) and the closed-verb law forbids inventing one build-side",
	"internal/modules/privacy:AssembleSAR":                 "the closed catalog (events.md \u00a75) defines no subject-access-export type; the export is a read whose only write IS the audit row",
	"internal/modules/privacy:tombstoneCollateralScrubs":   "the erasure's single retention.applied on the person is the bus-visible fact for the whole scrub; these tombstones exist to bound each collateral record's field-history projection, and the closed catalog (events.md \u00a75) defines no per-collateral erasure type to ride",
	"internal/modules/overlay:auditUserMapChange":          "the Margince-user <-> incumbent-user mapping is workspace config, not a record fact: mirror_user_map is not a public contract entity, and the closed catalog (events.md §5, shared/kernel/events/catalog.go) defines no mirror.user_* type. Emitting one would need a contract event with no consumer. Same audit-only posture as overlay's own x_sor_mode flip",
	"internal/modules/overlay:BlockAutoMap":                "an admin's deliberate unmap is a workspace-config change on mirror_user_map and its standing mirror_user_automap_block, neither of them a public contract entity — the closed catalog defines no mirror.user_* type for either the removed mapping or the block that replaces it. Same audit-only posture as UpsertUserMap",
	"internal/modules/overlay:auditRevokedMappings":        "an automated mapping revoke — a stale email-sourced row, or one whose incumbent owner email became ambiguous — is a workspace-config change on mirror_user_map, which is not a public contract entity: the closed catalog defines no mirror.user_* type. Audited so a user's silently-removed access has a record; same posture as UpsertUserMap",
	"internal/modules/migration:Create":                    "import-run lifecycle is ratified audit-only \u2014 the import-export-migration chapter's IEM-GAP-3 explicitly declines to invent run-lifecycle events build-side ('this chapter deliberately does not invent them here'); the rows the run lands emit their own standard entity events through the native stores",
	"internal/modules/migration:CreateStagedRun":           "the direct importer's half of the same ratified import-run lifecycle \u2014 IEM-GAP-3 declines run-lifecycle events build-side, and the staged states it adds (validating, awaiting_approval) are the ones a human polls through IEM-WIRE-6 rather than subscribing to; the leads and organizations the approved run lands emit their own entity events through the people store. See migration:Create",
	"internal/modules/migration:FailValidation":            "the staged lifecycle's failure arm, audit-only on the same ground as its sibling moves: IEM-GAP-3 defines no run-lifecycle event and the closed-verb law forbids inventing one build-side. It records a validation that could not finish, which wrote nothing to the estate for a subscriber to hear about. See migration:Create",
	"internal/modules/migration:stageTransition":           "the staged lifecycle's one move (validating\u2192awaiting_approval\u2192running), audit-only on exactly the ground migration:transition already is: IEM-GAP-3 defines no run-lifecycle event and the closed-verb law forbids inventing one build-side. The audit row carries both sides of the move, which is what answers \"who approved this import, and against which state\"",
	"internal/modules/migration:Resume":                    "import-run lifecycle is ratified audit-only \u2014 IEM-GAP-3 declines run-lifecycle events build-side; see migration:Create",
	"internal/modules/migration:transition":                "import-run lifecycle is ratified audit-only \u2014 IEM-GAP-3 declines run-lifecycle events build-side; see migration:Create",
	"internal/modules/migration:beginUndo":                 "the undo lifecycle's own start move (complete\u2192undoing, or resuming one already undoing), audit-only on exactly the ground migration:transition already is: IEM-GAP-3 defines no run-lifecycle event and the closed-verb law forbids inventing one build-side. See migration:Create",
	"internal/modules/migration:completeUndo":              "the undo lifecycle's finish move (undoing\u2192undone), same ground as beginUndo \u2014 IEM-GAP-3 declines run-lifecycle events build-side. The rows it reverses are archived through each object's OWN store method, which emits its own standard entity event; this row is the import_run's own bookkeeping, exactly like the forward commit's completion. See migration:Create",
	"internal/modules/overlay:auditFreeze":                 "the flip's mirror freeze/unfreeze is audit-only for the same reason CompleteFlip is: the closed catalog (events.md §5) defines no mirror-freeze or flip.* type, and the freeze's observable effect is local (fenced writes refuse, the sweep skips the workspace) — no subscriber could act on an event. Audited because halting incumbent sync is a governance-relevant act and who latched the workspace must be on the record",
	"internal/modules/overlay:CompleteFlip":                "the overlay\u2192native mode flip is audit-only by the same ruling as overlay's Connect/Disconnect x_sor_mode writes carry their connection events: the closed catalog (events.md \u00a75) defines no flip.* or workspace-mode type, ADR-0071 mints no event, and the flip's observable effect (the dispatcher re-route) rides the in-process mode-flip observer, not the bus. The audit row carries the run id + mode",
	"internal/compose:auditExport":                         "the export audit entry IS the deliverable (features/04 \u00a75: 'writes the single export audit entry'; IEM 'export emits no domain events') \u2014 the closed catalog defines no export.* type and the bundle mutates no record",
	"internal/compose:sweepAndReseed":                      "the non-production admin data-reset's own audit row records the reset operation itself (entity_type workspace) \u2014 the closed catalog (events.md \u00a75) defines no reset.* type, and a subscriber would learn nothing actionable from an event announcing that the workspace it was about to react to no longer has the rows it referenced. The re-seed the same transaction performs writes its own standard entity events through each module's native seeder (pipeline.created, etc.)",
	"internal/compose/briefs:SnapshotRun":                  "the brief read model is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no brief.* type and the closed-verb law forbids inventing one build-side",
	"internal/compose/briefs:markItem":                     "the brief read model is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no brief.* type and the closed-verb law forbids inventing one build-side",
	"internal/compose/briefs:resurfaceExpiredSnoozes":      "the brief read model is ratified audit-only \u2014 the closed catalog (events.md \u00a75) defines no brief.* type and the closed-verb law forbids inventing one build-side",
	"internal/modules/people:recordDedupeCandidate":        "a dedupe candidate is a QUESTION the system is asking about two records, not something that happened to either \u2014 the closed catalog (events.md \u00a75) defines no dedupe.* type, nothing downstream acts on a proposal, and an outbox row nobody consumes would be a kind invented to satisfy the rule rather than a reader. Audited because who proposed a merge, when, and on what evidence belongs on the record's own history rather than only in operator telemetry",
	// The finance mirror's seven write sites. One rationale covers all of them,
	// and each is keyed on its own function so an EIGHTH finance write arrives
	// unratified instead of inheriting a waiver somebody wrote about four
	// tables.
	//
	// The event catalog is CLOSED: an event type exists because the contract
	// declares one, and a build may not mint a type to satisfy this rule. The
	// catalog carries no finance verb at all, and neither adjacent type fits.
	// `mirror.*` is the overlay write-back stream, so staging an accounting fact
	// under it would route the envelope to subscribers watching for something
	// else. `organization.updated` would tell every subscriber that a company
	// record changed when none did — the same argument that already makes
	// people:WriteProviderClaims audit-only. A wrong envelope is acted on; an
	// absent one is not.
	//
	// The audit row is the half that could not wait, because it cannot be
	// written afterwards: an invoice mirrored without one stays permanently
	// unaccounted for, and the erasure and retention reasoning that reads
	// audit_log is blind to it. Which finance types the contract should carry is
	// a product decision, tracked as its own issue; ratifying them replaces
	// these seven entries with emits at the same call sites.
	"internal/modules/finance:insertInvoice":             "the mirror takes in an invoice \u2014 see the note above these seven entries",
	"internal/modules/finance:updateInvoice":             "the source restated an invoice \u2014 same ground",
	"internal/modules/finance:insertPayment":             "the mirror takes in a received payment \u2014 same ground",
	"internal/modules/finance:updatePayment":             "the source restated a payment \u2014 same ground",
	"internal/modules/finance:mirrorCustomer":            "the mirror takes in a directory entry \u2014 same ground, and this row carries no money at all: it records which name in the accounting source a link points at",
	"internal/modules/finance:updateExternalCustomer":    "the source renamed a directory entry \u2014 same ground",
	"internal/modules/finance:auditConnectionTransition": "the accounting source went down or came back \u2014 same ground. This one is a STATE change rather than a record fact, so even a ratified finance catalog would want a connection verb rather than an invoice one",
	// WriteFiltered no longer belongs here: the bulk export is a non-entity
	// operational event, so it moved from storekit.Audit to storekit.LogSystem
	// (system_log, 0074) \u2014 it writes no audit_log row at all now.
})

func TestEveryAuditedMutationEmitsAnEvent(t *testing.T) {
	defer auditOnlyWrites.AssertAllMatched(t)
	emissionPathsByDir := map[string]map[string]bool{}
	fset := token.NewFileSet()
	for _, root := range []string{"internal/modules", "internal/compose", settingsStoreDir} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
				isIntegrationTagged(path) {
				return err
			}
			path = filepath.ToSlash(path)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			dir := filepath.Dir(path)
			emissionPaths, ok := emissionPathsByDir[dir]
			if !ok {
				emissionPaths, err = emissionBearingFunctions(fset, dir)
				if err != nil {
					return err
				}
				emissionPathsByDir[dir] = emissionPaths
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				var audits, emits bool
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch node := n.(type) {
					case *ast.SelectorExpr:
						if pkg, ok := node.X.(*ast.Ident); ok && pkg.Name == "storekit" {
							switch node.Sel.Name {
							case "Audit", "AuditWithEvidence":
								audits = true
							case "Emit", "EmitEvent", "EmitEventForEntity", "EmitPipeline", "EmitPipelinePayload":
								emits = true
							}
						}
					case *ast.CallExpr:
						if callee, ok := node.Fun.(*ast.Ident); ok && emissionPaths[callee.Name] {
							emits = true
						}
					}
					return true
				})
				if audits && !emits {
					key := filepath.ToSlash(filepath.Dir(path)) + ":" + fn.Name.Name
					if auditOnlyWrites.Waived(t, key) {
						continue
					}
					t.Errorf("%s: %s calls storekit.Audit without storekit.Emit — every audited mutation ships its event, or the exception is ratified in auditOnlyWrites",
						path, fn.Name.Name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// emissionBearingFunctions follows package-local plain-function calls so the
// gate accepts one named event-envelope helper without forcing every caller to
// spell Emit. Method helpers deliberately do not qualify: a receiver abstraction
// would make the transactional write shape too hard to verify statically here.
func emissionBearingFunctions(fset *token.FileSet, dir string) (map[string]bool, error) {
	emits := map[string]bool{}
	calls := map[string][]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || isIntegrationTagged(path) {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv != nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.SelectorExpr:
					if pkg, ok := node.X.(*ast.Ident); ok && pkg.Name == "storekit" {
						switch node.Sel.Name {
						case "Emit", "EmitEvent", "EmitEventForEntity", "EmitPipeline", "EmitPipelinePayload":
							emits[fn.Name.Name] = true
						}
					}
				case *ast.CallExpr:
					if callee, ok := node.Fun.(*ast.Ident); ok {
						calls[fn.Name.Name] = append(calls[fn.Name.Name], callee.Name)
					}
				}
				return true
			})
		}
	}
	for changed := true; changed; {
		changed = false
		for caller, callees := range calls {
			if emits[caller] {
				continue
			}
			for _, callee := range callees {
				if emits[callee] {
					emits[caller] = true
					changed = true
					break
				}
			}
		}
	}
	return emits, nil
}
