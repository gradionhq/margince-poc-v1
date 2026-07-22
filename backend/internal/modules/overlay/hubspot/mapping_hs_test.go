// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot_test

import (
	"slices"
	"sort"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
)

// rawContact is a §11-shaped HubSpot contact properties map (spike
// capture from a live DACH portal), plus address properties to exercise
// the TargetAssembler kind.
func rawContact() map[string]any {
	return map[string]any{
		"hs_object_id":     "100214862042",
		"firstname":        "Christian",
		"lastname":         "Muller",
		"email":            "Christian.Mueller@Example.DE",
		"jobtitle":         "Geschäftsführer",
		"phone":            nil,
		"mobilephone":      "49 176 10042069",
		"hubspot_owner_id": "1197833249",
		"createdate":       "2024-11-15T13:27:49.194Z",
		"lastmodifieddate": "2026-05-13T06:44:38.727Z",
		"address":          "Hauptstrasse 1",
		"city":             "Munich",
		"zip":              "80331",
		"country":          "Germany",
	}
}

func TestHubSpotContactMapping(t *testing.T) {
	m, ok := hubspot.Mapping("contacts")
	if !ok {
		t.Fatalf("Mapping(%s): want a declared mapping, got ok=false", "contacts")
	}

	out, unmapped, err := overlay.Apply(m, rawContact())
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}

	if got := out["first_name"]; got != "Christian" {
		t.Errorf("first_name = %v, want Christian", got)
	}
	if got := out["last_name"]; got != "Muller" {
		t.Errorf("last_name = %v, want Muller", got)
	}
	if got := out["title"]; got != "Geschäftsführer" {
		t.Errorf("title = %v, want Geschäftsführer", got)
	}
	if got := out["external_id"]; got != "100214862042" {
		t.Errorf("external_id = %v, want 100214862042", got)
	}
	if got := out["last_synced_at"]; got != "2026-05-13T06:44:38.727Z" {
		t.Errorf("last_synced_at = %v, want the lastmodifieddate value", got)
	}
	if got := out["owner_id"]; got != "1197833249" {
		t.Errorf("owner_id = %v, want the raw hubspot_owner_id (resolved downstream)", got)
	}

	childRow, ok := out["person_email"].(map[string]any)
	if !ok {
		t.Fatalf("person_email = %#v, want a child row map", out["person_email"])
	}
	if got := childRow["email"]; got != "christian.mueller@example.de" {
		t.Errorf("person_email.email = %v, want the lowercased address", got)
	}

	address, ok := out["address"].(map[string]any)
	if !ok {
		t.Fatalf("address = %#v, want an assembled jsonb map", out["address"])
	}
	if got := address["city"]; got != "Munich" {
		t.Errorf("address.city = %v, want Munich", got)
	}

	// mobilephone, createdate, and the null phone property have no
	// declared target in the contacts subset — the design's "unmapped:
	// flag" policy (never silently dropped, UC-E18-01 F3).
	sort.Strings(unmapped)
	want := []string{"createdate", "mobilephone", "phone"}
	if len(unmapped) != len(want) {
		t.Fatalf("unmapped = %v, want %v", unmapped, want)
	}
	for i, k := range want {
		if unmapped[i] != k {
			t.Errorf("unmapped[%d] = %q, want %q (full: %v)", i, unmapped[i], k, unmapped)
		}
	}

	if m.UnmappedPolicy != "flag" {
		t.Errorf("UnmappedPolicy = %q, want %q", m.UnmappedPolicy, "flag")
	}

	// full_name is assembled from firstname + lastname, trimmed (OVA-MAP-3):
	// a required, always-present display field, never left empty.
	if got := out["full_name"]; got != "Christian Muller" {
		t.Errorf("full_name = %v, want %q (assembled firstname + lastname)", got, "Christian Muller")
	}
}

