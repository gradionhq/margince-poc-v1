// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func fhRow(actorType string, before, after map[string]any) auditDiffRow {
	return auditDiffRow{
		id:         ids.NewV7(),
		action:     "update",
		entityType: "person",
		entityID:   ids.NewV7(),
		actorType:  actorType,
		actorID:    "user-1",
		occurredAt: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
		before:     before,
		after:      after,
	}
}

func TestDiffEmitsOneEntryPerChangedFieldAlphabetically(t *testing.T) {
	row := fhRow("human",
		map[string]any{"gamma": "g1", "alpha": "a1", "beta": "b1", "same": "x"},
		map[string]any{"gamma": "g2", "alpha": "a2", "beta": "b2", "same": "x"})
	entries := diffAuditRowFields(row, nil, nil)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3 (unchanged key must not emit)", len(entries))
	}
	for i, want := range []string{"alpha", "beta", "gamma"} {
		if entries[i].Field != want {
			t.Errorf("entries[%d].Field = %q, want %q (alphabetical emission)", i, entries[i].Field, want)
		}
		if entries[i].ID != row.id || !entries[i].ChangedAt.Equal(row.occurredAt) {
			t.Errorf("entries[%d] must carry the source row's id and occurred_at", i)
		}
	}
	if *entries[0].OldValue != "a1" || *entries[0].NewValue != "a2" {
		t.Errorf("alpha diff = %v -> %v, want a1 -> a2", *entries[0].OldValue, *entries[0].NewValue)
	}
}

func TestDiffCreateRowEmitsNilOldValues(t *testing.T) {
	row := fhRow("human", nil, map[string]any{"name": "Acme", "industry": "Tech"})
	entries := diffAuditRowFields(row, nil, nil)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if e.OldValue != nil {
			t.Errorf("create row: OldValue = %v, want nil", *e.OldValue)
		}
		if e.NewValue == nil {
			t.Error("create row: NewValue must be set")
		}
	}
}

func TestDiffRemovedFieldEmitsNilNewValue(t *testing.T) {
	row := fhRow("human", map[string]any{"nickname": "Ace"}, map[string]any{})
	entries := diffAuditRowFields(row, nil, nil)
	if len(entries) != 1 || entries[0].NewValue != nil || *entries[0].OldValue != "Ace" {
		t.Fatalf("removal: got %+v, want one entry Ace -> nil", entries)
	}
}

func TestDiffErasureTombstoneEmitsNothing(t *testing.T) {
	// The real tombstone (erasure.go) carries nil images with its
	// suppression tallies in evidence; this pins the belt-and-braces
	// guard for the mis-written shape — a tombstone whose tallies landed
	// in after must STILL project nothing, on the action alone.
	row := fhRow("system", nil, map[string]any{
		"reason": "dsr", "emails_suppressed": 2.0, "raw_rows_purged": 1.0, "activities_redacted": 3.0,
	})
	row.action = "erase"
	if entries := diffAuditRowFields(row, nil, nil); len(entries) != 0 {
		t.Fatalf("erase tombstone emitted %d entries, want 0 — its payload is proof, not fields", len(entries))
	}
}

func TestDiffMetaVerbPayloadsNeverProjectAsFields(t *testing.T) {
	// Evidence-carrying verbs (merge relink counts, promote provenance,
	// export receipts) must emit nothing even though their payloads are
	// non-empty maps — projecting them would fabricate a timeline.
	for _, action := range []string{"merge", "promote", "export", "assign", "anonymize"} {
		row := fhRow("human",
			map[string]any{"merged_into_id": nil},
			map[string]any{"merged_into_id": "p-2", "relinked": map[string]any{"activities": 3.0}})
		row.action = action
		if entries := diffAuditRowFields(row, nil, nil); len(entries) != 0 {
			t.Errorf("%s row emitted %d entries, want 0", action, len(entries))
		}
	}
}

func TestDiffMaskedFieldIsTotallyWithheld(t *testing.T) {
	row := fhRow("human",
		map[string]any{"name": "Old", "ssn": "111"},
		map[string]any{"name": "New", "ssn": "222"})
	mask := entityFieldMask{"ssn": {}}
	entries := diffAuditRowFields(row, mask, nil)
	if len(entries) != 1 || entries[0].Field != "name" {
		t.Fatalf("masked diff = %+v, want only the name entry", entries)
	}
}

func TestDiffFieldFilterNarrowsToOneField(t *testing.T) {
	row := fhRow("human",
		map[string]any{"a": "1", "b": "1"},
		map[string]any{"a": "2", "b": "2"})
	f := "b"
	entries := diffAuditRowFields(row, nil, &f)
	if len(entries) != 1 || entries[0].Field != "b" {
		t.Fatalf("field filter = %+v, want only b", entries)
	}
}

func TestDiffAgentRowsCarryPassportAndEvidence(t *testing.T) {
	pid := ids.NewV7()
	row := fhRow("agent", map[string]any{"stage": "s1"}, map[string]any{"stage": "s2"})
	row.passportID = &pid
	row.evidence = map[string]any{"source": "email-123"}
	entries := diffAuditRowFields(row, nil, nil)
	if len(entries) != 1 || entries[0].PassportID == nil || *entries[0].PassportID != pid {
		t.Fatalf("agent entry must carry the passport id: %+v", entries)
	}
	if entries[0].Evidence == nil || entries[0].Evidence["source"] != "email-123" {
		t.Errorf("agent entry must carry evidence: %+v", entries[0].Evidence)
	}
}

