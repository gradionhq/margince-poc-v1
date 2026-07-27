// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The inbox row is what a triaging approver acts on, and for the whole REST
// admission surface it used to say only which verb hit which path. These
// cases pin what a human can now see without opening the envelope: the
// operation, and the values the call would actually write.

import (
	"strings"
	"testing"
)

func TestRestSummaryNamesTheValuesTheCallWouldWrite(t *testing.T) {
	got := restSummary("updateDeal", "PATCH", "/v1/deals/018f2a10-0000-7000-8000-00000000000a",
		[]byte(`{"amount_minor":100,"currency":"EUR","expected_close_date":"2027-06-30"}`))

	for _, want := range []string{
		"updateDeal", "PATCH", "/v1/deals/018f2a10-0000-7000-8000-00000000000a",
		"amount_minor=100", "currency=EUR", "expected_close_date=2027-06-30",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q does not carry %q — an approver cannot see what they are approving", got, want)
		}
	}
}

// A body-less action route (send this offer, archive this record) has no
// fields to name, and the operation IS the whole change.
func TestRestSummaryOfABodylessActionNamesTheOperation(t *testing.T) {
	got := restSummary("sendOffer", "POST", "/v1/offers/018f2a10-0000-7000-8000-00000000beef/send", nil)
	if !strings.Contains(got, "sendOffer") || !strings.Contains(got, "/send") {
		t.Errorf("summary %q does not name the operation", got)
	}
	if strings.Contains(got, ":") && strings.HasSuffix(got, ":") {
		t.Errorf("summary %q trails an empty field list", got)
	}
}

// A wide patch is summarized, not dumped: the row stays readable and
// proposed_change still carries the whole envelope.
func TestRestSummaryBoundsAWidePatch(t *testing.T) {
	var b strings.Builder
	b.WriteString("{")
	for i := range 30 {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"field`)
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(string(rune('0' + i/26)))
		b.WriteString(`":1`)
	}
	b.WriteString("}")

	got := restSummary("updatePerson", "PATCH", "/v1/people/x", []byte(b.String()))
	if !strings.Contains(got, "more") {
		t.Errorf("a 30-field patch was not bounded: %q", got)
	}
	if strings.Count(got, "=") > summaryFieldLimit {
		t.Errorf("summary enumerates %d fields, want at most %d", strings.Count(got, "="), summaryFieldLimit)
	}
}

// A single long value cannot crowd out the fields after it.
func TestRestSummaryBoundsOneLongValue(t *testing.T) {
	got := restSummary("updatePerson", "PATCH", "/v1/people/x",
		[]byte(`{"notes":"`+strings.Repeat("a", 500)+`","title":"CEO"}`))
	if !strings.Contains(got, "title=CEO") {
		t.Errorf("a long value crowded out the field after it: %q", got)
	}
	if len(got) > 400 {
		t.Errorf("summary is %d bytes; one value should not dominate it", len(got))
	}
}

// Nested structure is named and counted rather than expanded — the summary
// answers "what shape", the envelope answers "what exactly".
func TestRestSummaryCountsNestedStructure(t *testing.T) {
	got := restSummary("createOffer", "POST", "/v1/deals/x/offers",
		[]byte(`{"currency":"EUR","line_items":[{"description":"Pilot"},{"description":"Support"}]}`))
	if !strings.Contains(got, "line_items=[2]") {
		t.Errorf("summary %q does not count the nested line items", got)
	}
}

// Clearing a field and setting it empty are different changes, and the
// approving human has to be able to tell them apart. Unmarshaling JSON null
// into a plain string succeeds and leaves it empty, so a naive string probe
// renders `owner_id=` for both — which reads like nothing is happening on the
// one that hands the record to nobody.
func TestRestSummaryDistinguishesNullFromEmpty(t *testing.T) {
	cleared := restSummary("updateDeal", "PATCH", "/v1/deals/x", []byte(`{"owner_id":null}`))
	if !strings.Contains(cleared, "owner_id=null") {
		t.Errorf("summary %q does not show that owner_id is being CLEARED", cleared)
	}
	emptied := restSummary("updateDeal", "PATCH", "/v1/deals/x", []byte(`{"owner_id":""}`))
	if strings.Contains(emptied, "owner_id=null") {
		t.Errorf("summary %q reports an empty string as a clear", emptied)
	}
	if cleared == emptied {
		t.Error("clearing a field and emptying it render identically")
	}
}
