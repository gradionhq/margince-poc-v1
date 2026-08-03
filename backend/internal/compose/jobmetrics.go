// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The job-runtime section of /metrics: OPS-MET-2's queue depth and the
// gauges beside it, plus the sweep pair. Every number is read from
// river_job at scrape time rather than counted in process, because the
// dispatchers run in cmd/worker and cmd/worker serves no exposition
// endpoint at all — an in-process counter incremented there would be
// invisible to every scrape while the api's own copy read a truthful
// looking zero.

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// queueDepthStates are the states a job is WAITING in. A job backing off
// toward its next attempt is queued work nobody has done, so retryable
// belongs here — OPS-MET-2 asks how much work is outstanding, and a queue
// full of retrying jobs is the case the metric exists to surface. pending
// is River's staged state (its migration 004): nothing in this tree sets
// it today, and it is counted rather than dropped because a state the
// renderer has never heard of vanishing is the posture this whole phase
// exists to remove.
var queueDepthStates = map[string]bool{
	"available": true,
	"scheduled": true,
	"retryable": true,
	"pending":   true,
}

// queueKey identifies one series of the per-queue gauges.
type queueKey struct{ queue, workspace string }

// kindKey identifies one series of the per-kind gauges. Dead work is keyed
// by kind rather than by queue because "which work will never run" is a
// question about the kind, not about the lane it happened to sit in.
type kindKey struct{ kind, workspace string }

// stateKey identifies one series of the unrecognised-state gauge, which
// carries the state itself so an operator can see what appeared.
type stateKey struct{ state, queue, workspace string }

// The river_job_state values this renderer classifies by name. Named so
// the spelling that decides which gauge a row lands on exists once.
const (
	stateRunning   = "running"
	stateDiscarded = "discarded"
	stateCancelled = "cancelled"
)

// jobSeries is one pass of the snapshot, bucketed per family.
type jobSeries struct {
	depth        map[queueKey]int64
	running      map[queueKey]int64
	discarded    map[kindKey]int64
	cancelled    map[kindKey]int64
	oldest       map[queueKey]float64
	unrecognised map[stateKey]int64
}

// aggregate buckets every row exactly once. Split out of writeJobMetrics so
// the classification reads as one list of cases rather than as a preamble to
// seven writes.
func aggregate(rows []jobs.StateRow) jobSeries {
	a := jobSeries{
		depth:        map[queueKey]int64{},
		running:      map[queueKey]int64{},
		discarded:    map[kindKey]int64{},
		cancelled:    map[kindKey]int64{},
		oldest:       map[queueKey]float64{},
		unrecognised: map[stateKey]int64{},
	}
	for _, r := range rows {
		qk := queueKey{queue: r.Queue, workspace: r.WorkspaceID}
		kk := kindKey{kind: r.Kind, workspace: r.WorkspaceID}
		switch {
		case queueDepthStates[r.State]:
			a.depth[qk] += r.Count
		case r.State == stateRunning:
			a.running[qk] += r.Count
		case r.State == stateDiscarded:
			a.discarded[kk] += r.Count
		case r.State == stateCancelled:
			// Reported apart from discarded on purpose. River cancels
			// deliberately, without spending every attempt, so folding the
			// two together would make the discarded gauge's own HELP text
			// false for half the rows under it.
			a.cancelled[kk] += r.Count
		default:
			a.unrecognised[stateKey{state: r.State, queue: r.Queue, workspace: r.WorkspaceID}] += r.Count
		}
		a.ageFrom(qk, r)
	}
	return a
}

// ageFrom folds one row into the per-queue oldest-runnable age.
//
// The age is the WORST case across the kinds sharing a queue; reporting the
// last row read would make the number depend on the order the database
// returned groups in.
//
// Only WAITING rows contribute a series at all. The gauge measures the
// oldest runnable-and-UNCLAIMED job, so a running row is not its subject —
// it has already been claimed — and a discarded one never will be. A queue
// holding nothing but those has no such job, and emitting a zero for it
// would answer the gauge's own question with a number about work that is
// not waiting. It is also what keeps this gauge agreeing with the endpoint,
// which reports null for exactly these rows.
//
// A zero from a queue that DOES hold waiting work is a measured value: the
// work is there and none of it is late yet.
func (a jobSeries) ageFrom(qk queueKey, r jobs.StateRow) {
	if !queueDepthStates[r.State] {
		return
	}
	if _, seen := a.oldest[qk]; !seen || r.OldestRunnableAgeSeconds > a.oldest[qk] {
		a.oldest[qk] = r.OldestRunnableAgeSeconds
	}
}

