// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The seatless-workspace gauge: the number of live workspaces the last
// extension-job dispatch skipped because they hold no agent seat.
//
// It exists because the alternative was worse. Nothing in the product creates
// an agent seat yet (margince-poc-v1#656) and a composed unit ships enabled at
// its declared cadence, so on a fresh installation every extension tick had no
// initiator to name — and the dispatcher's original posture (enqueue anyway,
// fail at the authority derivation) turned that one missing row into three
// failures a minute per workspace, forever. Skipping alone would have traded a
// log storm for silence, and a dead capability nobody can see is not an
// improvement. This number is the third option: the condition is REPORTED, as
// a quantity an operator can alert on, without being reported as a fault.
//
// A GAUGE and not a counter: the question is "how many tenants cannot run their
// extension jobs right now", which is a level. It reads zero on an installation
// whose workspaces all hold a seat, and drops to zero the moment a seat is
// inserted, so it also answers "did the fix take".
//
// It is ABSENT from a process that has never dispatched — the api role, and a
// worker before its first tick. Zero there would be a fabricated reading of a
// fleet this process has not looked at, indistinguishable from a healthy one;
// the same "declared or absent" posture every other section in the exposition
// takes for a number it could not measure.

import (
	"fmt"
	"io"
	"sync/atomic"
)

// neverDispatched is the sentinel the gauge starts at, and it is why the count
// is signed: a skipped-workspace count cannot be negative, so no dispatch can
// ever produce this value, and "absent" needs no second variable to track it.
const neverDispatched = -1

// seatlessWorkspaces is the last dispatch's count, process-wide rather than
// per-worker: the metrics endpoint reads it without holding any dispatcher
// instance, the same shape platform/events.PublishedTotal already establishes.
//
// LAST dispatch, not a running total, because it is a level. Every composed
// job's dispatcher writes it, so on an installation with several units the
// value is whichever fanned out most recently — they all enumerate the same
// fleet and ask the same question of it, so they cannot disagree except
// transiently.
var seatlessWorkspaces = func() *atomic.Int64 {
	var v atomic.Int64
	v.Store(neverDispatched)
	return &v
}()

// recordSeatlessWorkspaces publishes one dispatch's skipped count.
func recordSeatlessWorkspaces(n int) { seatlessWorkspaces.Store(int64(n)) }

// SeatlessWorkspaces reports how many live workspaces the last extension-job
// dispatch skipped for want of an agent seat, or neverDispatched when this
// process has not fanned an extension job out at all.
func SeatlessWorkspaces() int64 { return seatlessWorkspaces.Load() }

// WriteSeatlessWorkspacesGauge renders the gauge into a /metrics exposition,
// and writes NOTHING from a process that has never dispatched (see above).
//
// Exported because the worker's observe listener assembles its own exposition
// and passes no job-stats section — and the worker is the process that actually
// dispatches, so an unexported gauge would be readable only from the role that
// can never populate it.
//
// No workspace_id label: the number is a fleet count, and labelling it per
// tenant would mint a series per workspace for a condition that is currently
// true of ALL of them on a fresh installation — the widest possible label set
// for the least informative case.
func WriteSeatlessWorkspacesGauge(w io.Writer) error {
	n := SeatlessWorkspaces()
	if n == neverDispatched {
		return nil
	}
	_, err := fmt.Fprintf(w,
		"# HELP margince_extension_job_seatless_workspaces Live workspaces skipped by the last extension-job dispatch because they hold no agent seat (see issue 656).\n"+
			"# TYPE margince_extension_job_seatless_workspaces gauge\n"+
			"margince_extension_job_seatless_workspaces %d\n", n)
	return err
}
