// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aiactivity

// Who an occurrence belongs to is derived from the ENVELOPE, and the envelope
// does not hand the uuid over uniformly — which is the whole reason this rule
// lives in one function with tests of its own.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A human envelope carries the uuid ONLY inside Actor.ID, as "human:<uuid>":
// OnBehalfOf is nil for a person acting as themselves. Parsing it here is the
// one place that spelling is understood, and storing a real uuid is the first
// step of reconciling it rather than a fifth copy of the problem.
func TestAHumanActorIsResolvedFromTheActorID(t *testing.T) {
	user := ids.NewV7()
	scope, got, err := ResolveActor(events.Actor{Type: "human", ID: "human:" + user.String()})
	if err != nil {
		t.Fatalf("ResolveActor: %v", err)
	}
	if scope != ScopePersonal || got != user {
		t.Fatalf("scope/user = %s/%s, want personal/%s", scope, got, user)
	}
}

func TestAnAgentActorIsResolvedFromOnBehalfOf(t *testing.T) {
	user := ids.NewV7()
	scope, got, err := ResolveActor(events.Actor{
		Type: "agent", ID: "agent:" + ids.NewV7().String(), OnBehalfOf: &user,
	})
	if err != nil || scope != ScopePersonal || got != user {
		t.Fatalf("scope/user/err = %s/%s/%v, want personal/%s/nil", scope, got, err, user)
	}
}

// A connector is an agent for this purpose: the human behind it is the one
// whose work it is, and reading only Type=="agent" would drop every capture
// occurrence on the floor the moment slice 4 emits one.
func TestAConnectorActorIsResolvedFromOnBehalfOfToo(t *testing.T) {
	user := ids.NewV7()
	scope, got, err := ResolveActor(events.Actor{
		Type: "connector", ID: "connector:gmail", OnBehalfOf: &user,
	})
	if err != nil || scope != ScopePersonal || got != user {
		t.Fatalf("scope/user/err = %s/%s/%v, want personal/%s/nil", scope, got, err, user)
	}
}

// A system actor with nobody behind it is workspace-scoped work: it belongs to
// nobody, and a personal read never matches it.
func TestASystemActorWithNoHumanIsWorkspaceScoped(t *testing.T) {
	scope, got, err := ResolveActor(events.Actor{Type: "system", ID: "system:relay"})
	if err != nil || scope != ScopeWorkspace || !got.IsZero() {
		t.Fatalf("scope/user/err = %s/%s/%v, want workspace/zero/nil", scope, got, err)
	}
}

// An unparseable human actor is REFUSED, never quietly downgraded. Silently
// making it workspace-scoped is how one person's work becomes a system sweep
// that nobody can find and nobody notices is missing.
func TestAnUnparseableHumanActorIsRefused(t *testing.T) {
	for _, id := range []string{"human:not-a-uuid", "human:", "", ids.NewV7().String()} {
		if _, _, err := ResolveActor(events.Actor{Type: "human", ID: id}); err == nil {
			t.Fatalf("actor id %q: expected a refusal, not a silent workspace-scoped occurrence", id)
		}
	}
}

// A human envelope that ALSO names somebody else is refused rather than
// resolved either way. It is not a shape any writer produces, and guessing
// which half is the truth is how one person's work is filed under another's.
func TestAHumanActorActingOnBehalfOfSomebodyElseIsRefused(t *testing.T) {
	self, other := ids.NewV7(), ids.NewV7()
	if _, _, err := ResolveActor(events.Actor{
		Type: "human", ID: "human:" + self.String(), OnBehalfOf: &other,
	}); err == nil {
		t.Fatal("a human acting on behalf of a different human is not a shape this projection can attribute")
	}
}