// TestHubSpotContactFullNameFidelity is the OVA-MAP-3 golden case set: a
// mirrored contact's full_name is never empty. It is assembled from
// firstname + lastname (trimmed), falling back to the primary email's local
// part, and only then to a stable non-empty placeholder.
func TestHubSpotContactFullNameFidelity(t *testing.T) {
	m, ok := hubspot.Mapping("contacts")
	if !ok {
		t.Fatal("Mapping(contacts): want a declared mapping")
	}
	cases := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"both names", map[string]any{"hs_object_id": "1", "firstname": "Ada", "lastname": "Lovelace"}, "Ada Lovelace"},
		{"only lastname", map[string]any{"hs_object_id": "2", "firstname": "", "lastname": "Lovelace"}, "Lovelace"},
		{"only firstname", map[string]any{"hs_object_id": "3", "firstname": "Ada", "lastname": ""}, "Ada"},
		{"neither name, email present", map[string]any{"hs_object_id": "4", "firstname": "", "lastname": "", "email": "grace.hopper@navy.mil"}, "grace.hopper"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := overlay.Apply(m, tc.raw)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got := out["full_name"]; got != tc.want {
				t.Errorf("full_name = %v, want %q", got, tc.want)
			}
		})
	}

	// Neither name nor email: full_name must still be non-empty (a stable
	// placeholder), never absent or "".
	out, _, err := overlay.Apply(m, map[string]any{"hs_object_id": "9999"})
	if err != nil {
		t.Fatalf("Apply (nameless, emailless): %v", err)
	}
	got, ok := out["full_name"].(string)
	if !ok || got != "(unnamed contact)" {
		t.Errorf("full_name for a nameless, emailless contact = %#v, want the stable placeholder %q", out["full_name"], "(unnamed contact)")
	}
}

// rawCompany is a §9-shaped HubSpot company properties map.
func rawCompany() map[string]any {
	return map[string]any{
		"hs_object_id":        "61655665850",
		"hs_lastmodifieddate": "2026-05-13T06:44:38.727Z",
		"name":                "Muller GmbH",
		"industry":            "HOSPITAL_HEALTH_CARE",
		"numberofemployees":   "75",
		"hubspot_owner_id":    "1197833249",
		"domain":              "muller-gmbh.example",
		"address":             "Hauptstrasse 1",
		"city":                "Munich",
		"zip":                 "80331",
		"country":             "Germany",
	}
}

func TestHubSpotCompanyMapping(t *testing.T) {
	m, ok := hubspot.Mapping("companies")
	if !ok {
		t.Fatalf("Mapping(%s): want a declared mapping, got ok=false", "companies")
	}

	out, unmapped, err := overlay.Apply(m, rawCompany())
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}

	if got := out["display_name"]; got != "Muller GmbH" {
		t.Errorf("display_name = %v, want Muller GmbH", got)
	}
	if got := out["industry"]; got != "HOSPITAL_HEALTH_CARE" {
		t.Errorf("industry = %v, want HOSPITAL_HEALTH_CARE", got)
	}
	if got := out["size_band"]; got != "51-200" {
		t.Errorf("size_band = %v, want 51-200 (bucketized from 75 employees)", got)
	}
	if got := out["owner_id"]; got != "1197833249" {
		t.Errorf("owner_id = %v, want the raw hubspot_owner_id", got)
	}
	if got := out["external_id"]; got != "61655665850" {
		t.Errorf("external_id = %v, want 61655665850", got)
	}
	address, ok := out["address"].(map[string]any)
	if !ok {
		t.Fatalf("address = %#v, want an assembled jsonb map", out["address"])
	}
	if got := address["city"]; got != "Munich" {
		t.Errorf("address.city = %v, want Munich", got)
	}

	// domain has no home column (design §9) — it must surface as
	// unmapped, never silently dropped.
	if !containsString(unmapped, "domain") {
		t.Errorf("unmapped = %v, want it to contain %q", unmapped, "domain")
	}
}

// rawDeal is a §9-shaped HubSpot deal properties map, carrying a
// negative amount to exercise the amount_minor_by_currency transform's
// round-half-away-from-zero fix on the mapping path (not just Apply
// unit-tested directly in overlay/mapping_test.go).
func rawDeal() map[string]any {
	return map[string]any{
		"hs_object_id":        "9001",
		"hs_lastmodifieddate": "2026-06-01T00:00:00.000Z",
		"dealname":            "Muller GmbH — Platform",
		"amount":              "-12.567",
		"deal_currency_code":  "EUR",
		"pipeline":            "default",
		"dealstage":           "1293549771",
		"closedate":           "2026-07-01T00:00:00.000Z",
	}
}

