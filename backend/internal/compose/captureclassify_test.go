// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The §2.8 batch-fidelity validator as a table: every requested id exactly
// once, ids verbatim, labels closed, confidence bounded — schema fidelity
// is a deterministic hard floor (§3.2), so the validator is the test
// surface, not the model.

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/schema"
)

// The request is the whole security perimeter of this site, and it carries
// SEVERAL senders at once: each one's bytes reach the model unedited, and the
// only thing that stops one of them ending its own span — and speaking about the
// mail below it — is a marker minted for THIS call and named in THIS call's
// system prompt. A request that fences under some other marker, or repeats a
// message outside its span, hands the instruction region to whoever wrote it.
func TestClassifyRequestFencesEveryMessageUnderTheMarkerItDeclares(t *testing.T) {
	batch := []unlabeledMessage{
		{ID: ids.NewV7(), Subject: "quote please", Body: "We need forty seats by March."},
		{ID: ids.NewV7(), Subject: "lunch thursday", Body: "Shall we say noon at the usual place?"},
	}

	req := classifyRequest(batch)

	marker, declared := promptfence.MarkerIn(req.System)
	if !declared {
		t.Fatalf("the classify system prompt declares no data boundary: %q", req.System)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("got %d messages, want the single user turn", len(req.Messages))
	}
	content := req.Messages[0].Content
	for _, m := range batch {
		openTag, closeTag := "<"+marker+` source_id="`+m.ID.String()+`">`, "</"+marker+">"
		openAt := strings.Index(content, openTag)
		if openAt < 0 {
			t.Fatalf("message %s is not opened under the declared marker keyed by its id:\n%s", m.ID, content)
		}
		closeAt := strings.Index(content[openAt:], closeTag)
		if closeAt < 0 {
			t.Fatalf("the span for message %s never closes:\n%s", m.ID, content)
		}
		span := content[openAt+len(openTag) : openAt+closeAt]
		for _, text := range []string{m.Subject, m.Body} {
			if !strings.Contains(span, text) {
				t.Errorf("message text %q never reached its own fenced span:\n%s", text, content)
			}
			// Containment is a question of counts, not membership: a prompt that
			// keeps the fence and ALSO repeats a message beside it puts that copy in
			// the instruction region while "is it inside?" stays true.
			if n := strings.Count(content, text); n != 1 {
				t.Errorf("message text %q appears %d times, want only the fenced one:\n%s", text, n, content)
			}
		}
	}
}

// A fence's scope is one call. A marker a previous sender was shown is a marker
// they can spell, so reusing one would give away the only thing they cannot
// forge.
func TestClassifyRequestMintsAFreshBoundaryPerCall(t *testing.T) {
	batch := []unlabeledMessage{{ID: ids.NewV7(), Subject: "quote please"}}

	first, declared := promptfence.MarkerIn(classifyRequest(batch).System)
	if !declared {
		t.Fatal("the classify system prompt declares no data boundary")
	}
	second, declared := promptfence.MarkerIn(classifyRequest(batch).System)
	if !declared {
		t.Fatal("the second classify system prompt declares no data boundary")
	}
	if first == second {
		t.Errorf("two classify requests share the boundary %q", first)
	}
}

func TestClassifyPayloadFidelity(t *testing.T) {
	a, b := ids.NewV7(), ids.NewV7()
	batch := []unlabeledMessage{{ID: a}, {ID: b}}
	ok := func(id ids.UUID, label string, conf float64) classifyResult {
		return classifyResult{ID: id.String(), Label: label, Confidence: schema.Confidence(conf)}
	}

	cases := map[string]struct {
		results []classifyResult
		wantErr bool
	}{
		"exact set passes": {
			results: []classifyResult{ok(a, "commitment", 0.9), ok(b, "noise", 0.8)},
		},
		"a missing id fails": {
			results: []classifyResult{ok(a, "meeting", 0.9)},
			wantErr: true,
		},
		"an unrequested id fails": {
			results: []classifyResult{ok(a, "noise", 0.9), ok(b, "noise", 0.9), ok(ids.NewV7(), "noise", 0.9)},
			wantErr: true,
		},
		"a duplicated id fails": {
			results: []classifyResult{ok(a, "noise", 0.9), ok(a, "noise", 0.9)},
			wantErr: true,
		},
		"an out-of-vocabulary label fails": {
			results: []classifyResult{ok(a, "spam", 0.9), ok(b, "noise", 0.9)},
			wantErr: true,
		},
		"an out-of-range confidence fails": {
			results: []classifyResult{ok(a, "noise", 1.2), ok(b, "noise", 0.9)},
			wantErr: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			msg := validateClassifyPayload(classifyPayload{Results: tc.results}, batch)
			if (msg != "") != tc.wantErr {
				t.Fatalf("validation = %q, wantErr=%v", msg, tc.wantErr)
			}
		})
	}
}

// Whatever the validator echoes back is MODEL output, which a sender who got the
// model to obey can choose. It reaches the operator's log AND, on a §5.2 retry,
// the next prompt — so it must be bounded at both exits.
func TestClassifyValidationMessagesDoNotEchoUnboundedModelText(t *testing.T) {
	batch := []unlabeledMessage{{ID: ids.NewV7()}}
	flood := strings.Repeat("A", 100_000)

	for name, msg := range map[string]string{
		"an unrequested id": validateClassifyPayload(classifyPayload{
			Results: []classifyResult{{ID: flood, Label: "noise", Confidence: 0.9}},
		}, batch),
		"an out-of-vocabulary label": validateClassifyPayload(classifyPayload{
			Results: []classifyResult{{ID: batch[0].ID.String(), Label: flood, Confidence: 0.9}},
		}, batch),
	} {
		t.Run(name, func(t *testing.T) {
			if msg == "" {
				t.Fatal("the payload was accepted")
			}
			if len(msg) > 500 {
				t.Fatalf("the validation message is %d bytes — model-chosen text must be clamped before it reaches a log or the next prompt", len(msg))
			}
		})
	}
}
