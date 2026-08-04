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
	"slices"
	"strings"
	"sync"
	"testing"
)

// minReason is the shortest text that can state a cost, measured on the text a
// reason states rather than the bytes it occupies: whitespace is normalised
// first, so padding is not length.
//
// Length is only half the floor. A reason that restates its own subject
// identifies the exception at any length without saying what it buys or gives
// up — which is the half a later reader needs in order to judge whether the
// trade still holds — so a restatement is refused however long the subject is.
const minReason = 20

// reasonEdgePunctuation is the quoting and terminal punctuation a restatement
// may wear without becoming a reason: `"enrich"`, `(enrich)` and `enrich.` all
// say exactly what `enrich` says.
const reasonEdgePunctuation = " \t\"'`“”‘’.,:;!?()[]{}<>"

// Waivers is a set of ratified exceptions to one gate's rule, each bound to the
// reason it costs.
//
// K is the vocabulary the gate's subjects are drawn from — a record type, a
// table name, an operationId. Keying by the caller's own named type keeps the
// type instead of casting it away at the lookup, so a record-type waiver cannot
// be keyed by a tool name. It is a string type, which is what makes the report
// order below total: every subject has one rendering, and no two distinct
// subjects share it.
type Waivers[K ~string] struct {
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
func Waive[K ~string](entries map[K]string) *Waivers[K] {
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
	// The reason floor below reports through t, and Helper marking is
	// per-function: without this the failure is attributed to gatekit's own line
	// rather than to the gate that relied on the waiver.
	t.Helper()
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	reason, ratified := w.reasons[subject]
	if !ratified {
		return false
	}
	w.matched[subject] = true
	stated := statedText(reason)
	switch {
	case restatesSubject(stated, string(subject)):
		t.Errorf("waiver %s answers with its own subject (%q): state what it costs, so the next "+
			"reader can judge whether the cost is still worth paying", subject, reason)
	case len(stated) < minReason:
		t.Errorf("waiver %s carries no usable reason (%q): state what it costs, so the next "+
			"reader can judge whether the cost is still worth paying", subject, reason)
	}
	return true
}

// statedText reduces a reason to the text it states: no surrounding space, and
// every internal run of whitespace read as the one space it stands for.
func statedText(reason string) string {
	return strings.Join(strings.Fields(reason), " ")
}

// restatesSubject reports whether a reason says its subject over again and
// nothing more. The whole text is compared, never a prefix: a reason that opens
// with its subject and then explains it — `enrich — the call fetches the
// target's own website` — is the shape several gates write, and it does state a
// cost.
func restatesSubject(stated, subject string) bool {
	return strings.EqualFold(strings.Trim(stated, reasonEdgePunctuation), strings.TrimSpace(subject))
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
	slices.Sort(subjects)
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
			stale = append(stale, string(subject))
		}
	}
	slices.Sort(stale)
	for _, subject := range stale {
		t.Errorf("waiver %s matched no subject: delete it, or correct the key if the subject "+
			"was renamed", subject)
	}
}
