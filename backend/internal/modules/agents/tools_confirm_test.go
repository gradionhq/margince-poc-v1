// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What a human reads when asked to approve a 🟡 call.

import (
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// The label a staged call shows the approver, per record type.
//
// `kind` is a weak label — it names a CLASS, not an instance — and it is only ever
// the best answer for an edge, which has no name at all. An activity carries a
// `kind` too, and there the id is what a human needs: "Update activity" plus a
// class name says nothing about WHICH note is being overwritten.
func TestRecordLabelUsesKindOnlyForAnEdge(t *testing.T) {
	edgeID, activityID := ids.NewV7(), ids.NewV7()
	for name, tc := range map[string]struct {
		entity datasource.EntityType
		id     ids.UUID
		fields string
		want   string
	}{
		"an edge has no name, so its kind is the label": {
			datasource.EntityRelationship, edgeID, `{"kind":"employment"}`, `"employment"`,
		},
		"an activity's kind is NOT its label — the id identifies the row": {
			datasource.EntityActivity, activityID, `{"kind":"note","body":"..."}`, activityID.String(),
		},
		"a named record still wins over everything": {
			datasource.EntityDeal, ids.NewV7(), `{"name":"Acme renewal","kind":"whatever"}`, `"Acme renewal"`,
		},
		"nothing to read falls back to the id": {
			datasource.EntityPerson, edgeID, `{}`, edgeID.String(),
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := recordLabel(datasource.Record{
				Ref: datasource.EntityRef{Type: tc.entity, ID: tc.id}, Fields: json.RawMessage(tc.fields),
			})
			if got != tc.want {
				t.Errorf("recordLabel = %s, want %s", got, tc.want)
			}
		})
	}
}