// writeJobMetrics renders the job-runtime section. Pure over snap so the
// exposition text is provable without a database.
//
// workspace_id is admitted on these gauges by ADR-0080/A125 — the id, never
// a name, because the exposition endpoint has no redaction path. An empty
// value is a dispatcher, exactly and only: a job that does tenant work
// declares its workspace, and a null in that column means a dispatcher and
// nothing else.
//
// It returns an error because an io.Writer can fail. A scrape that
// swallowed a refused write would serve a truncated exposition, which
// parses as a smaller fleet rather than as a broken one.
func writeJobMetrics(w io.Writer, snap jobs.Snapshot) error {
	a := aggregate(snap.Rows)
	depth, running := a.depth, a.running
	discarded, cancelled := a.discarded, a.cancelled
	oldest, unrecognised := a.oldest, a.unrecognised

	if err := writeQueueGauge(w, "margince_job_queue_depth",
		"Jobs waiting to run (available + scheduled + retryable + pending) per queue and workspace. An empty workspace_id is a dispatcher, which does no tenant work.",
		depth); err != nil {
		return err
	}
	if err := writeQueueGauge(w, "margince_job_running",
		"Jobs currently executing per queue and workspace.",
		running); err != nil {
		return err
	}
	if err := writeKindGauge(w, "margince_job_discarded",
		"Jobs that exhausted every attempt and will never run without intervention, per kind and workspace.",
		discarded); err != nil {
		return err
	}
	if err := writeKindGauge(w, "margince_job_cancelled",
		"Jobs stopped deliberately before their attempts ran out, per kind and workspace. Counted apart from discarded because the operator story differs -- a discarded job spent every attempt, a cancelled one was stopped. Both are work that will not happen, which is why the sweep pair counts either as a workspace missed.",
		cancelled); err != nil {
		return err
	}
	if err := writeAgeGauge(w, oldest); err != nil {
		return err
	}
	if err := writeSweepGauges(w, snap.Sweeps); err != nil {
		return err
	}
	return writeUnrecognisedStateGauge(w, unrecognised)
}

func writeQueueGauge(w io.Writer, name, help string, series map[queueKey]int64) error {
	if err := writeFamilyHeader(w, name, help); err != nil {
		return err
	}
	for _, k := range sortedQueueKeys(series) {
		if _, err := fmt.Fprintf(w, "%s{queue=%s,workspace_id=%s} %d\n",
			name, label(k.queue), label(k.workspace), series[k]); err != nil {
			return err
		}
	}
	return nil
}

func writeKindGauge(w io.Writer, name, help string, series map[kindKey]int64) error {
	if err := writeFamilyHeader(w, name, help); err != nil {
		return err
	}
	keys := sortedKeysOf(series, func(a, b kindKey) int {
		return cmp.Or(cmp.Compare(a.kind, b.kind), cmp.Compare(a.workspace, b.workspace))
	})
	for _, k := range keys {
		if _, err := fmt.Fprintf(w, "%s{kind=%s,workspace_id=%s} %d\n",
			name, label(k.kind), label(k.workspace), series[k]); err != nil {
			return err
		}
	}
	return nil
}

func writeAgeGauge(w io.Writer, series map[queueKey]float64) error {
	const name = "margince_job_oldest_queued_age_seconds"
	if err := writeFamilyHeader(w, name,
		"Seconds the oldest runnable-and-unclaimed job has waited per queue and workspace. A job scheduled for the future is not counted; it is not late."); err != nil {
		return err
	}
	for _, k := range sortedQueueKeys(series) {
		if _, err := fmt.Fprintf(w, "%s{queue=%s,workspace_id=%s} %.0f\n",
			name, label(k.queue), label(k.workspace), series[k]); err != nil {
			return err
		}
	}
	return nil
}

// writeSweepGauges renders the pair that answers "are tenants being
// missed". Both halves carry the same sweep label so an alert can compare
// them; the label is the CHILD kind, which is what the rows hold — mapping
// back to the dispatcher would be a hand-kept table.
func writeSweepGauges(w io.Writer, sweeps []jobs.SweepPass) error {
	ordered := slices.SortedFunc(slices.Values(sweeps), func(a, b jobs.SweepPass) int {
		return cmp.Compare(a.Kind, b.Kind)
	})

	if err := writeFamilyHeader(w, "margince_sweep_workspaces_total",
		"Workspaces with a surviving child of this fleet pass. Counted per workspace rather than per pass: a child still active from an earlier fan-out is deduplicated out of the current one and writes no new row, so no batch can be identified. A workspace whose only child aged out of River's job retention is absent rather than reported as zero."); err != nil {
		return err
	}
	for _, s := range ordered {
		if _, err := fmt.Fprintf(w, "margince_sweep_workspaces_total{sweep=%s} %d\n",
			label(s.Kind), s.Workspaces); err != nil {
			return err
		}
	}

	if err := writeFamilyHeader(w, "margince_sweep_workspaces_failed",
		"Workspaces whose MOST RECENT child of this fleet pass ended discarded or cancelled — tenants whose share of the pass did not happen. A workspace that failed and then succeeded is not counted."); err != nil {
		return err
	}
	for _, s := range ordered {
		if _, err := fmt.Fprintf(w, "margince_sweep_workspaces_failed{sweep=%s} %d\n",
			label(s.Kind), s.Failed); err != nil {
			return err
		}
	}
	return nil
}

