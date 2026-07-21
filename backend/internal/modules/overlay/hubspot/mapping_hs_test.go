// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot_test

import (
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

	// full_name is a target-side gap (mapping_hs.go doc comment): its
	// source keys (firstname/lastname/email) are already consumed by
	// other FieldMappings, so it produces no unmapped key — the only
	// way to catch a silent drop is to pin the current absence here. A
	// future full_name assembler must update this assertion, not just
	// the doc comment.
	if _, ok := out["full_name"]; ok {
		t.Errorf("full_name = %v, want absent (no full_name assembler is declared yet)", out["full_name"])
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
// negative amount to exercise the amount_to_minor transform's
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

// rawEngagement is a HubSpot engagement properties map using the
// property names design.md §9 names literally (no §11 spike capture
// exists for engagements — see the engagementsMapping doc comment).
func rawEngagement() map[string]any {
	return map[string]any{
		"hs_object_id":        "5501",
		"hs_lastmodifieddate": "2026-06-02T00:00:00.000Z",
		"hs_engagement_type":  "CALL",
		"hs_timestamp":        "2026-06-02T09:00:00.000Z",
		"direction":           "OUTBOUND",
		"hs_call_duration":    "180000",
	}
}

func TestHubSpotEngagementMapping(t *testing.T) {
	m, ok := hubspot.Mapping("engagements")
	if !ok {
		t.Fatalf("Mapping(%s): want a declared mapping, got ok=false", "engagements")
	}

	out, unmapped, err := overlay.Apply(m, rawEngagement())
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}

	if got := out["kind"]; got != "call" {
		t.Errorf("kind = %v, want call (lowercased from CALL)", got)
	}
	if got := out["occurred_at"]; got != "2026-06-02T09:00:00.000Z" {
		t.Errorf("occurred_at = %v, want the hs_timestamp value", got)
	}
	if got := out["direction"]; got != "OUTBOUND" {
		t.Errorf("direction = %v, want OUTBOUND", got)
	}
	if got := out["duration_seconds"]; got != "180000" {
		t.Errorf("duration_seconds = %v, want the raw hs_call_duration", got)
	}
	if got := out["external_id"]; got != "5501" {
		t.Errorf("external_id = %v, want 5501", got)
	}
	if len(unmapped) != 0 {
		t.Errorf("unmapped = %v, want none (every rawEngagement property is declared)", unmapped)
	}
}

// rawLead is a §9-shaped HubSpot lead properties map.
func rawLead() map[string]any {
	return map[string]any{
		"hs_object_id":        "7701",
		"hs_lastmodifieddate": "2026-06-03T00:00:00.000Z",
		"name":                "Erika Musterfrau",
		"email":               "erika@example.de",
		"company":             "Musterfrau Consulting",
		"status":              "NEW",
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

	if got := out["full_name"]; got != "Erika Musterfrau" {
		t.Errorf("full_name = %v, want Erika Musterfrau", got)
	}
	if got := out["email"]; got != "erika@example.de" {
		t.Errorf("email = %v, want erika@example.de", got)
	}
	if got := out["company_name"]; got != "Musterfrau Consulting" {
		t.Errorf("company_name = %v, want Musterfrau Consulting", got)
	}

	// status needs an enum-remapping transform outside the closed
	// registry (mapping_hs.go doc comment) — flagged, not invented.
	if !containsString(unmapped, "status") {
		t.Errorf("unmapped = %v, want it to contain %q", unmapped, "status")
	}
}

// TestIncumbentClassForReverseResolvesEveryMappedTarget proves
// IncumbentClassFor's reverse lookup for every declared canonical target,
// plus its honest ok=false for a canonical name with no declared mapping
// — never a guessed answer (its own doc comment).
func TestIncumbentClassForReverseResolvesEveryMappedTarget(t *testing.T) {
	tests := []struct {
		canonical string
		want      string
	}{
		{"person", "contacts"},
		{"organization", "companies"},
		{"deal", "deals"},
		{"activity", "engagements"},
		{"lead", "leads"},
	}
	for _, tt := range tests {
		got, ok := hubspot.IncumbentClassFor(tt.canonical)
		if !ok || got != tt.want {
			t.Errorf("IncumbentClassFor(%q) = (%q, %v), want (%q, true)", tt.canonical, got, ok, tt.want)
		}
	}

	if _, ok := hubspot.IncumbentClassFor("no-such-canonical-type"); ok {
		t.Error("IncumbentClassFor: want ok=false for a canonical name with no declared mapping")
	}
}

// TestMappingReportsAnUndeclaredObjectClassAsAnHonestGap proves Mapping's
// own documented contract: an object class outside the five §9 declares
// answers ok=false rather than a zero ObjectMapping a caller might
// silently apply — the same honest-gap contract IncumbentClassFor keeps.
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
