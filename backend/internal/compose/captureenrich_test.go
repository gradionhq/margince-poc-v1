// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The §2.9 source-window rule as a table: the signature block is the
// TRAILING non-quoted lines — quoted history is never identity evidence,
// and padding never counts as content.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
)

// Everything this prompt carries is the sender's own writing — their name, their
// address, the trailing lines of their mail — and the only thing that keeps it
// out of the instruction region is a marker minted for THIS call and named in
// THIS call's system prompt. A request that fences under some other marker, or
// repeats the text beside the span, hands the instructions to whoever wrote the
// mail.
func TestSignatureEnrichRequestFencesEveryUntrustedFieldUnderTheMarkerItDeclares(t *testing.T) {
	cand := people.SignatureCandidate{
		FullName:   "Bob Person",
		Email:      "bob@acme.example",
		ActivityID: ids.NewV7(),
		Body:       "Thanks!\nBest,\nBob Person\nCTO, Acme GmbH\n+49 30 1234567",
	}
	lines := signatureBlock(cand.Body)

	req := signatureEnrichRequest(cand, lines)

	marker, declared := promptfence.MarkerIn(req.System)
	if !declared {
		t.Fatalf("the signature-enrich system prompt declares no data boundary: %q", req.System)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("got %d messages, want the single user turn", len(req.Messages))
	}
	content := req.Messages[0].Content
	openTag, closeTag := "<"+marker+">", "</"+marker+">"
	signatureTag := "<" + marker + ` source_id="` + cand.ActivityID.String() + `">`
	if !strings.Contains(content, signatureTag+lines+closeTag) {
		t.Errorf("the signature window is not opened under the declared marker keyed by its activity:\n%s", content)
	}
	if !strings.Contains(content, openTag+"Name: "+cand.FullName+"\nEmail: "+cand.Email+closeTag) {
		t.Errorf("the person's own name and address are not inside the boundary:\n%s", content)
	}
	// Containment is not a question of membership: a prompt that keeps the fence
	// and ALSO repeats the text beside it puts that copy in the instruction region
	// while "is it inside?" stays true. So the assertion is on what the prompt
	// says in its OWN voice.
	instructions := outsideEverySpan(content, marker)
	for _, text := range []string{cand.FullName, cand.Email, "CTO, Acme GmbH", "+49 30 1234567"} {
		if strings.Contains(instructions, text) {
			t.Errorf("untrusted text %q reaches the instruction region:\n%s", text, instructions)
		}
	}
}

// outsideEverySpan is the prompt with every fenced span removed — what is left
// is what the model reads as instruction. An unclosed span keeps its text, which
// is the leak it would be.
func outsideEverySpan(content, marker string) string {
	var b strings.Builder
	closeTag := "</" + marker + ">"
	for rest := content; ; {
		openAt := strings.Index(rest, "<"+marker)
		if openAt < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:openAt])
		closeAt := strings.Index(rest[openAt:], closeTag)
		if closeAt < 0 {
			b.WriteString(rest[openAt:])
			return b.String()
		}
		rest = rest[openAt+closeAt+len(closeTag):]
	}
}

// A fence's scope is one call. A marker a previous sender was shown is a marker
// they can spell, so reusing one would give away the only thing they cannot
// forge.
func TestSignatureEnrichRequestMintsAFreshBoundaryPerCall(t *testing.T) {
	cand := people.SignatureCandidate{FullName: "Bob Person", ActivityID: ids.NewV7()}

	first, declared := promptfence.MarkerIn(signatureEnrichRequest(cand, "CTO, Acme GmbH").System)
	if !declared {
		t.Fatal("the signature-enrich system prompt declares no data boundary")
	}
	second, declared := promptfence.MarkerIn(signatureEnrichRequest(cand, "CTO, Acme GmbH").System)
	if !declared {
		t.Fatal("the second signature-enrich system prompt declares no data boundary")
	}
	if first == second {
		t.Errorf("two signature-enrich requests share the boundary %q", first)
	}
}

func TestSignatureBlockWindow(t *testing.T) {
	t.Run("quoted history is excluded", func(t *testing.T) {
		body := "Thanks!\n> On Tue, Alice wrote:\n> old text\nBest,\nBob Person\nCTO, Acme GmbH\n+49 30 1234567"
		got := signatureBlock(body)
		if strings.Contains(got, "old text") {
			t.Fatalf("quoted history leaked into the window: %q", got)
		}
		if !strings.Contains(got, "CTO, Acme GmbH") {
			t.Fatalf("signature line missing from the window: %q", got)
		}
	})

	t.Run("only the trailing lines survive a long body", func(t *testing.T) {
		var b strings.Builder
		for i := range 40 {
			fmt.Fprintf(&b, "prose line %d\n", i)
		}
		b.WriteString("Jane Doe\nHead of Ops\n")
		got := signatureBlock(b.String())
		if lines := strings.Count(got, "\n") + 1; lines > signatureLineCount {
			t.Fatalf("window holds %d lines, cap is %d", lines, signatureLineCount)
		}
		if !strings.HasSuffix(got, "Head of Ops") {
			t.Fatalf("window must end at the body's tail: %q", got)
		}
	})

	t.Run("an all-quoted body yields nothing", func(t *testing.T) {
		if got := signatureBlock("> a\n> b\n"); got != "" {
			t.Fatalf("all-quoted body produced %q, want empty", got)
		}
	})
}

// The field name the shape validator rejects is MODEL output, so a sender who
// steered the model chose it. It is logged and, on a §5.2 retry, appended back
// into the prompt — bounded at both exits or it is a writing surface.
func TestSignatureShapeValidationDoesNotEchoUnboundedModelText(t *testing.T) {
	flood := strings.Repeat("A", 100_000)
	payload := fmt.Sprintf(`{"fields":[{"field":%q,"value":"x","evidence_snippet":"y"}]}`, flood)

	err := signatureShapeValid(payload)
	if err == nil {
		t.Fatal("a field outside the allowed set was accepted")
	}
	if len(err.Error()) > 500 {
		t.Fatalf("the validation error is %d bytes — model-chosen text must be clamped before it reaches a log or the next prompt", len(err.Error()))
	}
}
