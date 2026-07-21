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
	objectClassContacts    = "contacts"
	objectClassCompanies   = "companies"
	objectClassDeals       = "deals"
	objectClassEngagements = "engagements"
	objectClassLeads       = "leads"
)

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
	propEmail     = "email"
	propName      = "name"
	targetAddress = "address"
	targetSubject = "subject"
	targetBody    = "body"
)

// Mapping returns the ObjectMapping for one HubSpot object class. An
// object class with no declared mapping (ok=false) is an honest gap, not
// a guessed answer — the same "flag, never guess" contract IncumbentClassFor
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
// hand; IncumbentClassFor (this file) and mappingFor (adapter.go) derive
// their lookups from THIS slice (never a second hand-written switch) so
// every direction of the source↔target correspondence can never drift
// against another as mappings are added.
var objectMappings = []overlay.ObjectMapping{
	contactsMapping, companiesMapping, dealsMapping, engagementsMapping, leadsMapping,
}

// IncumbentClassFor reverse-resolves canonical (a Margince entity-type
// name, e.g. "person") to the HubSpot object class that maps onto it
// (e.g. "contacts"). This is the seam's asymmetry, made explicit: every
// Incumbent method (Backfill/Modified/Get) takes an INCUMBENT class as
// input, while Record.ObjectClass and the mirror's own object_class
// column carry the CANONICAL name as output. A caller holding only the
// canonical name (e.g. a datasource.EntityRef.Type from the frozen
// SystemOfRecordProvider seam) must translate through this function
// before calling into an Incumbent — passing the canonical name straight
// through is exactly the mistake this function exists to prevent (see
// FreshnessReader.Read in overlay/freshness.go, the first caller). A
// canonical name with no declared mapping (ok=false) is an honest gap,
// not a guessed answer.
func IncumbentClassFor(canonical string) (string, bool) {
	for _, m := range objectMappings {
		if m.Target == canonical {
			return m.Source, true
		}
	}
	return "", false
}

