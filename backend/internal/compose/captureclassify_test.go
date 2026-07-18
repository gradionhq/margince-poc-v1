// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The §2.8 batch-fidelity validator as a table: every requested id exactly
// once, ids verbatim, labels closed, confidence bounded — schema fidelity
// is a deterministic hard floor (§3.2), so the validator is the test
// surface, not the model.

import (
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
