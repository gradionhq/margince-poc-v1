// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

import (
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// FieldHistoryFilter carries the validated query surface of
// (GET /field-history). EntityType and EntityID are required; the rest
// narrow the projection.
type FieldHistoryFilter struct {
	EntityType string
	EntityID   ids.UUID
	Field      *string
	ActorType  *string
	Cursor     *string
	Limit      *int
}

// FieldHistoryEntry is one per-field change projected from a single
// audit_log row's before/after diff — not a stored history row. ID is
// the source audit row's id, so entries from one mutation share it.
type FieldHistoryEntry struct {
	ID         ids.UUID
	EntityType string
	EntityID   ids.UUID
	Field      string
	OldValue   *string
	NewValue   *string
	ChangedAt  time.Time
	ActorType  string
	ActorID    string
	PassportID *ids.UUID
	Evidence   map[string]any
}

// FieldHistoryPage is one keyset window of the timeline, newest first.
type FieldHistoryPage struct {
	Entries    []FieldHistoryEntry
	NextCursor string
	HasMore    bool
}

// The record kinds whose field history is readable — the audit spine's
// entity_type is free text, so the surface pins the vocabulary.
var fieldHistoryEntityTypes = map[string]bool{
	"person": true, "organization": true, "deal": true, "lead": true, "activity": true,
}

var fieldHistoryActorTypes = map[string]bool{
	"human": true, "agent": true, "system": true, "connector": true,
}

// entityFieldMask names fields whose history is withheld for an entity
// type, exactly as the live value would be withheld — hiding history and
// value is one motion, never two mechanisms. Empty until field-level
// masking ships; the transform applies it to both sides before diffing
// so a masked field can never leak through an old_value.
type entityFieldMask map[string]struct{}

var defaultFieldMasks = map[string]entityFieldMask{}

// auditDiffRow carries the columns of one audit_log row the diff needs.
type auditDiffRow struct {
	id         ids.UUID
	entityType string
	entityID   ids.UUID
	actorType  string
	actorID    string
	passportID *ids.UUID
	evidence   map[string]any
	occurredAt time.Time
	before     map[string]any
	after      map[string]any
}

// diffAuditRowFields projects one audit row into per-field entries:
// changed or added keys emit old->new, removed keys emit old->nil, and
// keys equal on both sides emit nothing — an empty history is honest,
// never fabricated. Keys emit alphabetically so a row's entries are
// deterministic. passport/evidence surface only for agent actors.
func diffAuditRowFields(row auditDiffRow, mask entityFieldMask, fieldFilter *string) []FieldHistoryEntry {
	before := applyFieldMask(row.before, mask)
	after := applyFieldMask(row.after, mask)

	keyset := make(map[string]struct{}, len(before)+len(after))
	for k := range before {
		keyset[k] = struct{}{}
	}
	for k := range after {
		keyset[k] = struct{}{}
	}
	keys := make([]string, 0, len(keyset))
	for k := range keyset {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var entries []FieldHistoryEntry
	for _, key := range keys {
		if fieldFilter != nil && key != *fieldFilter {
			continue
		}
		beforeVal, inBefore := before[key]
		afterVal, inAfter := after[key]
		switch {
		case inAfter && (!inBefore || !reflect.DeepEqual(beforeVal, afterVal)):
			entries = append(entries, makeFieldHistoryEntry(row, key, stringifyFieldValue(beforeVal), stringifyFieldValue(afterVal)))
		case inBefore && !inAfter:
			entries = append(entries, makeFieldHistoryEntry(row, key, stringifyFieldValue(beforeVal), nil))
		}
	}
	return entries
}

func makeFieldHistoryEntry(row auditDiffRow, field string, oldValue, newValue *string) FieldHistoryEntry {
	var passportID *ids.UUID
	var evidence map[string]any
	if row.actorType == "agent" {
		passportID = row.passportID
		evidence = row.evidence
	}
	return FieldHistoryEntry{
		ID:         row.id,
		EntityType: row.entityType,
		EntityID:   row.entityID,
		Field:      field,
		OldValue:   oldValue,
		NewValue:   newValue,
		ChangedAt:  row.occurredAt,
		ActorType:  row.actorType,
		ActorID:    row.actorID,
		PassportID: passportID,
		Evidence:   evidence,
	}
}

// stringifyFieldValue renders a diff side for display. A nil (JSON null
// or absent) value stays a nil pointer — the client renders the
// empty/created origin label, never a literal "nil".
func stringifyFieldValue(v any) *string {
	if v == nil {
		return nil
	}
	s := fmt.Sprintf("%v", v)
	return &s
}

func applyFieldMask(data map[string]any, mask entityFieldMask) map[string]any {
	if data == nil || len(mask) == 0 {
		return data
	}
	out := make(map[string]any, len(data))
	for k, v := range data {
		if _, hidden := mask[k]; !hidden {
			out[k] = v
		}
	}
	return out
}