// contactsMapping is the design.md §9 contacts→person subset this task
// ships. Two §9 fields are deferred because no closed-registry
// transform covers them yet, and this task does not invent one — but
// they are NOT equivalent gaps, and a caller must not treat them alike:
//
//   - social links (jsonb): its source properties are consumed by no
//     other FieldMapping, so Apply's unmapped []string surfaces them —
//     the "flag, never silently drop" policy (design §4.8) holds.
//   - full_name (an N:1 assembler over firstname/lastname/email): its
//     source keys (firstname, lastname, email) ARE already consumed by
//     the first_name/last_name/person_email.email FieldMappings below.
//     Omitting the assembler therefore produces NO unmapped key —
//     person.full_name is silently left unpopulated with zero runtime
//     signal. This is a target-side gap, invisible to the unmapped-key
//     mechanism. TestHubSpotContactMapping asserts "full_name" is
//     absent from Apply's output today so a future change that adds a
//     full_name transform without updating that test fails loudly,
//     rather than this comment being the only record of the gap.
//
// The phone/mobilephone properties (design §9: "phone→no column
// (x_phone custom)") are the ordinary case: unmapped/flagged until the
// x_ custom column lands.
var contactsMapping = overlay.ObjectMapping{
	Source:         objectClassContacts,
	Target:         "person",
	ExternalKey:    propHSObjectID,
	Baseline:       "lastmodifieddate",
	UnmappedPolicy: unmappedPolicyFlag,
	Fields: []overlay.FieldMapping{
		{From: []string{"firstname"}, To: "first_name", Kind: overlay.TargetColumn},
		{From: []string{"lastname"}, To: "last_name", Kind: overlay.TargetColumn},
		{From: []string{"jobtitle"}, To: "title", Kind: overlay.TargetColumn},
		{
			From:      []string{propEmail},
			To:        "person_email.email",
			Kind:      overlay.TargetChild,
			Transform: "lowercase",
		},
		{
			From:    []string{"hubspot_owner_id"},
			To:      "owner_id",
			Kind:    overlay.TargetColumn,
			Resolve: "mirror_user_map",
		},
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
		{From: []string{"industry"}, To: "industry", Kind: overlay.TargetColumn},
		{
			From:      []string{"numberofemployees"},
			To:        "size_band",
			Kind:      overlay.TargetColumn,
			Transform: "employees_to_size_band",
		},
		{
			From:    []string{"hubspot_owner_id"},
			To:      "owner_id",
			Kind:    overlay.TargetColumn,
			Resolve: "mirror_user_map",
		},
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
// amount_to_minor is wired here as a straight cents conversion (§9 calls
// for "decimal→minor by currency exponent" — a currency-aware exponent
// table, e.g. JPY has none, KWD has three — which is NOT in the closed
// transform registry). Using the existing cents-only transform is a
// known simplification for portals trading in two-decimal currencies;
// a currency-aware transform is a reconciliation item for the spec, not
// invented here.
var dealsMapping = overlay.ObjectMapping{
	Source:         objectClassDeals,
	Target:         "deal",
	ExternalKey:    propHSObjectID,
	Baseline:       baselineHSLastModifiedDate,
	UnmappedPolicy: unmappedPolicyFlag,
	Fields: []overlay.FieldMapping{
		{From: []string{"dealname"}, To: "name", Kind: overlay.TargetColumn},
		{
			From:      []string{"amount"},
			To:        "amount_minor",
			Kind:      overlay.TargetColumn,
			Transform: "amount_to_minor",
		},
		{From: []string{"deal_currency_code"}, To: "currency", Kind: overlay.TargetColumn},
		{From: []string{"pipeline"}, To: "pipeline_id", Kind: overlay.TargetColumn},
		{From: []string{"dealstage"}, To: "stage_id", Kind: overlay.TargetColumn},
		{From: []string{"closedate"}, To: "expected_close_date", Kind: overlay.TargetColumn},
	},
}

// engagementsMapping is the design.md §9 engagements→activity subset.
// §11's spike capture has no engagement wire sample (only contacts,
// companies, deals, pipelines, and associations were captured), so the
// property names below are the ones §9's prose names literally
// (hs_engagement_type, hs_timestamp, hs_call_duration,
// hs_meeting_outcome — all real, documented HubSpot engagement
// properties — plus subject/title and body/text, which §9 states as
// either/or per engagement subtype). This is a reconciliation item for
// upstream (contract-first, P3): a real HubSpot portal capture may
// reveal per-subtype (note/email/call/meeting/task) property variance
// this flat map can't express, at which point the map either grows
// subtype-specific FieldMappings or the mapping IR grows a fourth kind —
// neither invented here.
//
// subject/title and body/text are each declared as two FieldMappings
// landing on the same target (subject, body respectively): when only one
// of an either/or pair is present (the normal case — a call carries no
// "title", a meeting carries no "subject"), the present one lands
// untouched; if a raw record somehow carried both, the later FieldMapping
// silently wins, a documented simplification, not a data-loss defect
// (both would already report the "same" value on any real record).
var engagementsMapping = overlay.ObjectMapping{
	Source:         objectClassEngagements,
	Target:         "activity",
	ExternalKey:    propHSObjectID,
	Baseline:       baselineHSLastModifiedDate,
	UnmappedPolicy: unmappedPolicyFlag,
	Fields: []overlay.FieldMapping{
		{From: []string{"hs_engagement_type"}, To: "kind", Kind: overlay.TargetColumn, Transform: "lowercase"},
		{From: []string{targetSubject}, To: targetSubject, Kind: overlay.TargetColumn},
		{From: []string{"title"}, To: targetSubject, Kind: overlay.TargetColumn},
		{From: []string{targetBody}, To: targetBody, Kind: overlay.TargetColumn},
		{From: []string{"text"}, To: targetBody, Kind: overlay.TargetColumn},
		{From: []string{"hs_timestamp"}, To: "occurred_at", Kind: overlay.TargetColumn},
		{From: []string{"direction"}, To: "direction", Kind: overlay.TargetColumn},
		{From: []string{"hs_call_duration"}, To: "duration_seconds", Kind: overlay.TargetColumn},
		{From: []string{"hs_meeting_outcome"}, To: "meeting_status", Kind: overlay.TargetColumn},
	},
}

// leadsMapping is the design.md §9 leads→lead subset (portals with the
// Leads object). `status` is a deliberate target-side gap: §9 calls for
// mapping HubSpot's free-form lead status into the fixed
// new/working/promoted/disqualified enum, which needs an enum-remapping
// transform NOT in the closed registry ({lowercase, amount_to_minor,
// employees_to_size_band, address_json}) — per the "flag, don't invent"
// rule this task does not add one. status is left unconsumed so Apply's
// unmapped []string surfaces it, exactly like companies' domain.
var leadsMapping = overlay.ObjectMapping{
	Source:         objectClassLeads,
	Target:         "lead",
	ExternalKey:    propHSObjectID,
	Baseline:       baselineHSLastModifiedDate,
	UnmappedPolicy: unmappedPolicyFlag,
	Fields: []overlay.FieldMapping{
		{From: []string{propName}, To: "full_name", Kind: overlay.TargetColumn},
		{From: []string{propEmail}, To: propEmail, Kind: overlay.TargetColumn},
		{From: []string{"company"}, To: "company_name", Kind: overlay.TargetColumn},
	},
}