func TestDiffNonAgentRowsNeverCarryAgentAttribution(t *testing.T) {
	pid := ids.NewV7()
	for _, actor := range []string{"human", "system", "connector"} {
		row := fhRow(actor, map[string]any{"stage": "s1"}, map[string]any{"stage": "s2"})
		row.passportID = &pid
		row.evidence = map[string]any{"source": "x"}
		entries := diffAuditRowFields(row, nil, nil)
		if entries[0].PassportID != nil || entries[0].Evidence != nil {
			t.Errorf("%s row leaked passport/evidence onto the entry", actor)
		}
	}
}

func TestDiffDeepEqualStructuredValuesEmitNothing(t *testing.T) {
	row := fhRow("human",
		map[string]any{"meta": map[string]any{"k": []any{1.0, 2.0}}},
		map[string]any{"meta": map[string]any{"k": []any{1.0, 2.0}}})
	if entries := diffAuditRowFields(row, nil, nil); len(entries) != 0 {
		t.Fatalf("structurally equal values emitted %d entries, want 0", len(entries))
	}
}

func TestStringifyRendersValuesAndNeverLiteralNil(t *testing.T) {
	if got := stringifyFieldValue(float64(42)); got == nil || *got != "42" {
		t.Errorf("float64(42) = %v, want 42", got)
	}
	if got := stringifyFieldValue(nil); got != nil {
		t.Errorf("nil value = %q, want nil pointer (never a literal nil string)", *got)
	}
	if got := stringifyFieldValue("Managed Hosting"); got == nil || *got != "Managed Hosting" {
		t.Errorf("string value = %v, want it unchanged", got)
	}
}

// Every jsonb number decodes to float64, and Go's default verb formats one
// with %g — so a revenue of 50000000 reached the screen as "5e+07". A
// salesperson reading what changed on a company gets the number.
func TestStringifyLargeNumbersNeverRenderInScientificNotation(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{50000000, "50000000"},
		{250000000, "250000000"},
		{1234.5, "1234.5"},
		{0, "0"},
		{-7500000, "-7500000"},
	} {
		got := stringifyFieldValue(tc.in)
		if got == nil || *got != tc.want {
			t.Errorf("stringifyFieldValue(%v) = %v, want %q", tc.in, got, tc.want)
		}
	}
}

// A structured value is the server's own jsonb, and the reader is looking at
// what changed on their account. Go's default formatting prints its internal
// map syntax, which reached the screen as `map[logo:https://…]`.
func TestStringifyStructuredValuesRenderAsJSONNotGoSyntax(t *testing.T) {
	got := stringifyFieldValue(map[string]any{"logo": "https://scale.sc/icon.png"})
	if got == nil {
		t.Fatal("structured value rendered as nil, want JSON")
	}
	if strings.HasPrefix(*got, "map[") {
		t.Errorf("value = %q, want JSON rather than Go map syntax", *got)
	}
	if *got != `{"logo":"https://scale.sc/icon.png"}` {
		t.Errorf("value = %q, want compact JSON", *got)
	}

	list := stringifyFieldValue([]any{"a", "b"})
	if list == nil || *list != `["a","b"]` {
		t.Errorf("array value = %v, want compact JSON", list)
	}
}

// A site-read confirmation audits the pipeline's own state under the
// organization: which draft it applied, where it read, and the entire applied
// payload. None of those is a field of the record — nobody can see one as a
// live value — so "what changed on this record" must not recite them.
func TestDiffWithholdsTheWritingPipelinesOwnBookkeeping(t *testing.T) {
	row := fhRow("agent", nil, map[string]any{
		"industry":      "Manufacturing",
		"source":        "site_read",
		"source_url":    "https://acme.example",
		"fields":        map[string]any{"logo": "https://acme.example/logo.png"},
		"human_fields":  map[string]any{},
		"facts":         []any{"one", "two"},
		"site_read_id":  "019fbc88-0000-7000-8000-000000000000",
		"draft_version": float64(3),
	})
	entries := diffAuditRowFields(row, nil, nil)
	if len(entries) != 1 || entries[0].Field != "industry" {
		got := make([]string, 0, len(entries))
		for _, entry := range entries {
			got = append(got, entry.Field)
		}
		t.Fatalf("fields = %v, want the record's own field alone", got)
	}
}

// A create names every column it wrote, nulls included. Projecting those as
// changes told the reader a field had been created and then cleared, about a
// field nobody ever filled.
func TestDiffSaysNothingAboutAFieldThatWasNeverFilled(t *testing.T) {
	row := fhRow("human", nil, map[string]any{
		"full_name": "Dana Buyer",
		"email":     nil,
		"company":   nil,
	})
	// A CREATE row, which is where this happens: the write names every column
	// it filled and every one it did not.
	row.action = "create"
	entries := diffAuditRowFields(row, nil, nil)
	if len(entries) != 1 || entries[0].Field != "full_name" {
		got := make([]string, 0, len(entries))
		for _, entry := range entries {
			got = append(got, entry.Field)
		}
		t.Fatalf("fields = %v, want the one field the create actually filled", got)
	}
}

// jsonb numbers decoded as float64 lose the low bits past 2^53, so a stored
// id or amount came back as a DIFFERENT number than the record holds.
func TestDiffRendersALargeNumberExactly(t *testing.T) {
	row := fhRow("human",
		map[string]any{"external_id": json.Number("9007199254740993")},
		map[string]any{"external_id": json.Number("9007199254740995")},
	)
	entries := diffAuditRowFields(row, nil, nil)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one", entries)
	}
	if got := *entries[0].NewValue; got != "9007199254740995" {
		t.Errorf("new value = %s, want the stored number unchanged", got)
	}
}
