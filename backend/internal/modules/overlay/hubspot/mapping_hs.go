// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package hubspot holds the HubSpot-specific mapping-as-contract
// declarations (design.md §4.8/§9): code-declared, test-guarded field
// maps from HubSpot's typed properties to Margince mirror columns,
// expressed in the overlay package's mapping IR.
package hubspot

import "github.com/gradionhq/margince/backend/internal/modules/overlay"

// The five HubSpot object classes design.md §9 maps — the one spelling
// shared with adapter.go's per-object watermark-property switch (design.md
// §7) and with Backfill's association-target table (overlay/backfill.go),
// so incumbent class names never drift across those three call sites.
const (
	objectClassContacts  = "contacts"
	objectClassCompanies = "companies"
	objectClassDeals     = "deals"
	objectClassLeads     = "leads"
	// The five v3 engagement object classes (OVA-MAP-1) — read separately,
	// each mapped to canonical "activity" with its own fixed kind.
	objectClassCalls    = "calls"
	objectClassMeetings = "meetings"
	objectClassEmails   = "emails"
	objectClassNotes    = "notes"
	objectClassTasks    = "tasks"
)

// activityTarget is the canonical Margince type all five engagement classes
// map onto.
const activityTarget = "activity"

// baselineHSLastModifiedDate is the watermark property every object
// class but contacts uses (design.md §7, spike-confirmed) — shared with
// adapter.go's watermarkProperty so the two can never spell it
// differently.
const baselineHSLastModifiedDate = "hs_lastmodifieddate"

// unmappedPolicyFlag is every ObjectMapping's UnmappedPolicy value below
// (design.md §4.8: "unmapped: flag" — surface, never silently drop).
const unmappedPolicyFlag = "flag"

// Property/target names repeated across more than one ObjectMapping —
// named once so the mappings below select a value rather than retyping
// the same literal, and so a future rename touches one declaration.
const (
	propEmail       = "email"
	propName        = "name"
	propFirstname   = "firstname"
	propLastname    = "lastname"
	propLeadLabel   = "hs_lead_label"
	targetAddress   = "address"
	targetSubject   = "subject"
	targetBody      = "body"
	targetFullName  = "full_name"
	targetKind      = "kind"
	targetOccurred  = "occurred_at"
	targetDirection = "direction"
	propHSTimestamp = "hs_timestamp"
	// industryField is the one spelling shared by companies' HubSpot
	// `industry` property and the canonical `industry` mirror column — the
	// same word both sides of the mapping, named once so read and write
	// select it rather than retype the literal.
	industryField = "industry"
)

// The five canonical activity kinds (OVA-MAP-1) — the Const value each
// engagement read mapping stamps and the key each write spec selects its
// engagement class by (OVA-MAP-W3). Named once so read and write can never
// spell a kind differently.
const (
	kindCall    = "call"
	kindMeeting = "meeting"
	kindEmail   = "email"
	kindNote    = "note"
	kindTask    = "task"
)

// Mapping returns the ObjectMapping for one HubSpot object class. An
// object class with no declared mapping (ok=false) is an honest gap, not
// a guessed answer — the same "flag, never guess" contract IncumbentClassesFor
// keeps for the reverse direction; the caller decides what an undeclared
// class means rather than the lookup crashing the process.
func Mapping(source string) (overlay.ObjectMapping, bool) {
	for _, m := range objectMappings {
		if m.Source == source {
			return m, true
		}
	}
	return overlay.ObjectMapping{}, false
}

// objectMappings is the enumerable registry Mapping's switch encodes by
// hand; IncumbentClassesFor (this file) and mappingFor (adapter.go) derive
// their lookups from THIS slice (never a second hand-written switch) so
// every direction of the source↔target correspondence can never drift
// against another as mappings are added.
var objectMappings = []overlay.ObjectMapping{
	contactsMapping, companiesMapping, dealsMapping, leadsMapping,
	callsMapping, meetingsMapping, emailsMapping, notesMapping, tasksMapping,
}

