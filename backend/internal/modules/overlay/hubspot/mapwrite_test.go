// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot

import (
	"testing"
	"time"
)

// The golden write-mapping suite: one assertion group per OVA-MAP-W rule,
// the inverse of the OVA-AC-4 read suite (AC-OV-12). A ÷100-everywhere,
// full_name-splitting, native-UUID-stage, or occurred_at-writing
// implementation fails here by construction.

// OVA-MAP-W1 — person first_name/last_name → firstname/lastname; full_name
// is the assembled display field and is NEVER written back; a write of only
// read-only fields sets no incumbent property.
func TestMapWritePersonNamesW1(t *testing.T) {
	got, err := mapWrite("person", map[string]any{
		"first_name": "Ada",
		"last_name":  "Lovelace",
	})
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
	got, err := mapWrite("person", map[string]any{"full_name": "Ada Lovelace"})
	if err != nil {
		t.Fatalf("mapWrite person full_name only: %v", err)
	}
	if len(got.Props) != 0 {
		t.Errorf("a write carrying only full_name must set no incumbent property, got %#v", got.Props)
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
		{"JPY", 1000, "1000"},  // exponent 0 → ÷1
		{"EUR", 1000, "10.00"}, // exponent 2 → ÷100
		{"BHD", 1500, "1.500"}, // exponent 3 → ÷1000
	}
	for _, c := range cases {
		got, err := mapWrite("deal", map[string]any{
			"amount_minor": float64(c.minor), // JSON-decoded numbers are float64
			"currency":     c.currency,
		})
		if err != nil {
			t.Fatalf("mapWrite deal %s: %v", c.currency, err)
		}
		if got.ObjectClass != objectClassDeals {
			t.Errorf("%s: object class = %q, want %q", c.currency, got.ObjectClass, objectClassDeals)
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
	got, err := mapWrite("deal", map[string]any{
		"amount_minor": nil,
		"currency":     "EUR",
	})
	if err != nil {
		t.Fatalf("mapWrite deal null amount: %v", err)
	}
	if _, ok := got.Props["amount"]; ok {
		t.Errorf("a null amount_minor must write no amount (never a guessed zero), got %q", got.Props["amount"])
	}
}

// OVA-MAP-W3 — activity kind selects the engagement class; duration_seconds →
// hs_call_duration ×1000 (ms); a task's due_at → hs_timestamp; occurred_at is
// read-only and never written.
func TestMapWriteActivityCallDurationW3(t *testing.T) {
	got, err := mapWrite("activity", map[string]any{
		"kind":             "call",
		"duration_seconds": float64(90),
		"occurred_at":      "2026-01-02T15:04:05Z", // read-only — must not be written
	})
	if err != nil {
		t.Fatalf("mapWrite activity call: %v", err)
	}
	if got.ObjectClass != objectClassCalls {
		t.Errorf("object class = %q, want %q", got.ObjectClass, objectClassCalls)
	}
	if got.Props["hs_call_duration"] != "90000" {
		t.Errorf("hs_call_duration = %q, want 90000 (seconds ×1000)", got.Props["hs_call_duration"])
	}
	if _, ok := got.Props[propHSTimestamp]; ok {
		t.Errorf("occurred_at is read-only and must never be written, got hs_timestamp=%q", got.Props[propHSTimestamp])
	}
}

func TestMapWriteActivityTaskDueAtW3(t *testing.T) {
	due := time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC)
	got, err := mapWrite("activity", map[string]any{
		"kind":   "task",
		"due_at": due.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("mapWrite activity task: %v", err)
	}
	if got.ObjectClass != objectClassTasks {
		t.Errorf("object class = %q, want %q", got.ObjectClass, objectClassTasks)
	}
	wantMillis := "1772614800000" // due.UnixMilli()
	if got.Props[propHSTimestamp] != wantMillis {
		t.Errorf("hs_timestamp = %q, want %q (due_at as epoch millis)", got.Props[propHSTimestamp], wantMillis)
	}
}

// OVA-MAP-W5 — lead full_name → hs_lead_name; email/company_name are
// association-derived and read-only on a lead write; and the status ↔
// hs_lead_label projection is DEFERRED (both directions) pending a pinned
// value map, matching the read side's raw-passthrough deferral (OVA-MAP-5) —
// so a raw status enum is never written untransformed into the label.
func TestMapWriteLeadPropsW5(t *testing.T) {
	got, err := mapWrite("lead", map[string]any{
		"full_name":    "Grace Hopper",
		"status":       "qualified",         // deferred — not written raw
		"email":        "grace@example.com", // read-only
		"company_name": "Navy",              // read-only
	})
	if err != nil {
		t.Fatalf("mapWrite lead: %v", err)
	}
	if got.ObjectClass != objectClassLeads {
		t.Errorf("object class = %q, want %q", got.ObjectClass, objectClassLeads)
	}
	if got.Props["hs_lead_name"] != "Grace Hopper" {
		t.Errorf("hs_lead_name = %q, want Grace Hopper", got.Props["hs_lead_name"])
	}
	if _, ok := got.Props["hs_lead_label"]; ok {
		t.Error("lead status/label projection is deferred — a raw status enum must not be written to hs_lead_label")
	}
	if _, ok := got.Props["email"]; ok {
		t.Error("lead email is association-derived and read-only — must not be written")
	}
	if _, ok := got.Props["company_name"]; ok {
		t.Error("lead company_name is association-derived and read-only — must not be written")
	}
}

// OVA-MAP-W: organization display_name→name and industry→industry project
// directly; size_band is read-only (the numberofemployees→band bucketing has
// no unambiguous inverse) and is never written.
func TestMapWriteOrganization(t *testing.T) {
	got, err := mapWrite("organization", map[string]any{
		"display_name": "Acme",
		"industry":     "Manufacturing",
		"size_band":    "51-200", // read-only — must not be written
	})
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
		t.Error("size_band is read-only (lossy inverse) — must not be written")
	}
}

// OVA-MAP-W2 at a 3-decimal, negative amount: minor units scale back to the
// decimal string by the currency exponent, sign preserved.
func TestMapWriteDealNegativeAmount(t *testing.T) {
	got, err := mapWrite("deal", map[string]any{"amount_minor": float64(-1500), "currency": "BHD"})
	if err != nil {
		t.Fatalf("mapWrite deal negative: %v", err)
	}
	if got.Props["amount"] != "-1.500" {
		t.Errorf("amount = %q, want -1.500", got.Props["amount"])
	}
}

// A numeric or boolean canonical value renders to its string form without a
// trailing ".0" (writableString's non-string branches).
func TestMapWriteRendersNonStringScalars(t *testing.T) {
	got, err := mapWrite("person", map[string]any{"first_name": float64(42), "last_name": true})
	if err != nil {
		t.Fatalf("mapWrite person scalars: %v", err)
	}
	if got.Props["firstname"] != "42" {
		t.Errorf("firstname = %q, want 42 (no trailing .0)", got.Props["firstname"])
	}
	if got.Props["lastname"] != "true" {
		t.Errorf("lastname = %q, want true", got.Props["lastname"])
	}
}

// An activity kind with no engagement class is an honest error.
func TestMapWriteActivityUnknownKind(t *testing.T) {
	if _, err := mapWrite("activity", map[string]any{"kind": "sms"}); err == nil {
		t.Error("an unknown activity kind must error, not pick an arbitrary class")
	}
}

// A present but unparseable due_at is an error, never a silently dropped
// timestamp.
func TestMapWriteActivityBadDueAt(t *testing.T) {
	if _, err := mapWrite("activity", map[string]any{"kind": "task", "due_at": "not-a-time"}); err == nil {
		t.Error("an unparseable task due_at must error")
	}
}

// An unknown canonical class is an honest error, never a silent empty write.
func TestMapWriteUnknownClass(t *testing.T) {
	if _, err := mapWrite("widget", map[string]any{"x": "y"}); err == nil {
		t.Error("mapWrite of an unknown canonical class must error, not return empty")
	}
}

// An activity write with no kind cannot choose an engagement endpoint — an
// honest error, not a guessed class.
func TestMapWriteActivityNoKind(t *testing.T) {
	if _, err := mapWrite("activity", map[string]any{"subject": "hi"}); err == nil {
		t.Error("an activity write with no kind must error (no endpoint to choose)")
	}
}
