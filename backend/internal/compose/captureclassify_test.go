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
)

func TestClassifyPayloadFidelity(t *testing.T) {
	a, b := ids.NewV7(), ids.NewV7()
	batch := []unlabeledMessage{{ID: a}, {ID: b}}
	ok := func(id ids.UUID, label string, conf float64) classifyResult {
		return classifyResult{ID: id.String(), Label: label, Confidence: conf}
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
