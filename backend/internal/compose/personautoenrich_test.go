// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// personEvent builds the envelope the consumer reads.
func personEvent(eventType string, id ids.UUID) events.Envelope {
	return events.Envelope{
		Type:   eventType,
		Entity: events.EntityRef{Type: "person", ID: id},
	}
}

// The consumer must ignore what it cannot act on WITHOUT touching the pool.
// The nil pool is the assertion: anything that reached the database here would
// panic, so a pass proves the refusal happened at the envelope.
func TestPersonAutoEnrichIgnoresWhatItCannotActOn(t *testing.T) {
	g := &PersonAutoEnrich{}
	ctx := context.Background()

	for name, env := range map[string]events.Envelope{
		"an archived person needs no match — the match requires a live row": personEvent("person.archived", ids.NewV7()),
		"an unrelated entity type":                                      {Type: "person.created", Entity: events.EntityRef{Type: "organization", ID: ids.NewV7()}},
		"an event about no entity at all":                               personEvent("person.created", ids.Nil),
		"a verb outside the set that can make a person newly matchable": personEvent("person.disqualified", ids.NewV7()),
	} {
		if err := g.HandleEvent(ctx, env); err != nil {
			t.Errorf("%s: HandleEvent returned %v, want a silent skip", name, err)
		}
	}
}

// The fill is not a human's edit and must never be recorded as one: the rows
// it writes carry this principal into captured_by, and a human id there would
// make an automatic fill indistinguishable from something a rep typed.
func TestPersonAutoEnrichWritesAsASystemPrincipal(t *testing.T) {
	g := &PersonAutoEnrich{}
	ws := ids.NewV7()
	env := events.Envelope{
		Type:        "person.created",
		Entity:      events.EntityRef{Type: "person", ID: ids.NewV7()},
		WorkspaceID: ws,
	}

	ctx := g.systemContext(context.Background(), env)
	actor, ok := principal.Actor(ctx)
	if !ok {
		t.Fatal("the pass writes with no principal at all")
	}
	if actor.Type != principal.PrincipalSystem {
		t.Errorf("actor type = %q, want a system principal", actor.Type)
	}
	if got, ok := principal.WorkspaceID(ctx); !ok || got != ws {
		t.Error("the workspace did not bind, so every write would land outside RLS's reach")
	}
}
