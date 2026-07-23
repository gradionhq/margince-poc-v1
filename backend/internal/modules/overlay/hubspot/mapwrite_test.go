// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// The golden write-mapping suite: one assertion group per OVA-MAP-W rule,
// the inverse of the OVA-AC-4 read suite (AC-OV-12), plus the write-back
// hardening rules (clear-field create/update semantics, type strictness,
// enum casing, integer precision). A ÷100-everywhere, full_name-splitting,
// native-UUID-stage, or occurred_at-writing implementation fails here by
// construction.

// OVA-MAP-W1 — person first_name/last_name → firstname/lastname; full_name is
// the assembled display field and is NEVER written back; a create carrying
// only read-only fields sets no incumbent property.
func TestMapWritePersonNamesW1(t *testing.T) {
	got, err := mapWrite("person", map[string]any{"first_name": "Ada", "last_name": "Lovelace"}, false)
	if err != nil {
		t.Fatalf("mapWrite person: %v", err)
	}
	if got.ObjectClass != objectClassContacts {
		t.Errorf("object class = %q, want %q", got.ObjectClass, objectClassContacts)
	}
	if got.Props["firstname"] != "Ada" || got.Props["lastname"] != "Lovelace" {
		t.Errorf("props = %#v, want firstname=Ada lastname=Lovelace", got.Props)
	}
}

func TestMapWritePersonFullNameIsReadOnlyW1(t *testing.T) {
	got, err := mapWrite("person", map[string]any{"full_name": "Ada Lovelace"}, false)
	if err != nil {
		t.Fatalf("mapWrite person full_name only: %v", err)
	}
	if len(got.Props) != 0 {
		t.Errorf("a write carrying only full_name must set no incumbent property, got %#v", got.Props)
	}
}

// Clear-field semantics: an explicit "" clears the property on UPDATE (sent to
// HubSpot) but is nothing to set on CREATE (skipped).
func TestMapWriteClearFieldOnUpdateOnly(t *testing.T) {
	upd, err := mapWrite("person", map[string]any{"title": ""}, true)
	if err != nil {
		t.Fatalf("mapWrite update clear: %v", err)
	}
	if v, ok := upd.Props["jobtitle"]; !ok || v != "" {
		t.Errorf("update title=\"\" must clear jobtitle (send \"\"), got props %#v", upd.Props)
	}
	cre, err := mapWrite("person", map[string]any{"title": ""}, false)
	if err != nil {
		t.Fatalf("mapWrite create empty title: %v", err)
	}
	if _, ok := cre.Props["jobtitle"]; ok {
		t.Errorf("create title=\"\" is nothing to set, got props %#v", cre.Props)
	}
}

// A non-string value in a string field is a type error (matching the native
// provider's 422), never coerced into a HubSpot string property.
func TestMapWriteRejectsNonStringScalar(t *testing.T) {
	if _, err := mapWrite("person", map[string]any{"first_name": float64(42)}, false); err == nil {
		t.Error("a numeric value in a string field must be a type error")
	}
}

// OVA-MAP-W2 — amount_minor + currency → amount scaled BACK by the ISO-4217
// minor-unit exponent (never a blanket ÷100); deal_currency_code from currency.
func TestMapWriteDealAmountByCurrencyW2(t *testing.T) {
	cases := []struct {
		currency string
		minor    int64
		want     string
	}{
		{"JPY", 1000, "1000"},
		{"EUR", 1000, "10.00"},
		{"BHD", 1500, "1.500"},
	}
	for _, c := range cases {
		got, err := mapWrite("deal", map[string]any{"amount_minor": json.Number(strconv.FormatInt(c.minor, 10)), "currency": c.currency}, false)
		if err != nil {
			t.Fatalf("mapWrite deal %s: %v", c.currency, err)
		}
		if got.Props["amount"] != c.want {
			t.Errorf("%s: amount = %q, want %q", c.currency, got.Props["amount"], c.want)
		}
		if got.Props["deal_currency_code"] != c.currency {
			t.Errorf("%s: deal_currency_code = %q, want %q", c.currency, got.Props["deal_currency_code"], c.currency)
		}
	}
}

func TestMapWriteDealNullAmountWritesNoAmountW2(t *testing.T) {
	got, err := mapWrite("deal", map[string]any{"amount_minor": nil, "currency": "EUR"}, false)
	if err != nil {
		t.Fatalf("mapWrite deal null amount: %v", err)
	}
	if _, ok := got.Props["amount"]; ok {
		t.Errorf("a null amount_minor must write no amount, got %q", got.Props["amount"])
	}
}

func TestMapWriteDealNegativeAmount(t *testing.T) {
	got, err := mapWrite("deal", map[string]any{"amount_minor": json.Number("-1500"), "currency": "BHD"}, false)
	if err != nil {
		t.Fatalf("mapWrite deal negative: %v", err)
	}
	if got.Props["amount"] != "-1.500" {
		t.Errorf("amount = %q, want -1.500", got.Props["amount"])
	}
}

// Large amount_minor must survive without a float64 precision loss (the write
// path decodes with UseNumber → json.Number → exact int64).
func TestMapWriteDealLargeAmountPrecise(t *testing.T) {
	got, err := mapWrite("deal", map[string]any{"amount_minor": json.Number("9007199254740993"), "currency": "JPY"}, false)
	if err != nil {
		t.Fatalf("mapWrite deal large: %v", err)
	}
	if got.Props["amount"] != "9007199254740993" {
		t.Errorf("amount = %q, want 9007199254740993 (no float64 rounding)", got.Props["amount"])
	}
}

