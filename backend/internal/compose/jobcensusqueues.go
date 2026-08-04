// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The queue half of the census: api/jobs.yaml's queues: block held to the pool
// set the runner actually hands River.
//
// Generation checks that every kind's queue: names a declared entry, so the
// references are sound — but the entries themselves described a pool nothing
// compared them to. Two silent failures live in that gap. A bound changed in
// jobQueues() and not in the file leaves operators reading a number no client
// runs at. A queue declared in the file and never built is worse: the fan-out
// helper reads the DECLARED queue when it inserts a child, so the rows land on
// a queue no client works, and they sit runnable forever while the dispatcher
// that made them keeps reporting a clean pass.

import (
	"fmt"
	"maps"
	"slices"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// declaredQueueFloor guards against a vacuous pass, on the same reasoning as
// declaredJobKindFloor: the contract declares eight queues, and a table
// answering none would make the walk below iterate zero times and read clean.
const declaredQueueFloor = 6

// everyDeclaredQueueIsBuiltWithItsDeclaredBound compares the two spellings of
// the queue set, both ways: name for name, and bound for bound.
func (c *JobCensus) everyDeclaredQueueIsBuiltWithItsDeclaredBound() []string {
	declared := jobs.DeclaredQueues()
	built := jobQueues()

	var findings []string
	for _, name := range slices.Sorted(maps.Keys(declared)) {
		config, exists := built[name]
		if !exists {
			findings = append(findings, fmt.Sprintf(
				"queue %q is declared but jobQueues() builds no such pool — a fan-out child is inserted on its DECLARED queue, so its rows would sit runnable with no client working them; build it, or retire the declaration", name))
			continue
		}
		if config.MaxWorkers != declared[name] {
			findings = append(findings, fmt.Sprintf(
				"queue %q declares max_workers %d but jobQueues() builds %d — the file is what operators read; move the number in both places", name, declared[name], config.MaxWorkers))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(built)) {
		if _, ok := declared[name]; !ok {
			findings = append(findings, fmt.Sprintf(
				"queue %q is built but api/jobs.yaml declares no such entry — a pool with no stated posture is a tuning knob; declare it with its reason and run `make gen`", name))
		}
	}
	if len(declared) < declaredQueueFloor {
		findings = append(findings, fmt.Sprintf(
			"the contract declares only %d queues, expected at least %d — both walks above read that table, so a census over an empty one reports nothing and reads clean", len(declared), declaredQueueFloor))
	}
	return findings
}