func TestHubSpotDealMapping(t *testing.T) {
	m, ok := hubspot.Mapping("deals")
	if !ok {
		t.Fatalf("Mapping(%s): want a declared mapping, got ok=false", "deals")
	}

	out, unmapped, err := overlay.Apply(m, rawDeal())
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}

	if got := out["name"]; got != "Muller GmbH — Platform" {
		t.Errorf("name = %v, want the dealname", got)
	}
	if got, ok := out["amount_minor"].(int64); !ok || got != -1257 {
		t.Errorf("amount_minor = %#v, want int64(-1257)", out["amount_minor"])
	}
	if got := out["currency"]; got != "EUR" {
		t.Errorf("currency = %v, want EUR", got)
	}
	if got := out["pipeline_id"]; got != "default" {
		t.Errorf("pipeline_id = %v, want default", got)
	}
	if got := out["stage_id"]; got != "1293549771" {
		t.Errorf("stage_id = %v, want 1293549771", got)
	}
	if got := out["expected_close_date"]; got != "2026-07-01T00:00:00.000Z" {
		t.Errorf("expected_close_date = %v, want the closedate value", got)
	}
	if len(unmapped) != 0 {
		t.Errorf("unmapped = %v, want none (every rawDeal property is declared)", unmapped)
	}

	// status is a target-side gap (mapping_hs.go doc comment): the
	// won/lost/open derivation needs stage metadata Apply never sees,
	// so it is the StageSemantic port's job, not a field-mapping
	// transform. Pin the absence so a future change updates this
	// assertion deliberately.
	if _, ok := out["status"]; ok {
		t.Errorf("status = %v, want absent (won/lost/open is derived via StageSemantic, not Apply)", out["status"])
	}
}

// TestHubSpotDealAmountCurrencyFidelity is the OVA-MAP-4 golden case set: a
// deal amount is scaled to minor units by the ISO-4217 minor-unit exponent
// of its deal_currency_code — never a blanket ×100. A ×100-everywhere
// implementation fails on JPY (exponent 0) and BHD (exponent 3) by
// construction. A deal with no currency code maps amount_minor to null.
func TestHubSpotDealAmountCurrencyFidelity(t *testing.T) {
	m, ok := hubspot.Mapping("deals")
	if !ok {
		t.Fatal("Mapping(deals): want a declared mapping")
	}
	cases := []struct {
		name, amount, code string
		want               int64
	}{
		{"JPY exponent 0", "1000", "JPY", 1000},
		{"EUR exponent 2", "10.00", "EUR", 1000},
		{"BHD exponent 3", "1.500", "BHD", 1500},
		{"CLF exponent 4", "1.5000", "CLF", 15000},
		{"lowercase code uppercased", "10.00", "usd", 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := overlay.Apply(m, map[string]any{"hs_object_id": "1", "amount": tc.amount, "deal_currency_code": tc.code})
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got, ok := out["amount_minor"].(int64); !ok || got != tc.want {
				t.Errorf("amount_minor for %s %s = %#v, want int64(%d)", tc.amount, tc.code, out["amount_minor"], tc.want)
			}
		})
	}

	// A deal amount with no currency code maps amount_minor to null (never a
	// guessed exponent), per OVA-MAP-4.
	out, _, err := overlay.Apply(m, map[string]any{"hs_object_id": "2", "amount": "10.00"})
	if err != nil {
		t.Fatalf("Apply (no currency): %v", err)
	}
	if v, present := out["amount_minor"]; present && v != nil {
		t.Errorf("amount_minor with no currency code = %#v, want null (absent or nil), never a guessed ×100", v)
	}

	// A currency present but the amount explicitly null (HubSpot's JSON null
	// for an unset property) also maps to null — not an error, not a coined
	// zero. It is "no amount", exactly like an absent one.
	out, _, err = overlay.Apply(m, map[string]any{"hs_object_id": "3", "amount": nil, "deal_currency_code": "EUR"})
	if err != nil {
		t.Fatalf("Apply (nil amount): %v — a null amount must map to null, never error", err)
	}
	if v, present := out["amount_minor"]; present && v != nil {
		t.Errorf("amount_minor for a nil amount = %#v, want null (absent or nil)", v)
	}
}

