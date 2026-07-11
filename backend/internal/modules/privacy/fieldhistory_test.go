// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func fhRow(actorType string, before, after map[string]any) auditDiffRow {
	return auditDiffRow{
		id:         ids.NewV7(),
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
	if entries := diffAuditRowFields(fhRow("system", nil, nil), nil, nil); len(entries) != 0 {
		t.Fatalf("tombstone (before=nil, after=nil) emitted %d entries, want 0", len(entries))
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
	if got := stringifyFieldValue(map[string]any{"a": "b"}); got == nil || *got == "" {
		t.Error("structured value must render non-empty")
	}
}
