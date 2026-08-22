// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package persondraft_test

// What the parse accepts from a lane, and what it refuses.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/persondraft"
)

func recipientInput() persondraft.Input {
	return persondraft.Input{
		Recipient: persondraft.RecipientIn{
			ID: "019fe7ae-0000-7000-8000-000000000001", Name: "Sarah Cole",
			FirstName: "Sarah", Email: "sarah@glazedfrog.example",
		},
	}
}

// A model that wraps its JSON in a ```json fence has answered correctly, and
// this surface used to fail it while the reply surface — same models, same
// ladder — accepted it. The rule is ai.Unfence's own: one reduction defines
// what every parse sees, so no caller invents its own trim.
func TestAFencedAnswerIsRead(t *testing.T) {
	raw := "```json\n" +
		`{"subject":"Next steps","body":"Hi Sarah,\n\nShall we pick this up?"}` +
		"\n```"

	draft, err := persondraft.ParseDraft(raw, recipientInput())
	if err != nil {
		t.Fatalf("a fenced answer was refused: %v", err)
	}
	if draft.Subject != "Next steps" {
		t.Errorf("subject = %q, want the model's own", draft.Subject)
	}
}

// Unfencing must not turn nonsense into a draft: a lane that answered prose
// still fails, and the caller degrades to the floor rather than sending it.
func TestAnAnswerThatIsNotJSONIsStillRefused(t *testing.T) {
	if _, err := persondraft.ParseDraft("I could not write this one, sorry.", recipientInput()); err == nil {
		t.Fatal("prose parsed as a draft")
	}
}