// IncumbentClassesFor reverse-resolves canonical (a Margince entity-type
// name, e.g. "person") to the HubSpot object class(es) that map onto it
// (e.g. "contacts"). This is the seam's asymmetry, made explicit: every
// Incumbent method (Backfill/Modified/Get) takes an INCUMBENT class as
// input, while Record.ObjectClass and the mirror's own object_class column
// carry the CANONICAL name as output. A caller holding only the canonical
// name (e.g. a datasource.EntityRef.Type from the frozen
// SystemOfRecordProvider seam) must translate through this function before
// calling into an Incumbent.
//
// The result is a SLICE because a canonical type can be backed by more than
// one incumbent class: "activity" is the five v3 engagement classes
// (calls/meetings/emails/notes/tasks) at once (OVA-MAP-1), so a completeness
// question ("is activity fully backfilled?") means "are all five done", and
// a single-record force-fresh of an activity is under-determined until the
// mirror row records which class it came from (a tracked follow-up; no
// force-fresh caller exists yet). Classes are returned in objectMappings
// order. A canonical name with no declared mapping (ok=false) is an honest
// gap, not a guessed answer.
func IncumbentClassesFor(canonical string) ([]string, bool) {
	var classes []string
	for _, m := range objectMappings {
		if m.Target == canonical {
			classes = append(classes, m.Source)
		}
	}
	return classes, len(classes) > 0
}

// ownerIDField maps HubSpot's hubspot_owner_id onto the mirror owner_id,
// resolved to an app_user through mirror_user_map. EVERY mirrored object
// class carries it: ProjectOwnerVisibility grants the owner's seats
// visibility from this field, so a class that omitted it would ingest rows
// that no one can ever see (the OVA-MAP-1 activities' original defect).
var ownerIDField = overlay.FieldMapping{
	From:    []string{"hubspot_owner_id"},
	To:      "owner_id",
	Kind:    overlay.TargetColumn,
	Resolve: "mirror_user_map",
}

// contactsMapping is the design.md §9 contacts→person subset. full_name is
// assembled from firstname/lastname (falling back to the email local part,
// then a stable placeholder) by the full_name transform, declared AlwaysEmit
// so a required display field is never left empty (OVA-MAP-3). first_name and
// last_name are still mapped through as-is (nullable), and email is still
// consumed by person_email.email — the assembler is an ADDITIONAL reader of
// those keys, so it adds no unmapped entry.
//
// One §9 field remains a deliberate gap: social links (jsonb) has no
// closed-registry transform yet, and its source properties are consumed by
// no FieldMapping, so Apply's unmapped []string surfaces them — the "flag,
// never silently drop" policy (design §4.8) holds. The phone/mobilephone
// properties (design §9: "phone→no column (x_phone custom)") are the same
// ordinary case: unmapped/flagged until the x_ custom column lands.
var contactsMapping = overlay.ObjectMapping{
	Source:         objectClassContacts,
	Target:         "person",
	ExternalKey:    propHSObjectID,
	Baseline:       "lastmodifieddate",
	UnmappedPolicy: unmappedPolicyFlag,
	Fields: []overlay.FieldMapping{
		{From: []string{propFirstname}, To: "first_name", Kind: overlay.TargetColumn},
		{From: []string{propLastname}, To: "last_name", Kind: overlay.TargetColumn},
		{
			From:       []string{propFirstname, propLastname, propEmail},
			To:         targetFullName,
			Kind:       overlay.TargetAssembler,
			Transform:  "full_name",
			AlwaysEmit: true,
		},
		{From: []string{"jobtitle"}, To: "title", Kind: overlay.TargetColumn},
		{
			From:      []string{propEmail},
			To:        "person_email.email",
			Kind:      overlay.TargetChild,
			Transform: "lowercase",
		},
		ownerIDField,
		{
			From:      []string{"address", "city", "state", "zip", "country"},
			To:        targetAddress,
			Kind:      overlay.TargetAssembler,
			Transform: "address_json",
		},
	},
}

// companiesMapping is the design.md §9 companies→organization subset.
// `domain` has no home column (§9: "domain→no column (x_domain/raw)") —
// left unconsumed so Apply's unmapped []string surfaces it, the same
// "flag, never silently drop" treatment contacts' phone gets.
var companiesMapping = overlay.ObjectMapping{
	Source:         objectClassCompanies,
	Target:         "organization",
	ExternalKey:    propHSObjectID,
	Baseline:       baselineHSLastModifiedDate,
	UnmappedPolicy: unmappedPolicyFlag,
	Fields: []overlay.FieldMapping{
		{From: []string{propName}, To: "display_name", Kind: overlay.TargetColumn},
		{From: []string{industryField}, To: industryField, Kind: overlay.TargetColumn},
		{
			From:      []string{"numberofemployees"},
			To:        "size_band",
			Kind:      overlay.TargetColumn,
			Transform: "employees_to_size_band",
		},
		ownerIDField,
		{
			From:      []string{"address", "city", "state", "zip", "country"},
			To:        targetAddress,
			Kind:      overlay.TargetAssembler,
			Transform: "address_json",
		},
	},
}