// TestHubSpotEngagementClassSplit is the OVA-MAP-1 golden suite: HubSpot v3
// has no generic engagements object, so the five engagement object classes
// are read separately and each maps to canonical "activity" with its OWN
// fixed kind — never a generic/other fallback, and never a kind derived from
// a record property. One fixture per class asserts the correct kind.
func TestHubSpotEngagementClassSplit(t *testing.T) {
	cases := []struct {
		class string
		raw   map[string]any
		kind  string
	}{
		{"calls", map[string]any{"hs_object_id": "1", "hs_call_title": "Intro call", "hs_call_duration": "180000"}, "call"},
		{"meetings", map[string]any{"hs_object_id": "2", "hs_meeting_title": "Kickoff", "hs_meeting_outcome": "COMPLETED"}, "meeting"},
		{"emails", map[string]any{"hs_object_id": "3", "hs_email_subject": "Proposal"}, "email"},
		{"notes", map[string]any{"hs_object_id": "4", "hs_note_body": "Left a voicemail"}, "note"},
		{"tasks", map[string]any{"hs_object_id": "5", "hs_task_subject": "Follow up"}, "task"},
	}
	for _, tc := range cases {
		t.Run(tc.class, func(t *testing.T) {
			m, ok := hubspot.Mapping(tc.class)
			if !ok {
				t.Fatalf("Mapping(%q): want a declared mapping", tc.class)
			}
			if m.Target != "activity" {
				t.Errorf("Mapping(%q).Target = %q, want activity", tc.class, m.Target)
			}
			out, _, err := overlay.Apply(m, tc.raw)
			if err != nil {
				t.Fatalf("Apply(%q): %v", tc.class, err)
			}
			if got := out["kind"]; got != tc.kind {
				t.Errorf("%q kind = %v, want %q (fixed by the object class, no generic fallback)", tc.class, got, tc.kind)
			}
		})
	}

	// The generic "engagements" object class no longer exists — v3 has no
	// such endpoint, and mapping it would reintroduce the forbidden lossy
	// class.
	if _, ok := hubspot.Mapping("engagements"); ok {
		t.Error(`Mapping("engagements"): want ok=false — v3 has no generic engagements object (OVA-MAP-1)`)
	}
}

