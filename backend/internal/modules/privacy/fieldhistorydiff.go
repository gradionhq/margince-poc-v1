// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Projecting ONE audit_log row into per-field changes: the mask applied to
// both images, the diff itself, and how a stored value is rendered for a
// reader. The reads that find the rows, page them and stop at an erasure
// tombstone live in fieldhistory.go — this file never touches the database.

// entityFieldMask names fields whose history is withheld for an entity
// type, exactly as the live value would be withheld — hiding history and
// value is one motion, never two mechanisms. Empty until field-level
// masking ships; the transform applies it to both sides before diffing
// so a masked field can never leak through an old_value.
type entityFieldMask map[string]struct{}

var defaultFieldMasks = map[string]entityFieldMask{}

// writerBookkeepingKeys names keys an audit image carries that are not fields
// OF the record: the writing pipeline's own state. A site-read confirmation
// audits which draft it applied (`site_read_id`, `draft_version`), where it
// read (`source`, `source_url`) and the whole applied payload (`fields`,
// `human_fields`, `facts`) — none of which is a column anyone can see as a
// live value, and one of which is a fact array thousands of characters long.
//
// Field history answers "what changed on this record", so it withholds them.
// The audit spine (recordHistoryEntry) keeps them: an auditor asking which
// pipeline run wrote a row is asking exactly this.
var writerBookkeepingKeys = entityFieldMask{
	"source":        {},
	"source_url":    {},
	"source_ref":    {},
	"fields":        {},
	"human_fields":  {},
	"facts":         {},
	"site_read_id":  {},
	"draft_version": {},
	"anchor":        {},
	"captured_by":   {},
}

// auditDiffRow carries the columns of one audit_log row the diff needs.
type auditDiffRow struct {
	id         ids.UUID
	action     string
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
	// A meta verb's payload is evidence, not a field image (see
	// fieldHistoryProjectedActions). The SQL scan already excludes such
	// rows; this guard keeps the invariant with the diff itself, so no
	// caller can ever project an erase tombstone's tallies or a merge's
	// relink counts as field changes.
	if !fieldHistoryProjectedActions[row.action] {
		return nil
	}
	before := applyFieldMask(applyFieldMask(row.before, mask), writerBookkeepingKeys)
	after := applyFieldMask(applyFieldMask(row.after, mask), writerBookkeepingKeys)

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
		// A create names every column it wrote, including the ones it wrote as
		// null. Both sides then render as absent, and the row reads "created →
		// cleared" about a field nobody ever filled — a lead captured without
		// an email produced one of those per empty column.
		if beforeVal == nil && afterVal == nil {
			continue
		}
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
//
// A scalar renders as itself. Anything structured — a jsonb object or array
// the audit row carried — renders as JSON, because Go's default formatting
// prints its own map syntax (`map[logo:https://…]`), and a reader looking at
// what changed on their account should never be shown the server's internal
// spelling of it.
//
//craft:ignore naked-any the argument IS an arbitrary decoded JSON value — one key of an audit row's jsonb image — and no narrower Go type describes it
func stringifyFieldValue(v any) *string {
	if v == nil {
		return nil
	}
	switch typed := v.(type) {
	case string:
		return &typed
	case float64:
		// Every jsonb number arrives here as a float64, and Go's default
		// verb formats one with %g — so an annual revenue of 50000000 reached
		// the screen as `5e+07`. FormatFloat with -1 precision writes the
		// shortest decimal that round-trips, and never an exponent.
		s := strconv.FormatFloat(typed, 'f', -1, 64)
		return &s
	case bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		json.Number:
		s := fmt.Sprintf("%v", typed)
		return &s
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		// Every value here came out of json.Unmarshal on the audit row, so it
		// re-encodes by construction; this branch exists for the shape of the
		// API, not for a case the data can reach. It answers with the same nil
		// an absent value gets — the client already renders that as an honest
		// "not recorded" marker in the reader's own language. A sentence
		// written here instead would be English on a German screen.
		return nil
	}
	s := string(encoded)
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
