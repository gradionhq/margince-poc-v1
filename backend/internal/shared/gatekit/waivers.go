// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package gatekit is where this repo's fitness gates hold their waivers and
// their walk scopes.
//
// A waiver carries two obligations: it states what it costs, and it describes
// code that still exists. Both are enforced here rather than per gate, so a
// gate cannot hold its exceptions to a weaker standard than its neighbours.
//
// gatekit is imported only from _test.go files. A waiver mechanism reachable
// from product code would be a way to mark shipped behaviour "ratified", which
// is not what a waiver is for; the census holds that line.
package gatekit

import (
	"fmt"
	"sort"
	"sync"
	"testing"
)

// minReason is the shortest text that can state a cost. A reason that only
// names its own subject, or repeats its key, identifies the exception without
// saying what it buys or gives up — which is the half a later reader needs in
// order to judge whether the trade still holds.
const minReason = 20

// Waivers is a set of ratified exceptions to one gate's rule, each bound to the
// reason it costs.
//
// K is the vocabulary the gate's subjects are drawn from — a record type, a
// table name, an operationId. Keying by the caller's own named type keeps the
// type instead of casting it away at the lookup, so a record-type waiver cannot
// be keyed by a tool name.
type Waivers[K comparable] struct {
	// mu guards matched. A package-level Waivers is shared by every test in its
	// package, so concurrent access is possible the moment any of them calls
	// t.Parallel(); an unguarded map would surface as a stale-waiver report at
	// random.
	mu      sync.Mutex
	reasons map[K]string
	matched map[K]bool
}

// Waive ratifies entries, each mapping a subject to what waiving it costs. The
// input is copied: a caller that mutates its literal afterwards must not widen
// what was ratified.
func Waive[K comparable](entries map[K]string) *Waivers[K] {
	reasons := make(map[K]string, len(entries))
	for subject, reason := range entries {
		reasons[subject] = reason
	}
	return &Waivers[K]{reasons: reasons, matched: make(map[K]bool, len(entries))}
}

// Waived reports whether subject is ratified, recording the match so
// AssertAllMatched can tell a live waiver from one describing code that is gone.
//
// It takes t because a reasonless entry must fail where it is RELIED ON: a
// separate validation step is one more call a new gate can omit, and a waiver
// whose reason nothing checks is exactly the gap this package closes.
func (w *Waivers[K]) Waived(t testing.TB, subject K) bool {
	t.Helper()
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.waivedLocked(t, subject)
}

// Reason returns the ratified reason, for a gate that reports its own waivers.
// Reading a reason is relying on the waiver, so it counts as a match.
func (w *Waivers[K]) Reason(t testing.TB, subject K) (string, bool) {
	t.Helper()
	if w == nil {
		return "", false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.waivedLocked(t, subject) {
		return "", false
	}
	return w.reasons[subject], true
}

// Subjects returns every ratified subject in a deterministic order, for the
// gates that walk their waivers rather than querying them. It does NOT mark
// anything matched: enumerating the set is not yet relying on any entry.
func (w *Waivers[K]) Subjects() []K {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	subjects := make([]K, 0, len(w.reasons))
	for subject := range w.reasons {
		subjects = append(subjects, subject)
	}
	sort.Slice(subjects, func(i, j int) bool {
		return fmt.Sprintf("%v", subjects[i]) < fmt.Sprintf("%v", subjects[j])
	})
	return subjects
}

// AssertAllMatched reports every entry no subject reached. Such an entry
// certifies nothing while reading as though it does, which is worse than no
// waiver at all.
//
// Call it from exactly ONE place per declaration: matched accumulates across
// every test in the package, so two tests each sweeping half the subjects would
// make whichever asserted first report false staleness. The owner is the test
// that enumerates the full subject set.
func (w *Waivers[K]) AssertAllMatched(t testing.TB) {
	t.Helper()
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	stale := make([]string, 0, len(w.reasons))
	for subject := range w.reasons {
		if !w.matched[subject] {
			stale = append(stale, fmt.Sprintf("%v", subject))
		}
	}
	sort.Strings(stale)
	for _, subject := range stale {
		t.Errorf("waiver %s matched no subject: delete it, or correct the key if the subject "+
			"was renamed", subject)
	}
}

// waivedLocked is the shared body of Waived and Reason. mu is held.
func (w *Waivers[K]) waivedLocked(t testing.TB, subject K) bool {
	// Helper marking is per-function, and the reason-floor Errorf lives here:
	// without this the failure is reported at gatekit's own line rather than at
	// the gate that relied on the waiver.
	t.Helper()
	reason, ratified := w.reasons[subject]
	if !ratified {
		return false
	}
	w.matched[subject] = true
	if len(reason) < minReason {
		t.Errorf("waiver %v carries no usable reason (%q): state what it costs, so the next "+
			"reader can judge whether the cost is still worth paying", subject, reason)
	}
	return true
}