// OVA-MAP-W3 — activity kind selects the engagement class; duration_seconds →
// hs_call_duration ×1000; a task's due_at → hs_timestamp; occurred_at is
// read-only; direction is uppercased to HubSpot's pinned enum; meeting_status
// is deferred (never written raw).
func TestMapWriteActivityCallW3(t *testing.T) {
	got, err := mapWrite("activity", map[string]any{
		"kind":             "call",
		"duration_seconds": json.Number("90"),
		"direction":        "outbound",
		"occurred_at":      "2026-01-02T15:04:05Z",
	}, false)
	if err != nil {
		t.Fatalf("mapWrite activity call: %v", err)
	}
	if got.ObjectClass != objectClassCalls {
		t.Errorf("object class = %q, want %q", got.ObjectClass, objectClassCalls)
	}
	if got.Props["hs_call_duration"] != "90000" {
		t.Errorf("hs_call_duration = %q, want 90000", got.Props["hs_call_duration"])
	}
	if got.Props["hs_call_direction"] != "OUTBOUND" {
		t.Errorf("hs_call_direction = %q, want OUTBOUND (uppercased enum)", got.Props["hs_call_direction"])
	}
	if _, ok := got.Props[propHSTimestamp]; ok {
		t.Errorf("occurred_at is read-only and must never be written")
	}
}

func TestMapWriteActivityMeetingStatusDeferred(t *testing.T) {
	got, err := mapWrite("activity", map[string]any{"kind": "meeting", "meeting_status": "held"}, false)
	if err != nil {
		t.Fatalf("mapWrite meeting: %v", err)
	}
	if _, ok := got.Props["hs_meeting_outcome"]; ok {
		t.Error("meeting_status projection is deferred (pinned enum vocabulary) — must not be written raw")
	}
}

func TestMapWriteActivityTaskDueAtW3(t *testing.T) {
	due := time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC)
	got, err := mapWrite("activity", map[string]any{"kind": "task", "due_at": due.Format(time.RFC3339)}, false)
	if err != nil {
		t.Fatalf("mapWrite task: %v", err)
	}
	if got.ObjectClass != objectClassTasks {
		t.Errorf("object class = %q, want %q", got.ObjectClass, objectClassTasks)
	}
	if got.Props[propHSTimestamp] != "1772614800000" {
		t.Errorf("hs_timestamp = %q, want 1772614800000 (due_at millis)", got.Props[propHSTimestamp])
	}
}

func TestMapWriteActivityNoKind(t *testing.T) {
	if _, err := mapWrite("activity", map[string]any{"subject": "hi"}, false); err == nil {
		t.Error("an activity write with no kind must error")
	}
}

func TestMapWriteActivityUnknownKind(t *testing.T) {
	if _, err := mapWrite("activity", map[string]any{"kind": "sms"}, false); err == nil {
		t.Error("an unknown activity kind must error")
	}
}

func TestMapWriteActivityBadDueAt(t *testing.T) {
	if _, err := mapWrite("activity", map[string]any{"kind": "task", "due_at": "not-a-time"}, false); err == nil {
		t.Error("an unparseable task due_at must error")
	}
}

// OVA-MAP-W5 — lead full_name → hs_lead_name; email/company_name are
// association-derived and read-only; status/label is deferred (both directions).
func TestMapWriteLeadPropsW5(t *testing.T) {
	got, err := mapWrite("lead", map[string]any{
		"full_name":    "Grace Hopper",
		"status":       "qualified",
		"email":        "grace@example.com",
		"company_name": "Navy",
	}, false)
	if err != nil {
		t.Fatalf("mapWrite lead: %v", err)
	}
	if got.ObjectClass != objectClassLeads {
		t.Errorf("object class = %q, want %q", got.ObjectClass, objectClassLeads)
	}
	if got.Props["hs_lead_name"] != "Grace Hopper" {
		t.Errorf("hs_lead_name = %q, want Grace Hopper", got.Props["hs_lead_name"])
	}
	for _, k := range []string{"hs_lead_label", "email", "company_name"} {
		if _, ok := got.Props[k]; ok {
			t.Errorf("%s must not be written (deferred/association-derived)", k)
		}
	}
}

// OVA-MAP-W: organization display_name→name, industry→industry; size_band
// read-only.
func TestMapWriteOrganization(t *testing.T) {
	got, err := mapWrite("organization", map[string]any{
		"display_name": "Acme",
		"industry":     "Manufacturing",
		"size_band":    "51-200",
	}, false)
	if err != nil {
		t.Fatalf("mapWrite organization: %v", err)
	}
	if got.ObjectClass != objectClassCompanies {
		t.Errorf("object class = %q, want %q", got.ObjectClass, objectClassCompanies)
	}
	if got.Props["name"] != "Acme" || got.Props["industry"] != "Manufacturing" {
		t.Errorf("props = %#v, want name=Acme industry=Manufacturing", got.Props)
	}
	if _, ok := got.Props["numberofemployees"]; ok {
		t.Error("size_band is read-only — must not be written")
	}
}

func TestMapWriteUnknownClass(t *testing.T) {
	if _, err := mapWrite("widget", map[string]any{"x": "y"}, false); err == nil {
		t.Error("mapWrite of an unknown canonical class must error")
	}
}