// writeUnrecognisedStateGauge reports work sitting in a state no arm above
// claimed — a River release that adds one, or a state this renderer was
// never taught. Its header is written ONLY when there is something to
// report: unlike the families above, a permanently empty series here would
// be noise on every dashboard, and its whole purpose is to be absent.
func writeUnrecognisedStateGauge(w io.Writer, series map[stateKey]int64) error {
	if len(series) == 0 {
		return nil
	}
	const name = "margince_job_unrecognised_state"
	if err := writeFamilyHeader(w, name,
		"Jobs in a state this exposition does not classify. Present only when such work exists; investigate rather than graph."); err != nil {
		return err
	}
	keys := sortedKeysOf(series, func(a, b stateKey) int {
		return cmp.Or(
			cmp.Compare(a.state, b.state),
			cmp.Compare(a.queue, b.queue),
			cmp.Compare(a.workspace, b.workspace))
	})
	for _, k := range keys {
		if _, err := fmt.Fprintf(w, "%s{state=%s,queue=%s,workspace_id=%s} %d\n",
			name, label(k.state), label(k.queue), label(k.workspace), series[k]); err != nil {
			return err
		}
	}
	return nil
}

func writeFamilyHeader(w io.Writer, name, help string) error {
	_, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
	return err
}

// label renders one label value for the Prometheus text format, which
// defines exactly three escapes — backslash, double quote and newline —
// and no others.
//
// Go's %q is not that format. It emits \t, \r and \xNN for control
// characters, and a parser meeting an escape the format does not define
// rejects the whole exposition rather than the one series. That matters
// here because workspace_id is args->>'workspace_id' verbatim from a table
// with no constraint on it and direct app-role CRUD, so a single
// raw-inserted row could otherwise poison every scrape for every metric.
// Anything outside the three escapes is dropped rather than encoded: a
// control character in a label value is already a broken row, and a
// readable scrape that omits it beats a rejected one that carries it.
func label(value string) string {
	var b strings.Builder
	b.Grow(len(value) + 2)
	b.WriteByte('"')
	for _, r := range value {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\n':
			b.WriteString(`\n`)
		case r < ' ' || r == 0x7f:
			// Dropped: see above.
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// sortedQueueKeys orders the per-queue series. A scrape target's series
// order should not flap between scrapes for no reason, and map iteration
// order is not stable.
func sortedQueueKeys[V any](series map[queueKey]V) []queueKey {
	return sortedKeysOf(series, func(a, b queueKey) int {
		return cmp.Or(cmp.Compare(a.queue, b.queue), cmp.Compare(a.workspace, b.workspace))
	})
}

// sortedKeysOf answers a series map's keys in the caller's order.
func sortedKeysOf[K comparable, V any](series map[K]V, order func(a, b K) int) []K {
	keys := make([]K, 0, len(series))
	for k := range series {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, order)
	return keys
}

// jobMetricsSection binds the job gauges to a snapshot reader. A failed
// read logs and writes NOTHING for the section: a scrape that emits zeroes
// it did not measure is worse than a gap, because a gap is visible on a
// graph while a fabricated zero reads as a healthy empty queue. This is the
// same posture Metrics already takes for the outbox backlog.
//
// The reader is a closure rather than the pool itself, matching how the
// outbox backlog is injected next door — so that posture is provable
// without a database, which is the only way it stays true.
//
// The ctx handed in is the exposition handler's own budget context, NOT the
// request's. The overlay section next door passes r.Context() and so runs
// unbounded; this read is the one that needs the budget, because it scans a
// table no index covers.
func jobMetricsSection(read func(context.Context) (jobs.Snapshot, error)) func(context.Context, io.Writer) error {
	return func(ctx context.Context, w io.Writer) error {
		snap, err := read(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "metrics: job stats query failed", "err", err)
			// A section that could not be measured is absent, not fatal: the
			// rest of the exposition is still true and still worth serving.
			return nil
		}
		return writeJobMetrics(w, snap)
	}
}
