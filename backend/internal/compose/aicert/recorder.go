// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

import (
	"context"
	"fmt"
	"sync"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// traceRecorder is the runner's ai.CallRecorder: the cert lane has no
// Postgres to trace ai_call rows into, so this stands in exactly where
// ai.NewCallMeter would in production. tracing.go's flush calls Record
// once per LOGICAL call — one batch carrying every rung that call
// walked, with exactly one attempt in the batch marked IsTerminal — and
// it does so synchronously before Router.Complete returns, so the
// runner can always read this call's own outcome back immediately via
// lastTerminal rather than re-deriving it from Router.Complete's
// return values. Batches (not a flattened attempt list) is what makes
// "this run's own terminal attempt" a one-line lookup instead of a scan
// keyed by LogicalCallID.
type traceRecorder struct {
	mu      sync.Mutex
	batches [][]ai.Call
}

func newTraceRecorder() *traceRecorder {
	return &traceRecorder{}
}

// Record satisfies ai.CallRecorder: it never fails, and never drops a
// batch — a cert run that silently lost a trace would score a run it
// can no longer explain.
func (r *traceRecorder) Record(_ context.Context, attempts []ai.Call) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, append([]ai.Call(nil), attempts...))
	return nil
}

// EnsureConfig is a no-op: the cert lane has no ai_call_config dimension
// table to plant a row in, and Router.flush already tolerates a
// CallRecorder that reports nothing back (best-effort enrichment).
func (r *traceRecorder) EnsureConfig(context.Context, ai.ConfigSnapshot) error {
	return nil
}

// lastTerminal returns the terminal attempt of the most recently
// recorded logical call. Ok is false only when Record was never called
// at all, or a batch it received carried no terminal row — both
// programmer-bug conditions in this package (every real Router.Complete
// flush marks exactly one attempt terminal), never a caller input.
func (r *traceRecorder) lastTerminal() (ai.Call, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.batches) == 0 {
		return ai.Call{}, false
	}
	return terminalOf(r.batches[len(r.batches)-1])
}

// mark names where this recorder's history stands right now, for a caller
// about to drive something and then read back everything IT recorded.
//
// A run is not one logical call: a site may retry, fall back, or drive a whole
// tool loop, and each of those is its own batch here. Reading only the last one
// would account a three-call run by its third call — the degrade the first one
// hit, the tokens it spent, and the prompt it sent all silently dropped.
func (r *traceRecorder) mark() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.batches)
}

// terminalsSince returns the terminal attempt of every logical call recorded
// after mark, in the order the calls were made.
//
// A batch with no terminal row, or a mark from another recorder, is a
// programmer bug in this package rather than a certification input — but it is
// reported rather than skipped, because a silently dropped call is exactly the
// accounting error this method exists to remove.
func (r *traceRecorder) terminalsSince(mark int) ([]ai.Call, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if mark < 0 || mark > len(r.batches) {
		return nil, fmt.Errorf("aicert: trace recorder: mark %d is outside the %d calls recorded", mark, len(r.batches))
	}
	since := r.batches[mark:]
	terminals := make([]ai.Call, 0, len(since))
	for i, batch := range since {
		term, ok := terminalOf(batch)
		if !ok {
			return nil, fmt.Errorf("aicert: trace recorder: call %d of the %d just recorded carries no terminal attempt", i+1, len(since))
		}
		terminals = append(terminals, term)
	}
	return terminals, nil
}

// terminalOf picks the one attempt of a logical call that actually served it.
func terminalOf(batch []ai.Call) (ai.Call, bool) {
	for _, c := range batch {
		if c.IsTerminal {
			return c, true
		}
	}
	return ai.Call{}, false
}