// dealsMapping is the design.md §9 deals→deal subset. Two target-side
// gaps, both deliberate:
//
//   - status: §9 derives won/lost/open from HubSpot's per-portal stage
//     metadata (isClosed/probability), not from the raw dealstage string
//     Apply sees — that derivation is the StageSemantic port's job
//     (provider.go declares it ErrUnsupportedBySoR pending a stage-
//     metadata source), never a field-mapping transform. dealstage is
//     still consumed, mapped to stage_id — it is not left unmapped, only
//     the second, semantic-derived target is absent.
//   - organization_id: §9's "assoc→company→organization_id" is derived
//     from the v4 associations edge, not a deal property — Backfill
//     (overlay/backfill.go's backfillAssocTargets) fetches and upserts
//     deals→companies associations directly; there is no properties-map
//     key for it, so it never appears in Apply's unmapped list either.
//
// amount_minor is assembled from amount + deal_currency_code and scaled by
// the currency's ISO-4217 minor-unit exponent (OVA-MAP-4) — JPY ×1, EUR/USD
// ×100, BHD ×1000 — never a blanket ×100; a deal with no currency code maps
// amount_minor to null rather than guessing an exponent. currency carries the
// code itself, uppercased.
var dealsMapping = overlay.ObjectMapping{
	Source:         objectClassDeals,
	Target:         "deal",
	ExternalKey:    propHSObjectID,
	Baseline:       baselineHSLastModifiedDate,
	UnmappedPolicy: unmappedPolicyFlag,
	Fields: []overlay.FieldMapping{
		{From: []string{"dealname"}, To: "name", Kind: overlay.TargetColumn},
		{
			From:      []string{"amount", "deal_currency_code"},
			To:        "amount_minor",
			Kind:      overlay.TargetAssembler,
			Transform: "amount_minor_by_currency",
		},
		{From: []string{"deal_currency_code"}, To: "currency", Kind: overlay.TargetColumn, Transform: "uppercase"},
		{From: []string{"pipeline"}, To: "pipeline_id", Kind: overlay.TargetColumn},
		{From: []string{"dealstage"}, To: "stage_id", Kind: overlay.TargetColumn},
		{From: []string{"closedate"}, To: "expected_close_date", Kind: overlay.TargetColumn},
	},
}

// The five engagement→activity mappings (OVA-MAP-1). HubSpot v3 exposes no
// generic engagements object: calls/meetings/emails/notes/tasks are each
// their own /crm/v3/objects/<class> endpoint with their own typed
// properties, and each lands on canonical "activity" with a FIXED kind
// carried in Const (determined by the class read, never from a
// hs_engagement_type field — a single lossy generic-engagement class is
// forbidden). Per-kind field constraints hold exactly as for native
// activities: meeting_status is meeting-only; call direction/duration are
// call-only.
//
// The property names are the documented HubSpot v3 engagement properties
// (hs_call_title/body/duration/direction, hs_meeting_title/body/outcome/
// start_time, hs_email_subject/text/direction, hs_note_body, hs_task_subject/
// body — all real). A live portal capture may still reveal per-class
// property variance (e.g. task status/priority columns) beyond this subset;
// per "flag, don't invent" anything not mapped surfaces via Apply's unmapped
// list rather than being guessed into a column, and remains a contract-first
// reconciliation item (P3).
var callsMapping = overlay.ObjectMapping{
	Source:         objectClassCalls,
	Target:         activityTarget,
	ExternalKey:    propHSObjectID,
	Baseline:       baselineHSLastModifiedDate,
	UnmappedPolicy: unmappedPolicyFlag,
	Const:          map[string]any{targetKind: kindCall},
	Fields: []overlay.FieldMapping{
		{From: []string{"hs_call_title"}, To: targetSubject, Kind: overlay.TargetColumn},
		{From: []string{"hs_call_body"}, To: targetBody, Kind: overlay.TargetColumn},
		{From: []string{propHSTimestamp}, To: targetOccurred, Kind: overlay.TargetColumn},
		{From: []string{"hs_call_direction"}, To: targetDirection, Kind: overlay.TargetColumn},
		{From: []string{"hs_call_duration"}, To: "duration_seconds", Kind: overlay.TargetColumn, Transform: "ms_to_seconds"},
		ownerIDField,
	},
}

var meetingsMapping = overlay.ObjectMapping{
	Source:         objectClassMeetings,
	Target:         activityTarget,
	ExternalKey:    propHSObjectID,
	Baseline:       baselineHSLastModifiedDate,
	UnmappedPolicy: unmappedPolicyFlag,
	Const:          map[string]any{targetKind: kindMeeting},
	Fields: []overlay.FieldMapping{
		{From: []string{"hs_meeting_title"}, To: targetSubject, Kind: overlay.TargetColumn},
		{From: []string{"hs_meeting_body"}, To: targetBody, Kind: overlay.TargetColumn},
		{From: []string{"hs_meeting_start_time"}, To: targetOccurred, Kind: overlay.TargetColumn},
		{From: []string{"hs_meeting_outcome"}, To: "meeting_status", Kind: overlay.TargetColumn},
		ownerIDField,
	},
}

