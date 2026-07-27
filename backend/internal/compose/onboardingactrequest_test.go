// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the non-company acts' request build owes: the server's context block
// stays data, the conversation reaches the model in the order it happened, and
// the boundary that separates the two is minted for this call alone.

import (
	"encoding/json"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func actContextFixture(t *testing.T) json.RawMessage {
	t.Helper()
	block, err := onboardingActContext(string(crmcontracts.OnboardingActVoice),
		onboardingVoiceContext{CorpusTotalWords: 1240, SourceCount: 3}, true, onboardingResearchState{}, nil)
	if err != nil {
		t.Fatalf("assembling the act context: %v", err)
	}
	return block
}

// The context block is application state, and the act prompt's own rule is that
// instructions inside it are never obeyed. That rule can only be stated about a
// region the model can tell apart, so the block belongs inside a span whose
// marker this request's system prompt declares — and nowhere else.
func TestOnboardingActRequestFencesTheContextUnderTheMarkerItDeclares(t *testing.T) {
	block := actContextFixture(t)

	req := onboardingActRequest(string(crmcontracts.OnboardingActVoice), "How is my corpus doing?", nil, block, "en")

	marker, declared := promptfence.MarkerIn(req.System)
	if !declared {
		t.Fatalf("the act system prompt declares no data boundary: %q", req.System)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("got %d messages, want the context turn and the administrator's own", len(req.Messages))
	}
	// Containment is not a question of membership: a prompt that keeps the fence
	// and ALSO repeats the block beside it puts that copy in the instruction
	// region while "is it inside?" stays true. So the assertion is on what the
	// prompt says in its OWN voice.
	instructions := outsideEverySpan(req.Messages[0].Content, marker)
	if strings.Contains(instructions, "1240") {
		t.Errorf("the context block reaches the instruction region:\n%s", instructions)
	}
	if !strings.Contains(req.System, "Respond in en.") {
		t.Errorf("the act prompt does not name the locale it was built for: %q", req.System)
	}
}

// The model is stateless, so a follow-up question only resolves against turns
// this request carries. They sit between the context block and the current
// message because that is the order they happened in, and a reply to "and the
// other one?" is about whichever turn precedes it.
func TestOnboardingActRequestReplaysTheConversationInOrder(t *testing.T) {
	history := []model.Message{
		{Role: chatRoleUser, Content: "What does connecting my inbox do?"},
		{Role: "assistant", Content: "It reads nothing without a per-purpose grant."},
	}

	req := onboardingActRequest(string(crmcontracts.OnboardingActConnect), "And what does it not do?",
		history, actContextFixture(t), "en")

	if len(req.Messages) != len(history)+2 {
		t.Fatalf("got %d messages, want the context turn, %d replayed turns and the administrator's own",
			len(req.Messages), len(history))
	}
	for i, turn := range history {
		replayed := req.Messages[i+1]
		if replayed.Role != turn.Role || replayed.Content != turn.Content {
			t.Errorf("replayed turn %d = %+v, want %+v", i+1, replayed, turn)
		}
	}
	current := req.Messages[len(req.Messages)-1]
	if current.Role != chatRoleUser || current.Content != "And what does it not do?" {
		t.Errorf("the administrator's own message is not the last turn: %+v", current)
	}
}

// A fence's scope is one call. A marker a previous turn was shown is a marker
// whoever wrote that turn can spell, so reusing one would give away the only
// thing they cannot forge.
func TestOnboardingActRequestMintsAFreshMarkerPerCall(t *testing.T) {
	block := actContextFixture(t)
	act := string(crmcontracts.OnboardingActResults)

	first, declared := promptfence.MarkerIn(onboardingActRequest(act, "Where do I stand?", nil, block, "en").System)
	if !declared {
		t.Fatal("the first act system prompt declares no data boundary")
	}
	second, declared := promptfence.MarkerIn(onboardingActRequest(act, "Where do I stand?", nil, block, "en").System)
	if !declared {
		t.Fatal("the second act system prompt declares no data boundary")
	}
	if first == second {
		t.Errorf("two act requests share the boundary %q", first)
	}
}
