// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// A briefing states a date from the RECORD, not from what a note recalls.
//
// The retriever always knew when each touch happened — occurred_at orders the
// timeline — but the answer dropped it, so the only dates a reader could see
// were the ones written inside note prose. A loss post-mortem written months
// later said "im Oktober" for an email dated 2025-09-13, and a briefing built
// on the prose alone repeats that to the customer. The date now rides the item.
func TestAnAssembledContextCarriesWhenEachThingHappened(t *testing.T) {
	when := time.Date(2025, 9, 13, 9, 30, 0, 0, time.UTC)
	activity, person := ids.NewV7(), ids.NewV7()
	got := assembledContext(context.Background(), retrieval.Context{
		Anchor: datasource.EntityRef{Type: datasource.EntityOrganization, ID: ids.NewV7()},
		Sections: []retrieval.Section{{Name: "recent", Items: []retrieval.Item{
			{
				Ref:     datasource.EntityRef{Type: datasource.EntityActivity, ID: activity},
				Summary: "Zu viele Ansprechpartner", OccurredAt: when,
			},
			// A person is not an event and carries no date; a zero one would
			// render as 0001-01-01 rather than as absent.
			{Ref: datasource.EntityRef{Type: datasource.EntityPerson, ID: person}, Summary: "Andrea"},
		}}},
	})
	if len(got.Sections) != 1 || len(got.Sections[0].Items) != 2 {
		t.Fatalf("assembled %+v, want one section of two items", got.Sections)
	}
	event, contact := got.Sections[0].Items[0], got.Sections[0].Items[1]
	if event.OccurredAt == nil || !event.OccurredAt.Equal(when) {
		t.Errorf("the event carries occurred_at %v, want %v — without it a reader can only "+
			"take dates from the prose", event.OccurredAt, when)
	}
	if contact.OccurredAt != nil {
		t.Errorf("a person carries occurred_at %v, want it absent", contact.OccurredAt)
	}
}