// TestHubSpotCallFieldFidelity pins the per-kind field mapping for a call:
// the ms→seconds duration (OVA-MAP-2) and the documented hs_call_* property
// names land on the activity columns.
func TestHubSpotCallFieldFidelity(t *testing.T) {
	m, ok := hubspot.Mapping("calls")
	if !ok {
		t.Fatal("Mapping(calls): want a declared mapping")
	}
	out, _, err := overlay.Apply(m, map[string]any{
		"hs_object_id": "5501", "hs_call_title": "Intro", "hs_call_body": "Discussed scope",
		"hs_timestamp": "2026-06-02T09:00:00.000Z", "hs_call_direction": "OUTBOUND", "hs_call_duration": "90000",
		"hubspot_owner_id": "owner-9",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := out["kind"]; got != "call" {
		t.Errorf("kind = %v, want call", got)
	}
	// Every engagement class maps the owner (else the activity ingests but
	// no visibility grant is ever projected — the original OVA-MAP-1 defect).
	if got := out["owner_id"]; got != "owner-9" {
		t.Errorf("owner_id = %v, want owner-9 (engagement classes must map the owner)", got)
	}
	if got := out["subject"]; got != "Intro" {
		t.Errorf("subject = %v, want Intro (from hs_call_title)", got)
	}
	if got := out["occurred_at"]; got != "2026-06-02T09:00:00.000Z" {
		t.Errorf("occurred_at = %v, want the hs_timestamp value", got)
	}
	if got := out["direction"]; got != "OUTBOUND" {
		t.Errorf("direction = %v, want OUTBOUND", got)
	}
	if got, ok := out["duration_seconds"].(int64); !ok || got != 90 {
		t.Errorf("duration_seconds = %#v, want int64(90) — hs_call_duration ms→s (OVA-MAP-2)", out["duration_seconds"])
	}
}

// TestHubSpotTaskTimestampFidelity is the OVA-MAP-8 golden case: a task's
// hs_timestamp is its DUE time, so it maps to due_at, and occurred_at comes
// from the task's creation — never the deadline.
func TestHubSpotTaskTimestampFidelity(t *testing.T) {
	m, ok := hubspot.Mapping("tasks")
	if !ok {
		t.Fatal("Mapping(tasks): want a declared mapping")
	}
	const due = "2026-07-10T17:00:00.000Z"
	const created = "2026-07-01T08:30:00.000Z"
	out, _, err := overlay.Apply(m, map[string]any{
		"hs_object_id": "7001", "hs_task_subject": "Follow up",
		"hs_timestamp": due, "hs_createdate": created,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := out["due_at"]; got != due {
		t.Errorf("due_at = %v, want the hs_timestamp value %q", got, due)
	}
	if got := out["occurred_at"]; got != created {
		t.Errorf("occurred_at = %v, want the hs_createdate value %q (never the deadline)", got, created)
	}
}

// rawLead is a HubSpot Leads-object properties map using the REAL Leads API
// property names (OVA-MAP-5) — hs_lead_name / hs_lead_label — never the
// name/email/company a contact carries (those do not exist on the Leads
// object). email and company_name are NOT lead properties: they come from
// the associated contact (see TestAdapterEnrichLeadsDerivesContactFields).
func rawLead() map[string]any {
	return map[string]any{
		"hs_object_id":        "7701",
		"hs_lastmodifieddate": "2026-06-03T00:00:00.000Z",
		"hs_lead_name":        "Erika Musterfrau",
		"hs_lead_label":       "NEW",
		"hubspot_owner_id":    "owner-3",
	}
}

func TestHubSpotLeadMapping(t *testing.T) {
	m, ok := hubspot.Mapping("leads")
	if !ok {
		t.Fatalf("Mapping(%s): want a declared mapping, got ok=false", "leads")
	}

	out, unmapped, err := overlay.Apply(m, rawLead())
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}

	// full_name comes from the real hs_lead_name property (OVA-MAP-5).
	if got := out["full_name"]; got != "Erika Musterfrau" {
		t.Errorf("full_name = %v, want Erika Musterfrau (from hs_lead_name)", got)
	}
	// The lead maps its owner (visibility), like every other class.
	if got := out["owner_id"]; got != "owner-3" {
		t.Errorf("owner_id = %v, want owner-3", got)
	}
	// email / company_name are NOT lead properties — Apply leaves them absent;
	// they are denormalized from the associated contact by the adapter.
	if _, present := out["email"]; present {
		t.Errorf("email = %v, want absent from Apply (it is contact-association-derived, not a lead property)", out["email"])
	}
	if _, present := out["company_name"]; present {
		t.Errorf("company_name = %v, want absent from Apply (contact-association-derived)", out["company_name"])
	}

	// hs_lead_label is REQUESTED and preserved in raw under its incumbent key
	// (so the deferral is real, not comment-only); the typed status enum remap
	// is what's deferred, and the wire keeps status at its default until then.
	if got := out["hs_lead_label"]; got != "NEW" {
		t.Errorf("hs_lead_label = %v, want the raw incumbent label NEW preserved in the record", got)
	}
	if containsString(unmapped, "hs_lead_label") {
		t.Errorf("unmapped = %v, must NOT contain hs_lead_label (it is mapped to raw, not dropped)", unmapped)
	}
}

// TestIncumbentClassesForReverseResolvesEveryMappedTarget proves
// IncumbentClassesFor's reverse lookup for every declared canonical target,
// plus its honest ok=false for a canonical name with no declared mapping —
// never a guessed answer. "activity" resolves to ALL FIVE engagement classes
// (OVA-MAP-1), the plural case the single-class predecessor could not express.
func TestIncumbentClassesForReverseResolvesEveryMappedTarget(t *testing.T) {
	tests := []struct {
		canonical string
		want      []string
	}{
		{"person", []string{"contacts"}},
		{"organization", []string{"companies"}},
		{"deal", []string{"deals"}},
		{"lead", []string{"leads"}},
		{"activity", []string{"calls", "meetings", "emails", "notes", "tasks"}},
	}
	for _, tt := range tests {
		got, ok := hubspot.IncumbentClassesFor(tt.canonical)
		if !ok || !slices.Equal(got, tt.want) {
			t.Errorf("IncumbentClassesFor(%q) = (%v, %v), want (%v, true)", tt.canonical, got, ok, tt.want)
		}
	}

	if _, ok := hubspot.IncumbentClassesFor("no-such-canonical-type"); ok {
		t.Error("IncumbentClassesFor: want ok=false for a canonical name with no declared mapping")
	}
}

// TestMappingReportsAnUndeclaredObjectClassAsAnHonestGap proves Mapping's
// own documented contract: an object class outside the five §9 declares
// answers ok=false rather than a zero ObjectMapping a caller might
// silently apply — the same honest-gap contract IncumbentClassesFor keeps.
func TestMappingReportsAnUndeclaredObjectClassAsAnHonestGap(t *testing.T) {
	if _, ok := hubspot.Mapping("tickets"); ok {
		t.Fatal("Mapping(\"tickets\"): want ok=false for an undeclared object class")
	}
}

// containsString reports whether s is present in list — a small local
// helper so the unmapped-key assertions above read as membership checks
// rather than hand-rolled loops repeated per test.
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