var emailsMapping = overlay.ObjectMapping{
	Source:         objectClassEmails,
	Target:         activityTarget,
	ExternalKey:    propHSObjectID,
	Baseline:       baselineHSLastModifiedDate,
	UnmappedPolicy: unmappedPolicyFlag,
	Const:          map[string]any{targetKind: kindEmail},
	Fields: []overlay.FieldMapping{
		{From: []string{"hs_email_subject"}, To: targetSubject, Kind: overlay.TargetColumn},
		{From: []string{"hs_email_text"}, To: targetBody, Kind: overlay.TargetColumn},
		{From: []string{propHSTimestamp}, To: targetOccurred, Kind: overlay.TargetColumn},
		{From: []string{"hs_email_direction"}, To: targetDirection, Kind: overlay.TargetColumn},
		ownerIDField,
	},
}

var notesMapping = overlay.ObjectMapping{
	Source:         objectClassNotes,
	Target:         activityTarget,
	ExternalKey:    propHSObjectID,
	Baseline:       baselineHSLastModifiedDate,
	UnmappedPolicy: unmappedPolicyFlag,
	Const:          map[string]any{targetKind: kindNote},
	Fields: []overlay.FieldMapping{
		{From: []string{"hs_note_body"}, To: targetBody, Kind: overlay.TargetColumn},
		{From: []string{propHSTimestamp}, To: targetOccurred, Kind: overlay.TargetColumn},
		ownerIDField,
	},
}

// tasksMapping differs from the other four engagement classes on the
// timestamp (OVA-MAP-8): a task's hs_timestamp is its DUE time, not its
// occurrence, so it maps to due_at (task-only per the Activity schema) and
// occurred_at is sourced from the task's creation timestamp — mapping the due
// time to occurred_at would date the task to its deadline.
var tasksMapping = overlay.ObjectMapping{
	Source:         objectClassTasks,
	Target:         activityTarget,
	ExternalKey:    propHSObjectID,
	Baseline:       baselineHSLastModifiedDate,
	UnmappedPolicy: unmappedPolicyFlag,
	Const:          map[string]any{targetKind: kindTask},
	Fields: []overlay.FieldMapping{
		{From: []string{"hs_task_subject"}, To: targetSubject, Kind: overlay.TargetColumn},
		{From: []string{"hs_task_body"}, To: targetBody, Kind: overlay.TargetColumn},
		{From: []string{propHSTimestamp}, To: "due_at", Kind: overlay.TargetColumn},
		{From: []string{"hs_createdate"}, To: targetOccurred, Kind: overlay.TargetColumn},
		ownerIDField,
	},
}

// leadsMapping is the leads→lead subset via the REAL HubSpot Leads object
// properties (OVA-MAP-5): a standard lead carries its own `hs_lead_name`
// (→ full_name), never the `name`/`email`/`company` a contact carries — those
// property names do not exist on the Leads object and were the prior mapping's
// invention. A lead's email and company_name are NOT lead properties: they are
// derived from the lead's REQUIRED contact association (adapter.enrichLeads),
// with company_name staying free text (never an org FK, per the Lead schema).
//
// Two deliberate deferrals, never invented:
//   - `hs_lead_label` → lead.status: the raw label is REQUESTED and preserved
//     in raw (under its own incumbent key), so it actually rides the mirror
//     record rather than being silently dropped; the typed status enum remap
//     still waits on a documented transform + a real capture, the same
//     "flag, don't invent" deferral the email-direction / meeting-status
//     remaps take. (A comment-only claim of "surfaced" would be false: a
//     property no FieldMapping names is never requested from HubSpot, so it
//     would never appear — mapping it to raw is what makes the deferral real.)
//   - a lead with NO contact association keeps email/company_name null rather
//     than inventing them.
var leadsMapping = overlay.ObjectMapping{
	Source:         objectClassLeads,
	Target:         "lead",
	ExternalKey:    propHSObjectID,
	Baseline:       baselineHSLastModifiedDate,
	UnmappedPolicy: unmappedPolicyFlag,
	Fields: []overlay.FieldMapping{
		{From: []string{"hs_lead_name"}, To: targetFullName, Kind: overlay.TargetColumn},
		{From: []string{propLeadLabel}, To: propLeadLabel, Kind: overlay.TargetColumn},
		ownerIDField,
	},
}
