// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The scheduled runner's occurrences reach the same feed as everything else.
//
// This is what makes moving the read safe: the rail reported the overnight
// runner before the projection existed, so if the runner did not announce
// itself, moving the read would have emptied the rail for exactly the work it
// was built to show. Every row here is BORN through runner.Store.
//
// A queued job and the run that claims it are ONE occurrence. The old read
// removed that duplicate in Go, comparing trigger refs after the fact; here the
// table's own UNIQUE (source, occurrence_key) does it, and these tests are what
// hold the key to that meaning.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/aiactivity"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// runnerFixture is the runner store plus the consumer that projects what it
// announces, and one passport bound to a real person.
type runnerFixture struct {
	env       *Env
	runs      *runner.Store
	consumer  *aiactivity.Consumer
	passport  ids.PassportID
	owner     ids.UUID
	spec      runner.AgentSpec
	trigger   string
	delivered int
}

func newRunnerFixture(t *testing.T) *runnerFixture {
	t.Helper()
	env := Setup(t)
	owner := OwnerConn(t)
	passport := env.SeedPassport(t, owner, "runner activity probe")
	// SeedPassport binds the harness's Rep1 as the human behind it, which is
	// the attribution this suite is about.
	return &runnerFixture{
		env:      env,
		runs:     runner.NewStore(env.DB()),
		consumer: aiactivity.NewConsumer(aiactivity.NewStore(env.DB()), testLogger(t)),
		passport: ids.From[ids.PassportKind](passport),
		owner:    env.Rep1,
		spec:     runner.Catalog()[0],
		trigger:  "uat:" + ids.NewV7().String(),
	}
}

// drain hands the consumer every envelope this occurrence has staged since the
// last call — what a subscriber that is keeping up receives.
func (f *runnerFixture) drain(t *testing.T) {
	t.Helper()
	var raws [][]byte
	err := f.env.DB().Tx(context.Background(), func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `
			SELECT envelope FROM event_outbox
			 WHERE envelope->>'type' = 'ai_task.state_changed'
			   AND envelope->'payload'->>'source' = $1
			 ORDER BY seq OFFSET $2`, runner.ActivitySource, f.delivered)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			raws = append(raws, raw)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("reading the staged envelopes: %v", err)
	}
	for _, raw := range raws {
		var env kevents.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("decoding a staged envelope: %v", err)
		}
		if err := f.consumer.HandleEvent(context.Background(), env); err != nil {
			t.Fatalf("the projection refused envelope %s: %v", env.EventID, err)
		}
		f.delivered++
	}
}

// feed reads the passport owner's own view, as that person, with the day
// boundary taken from the database that stamped the rows.
// dbNow is the clock this suite measures and schedules against — the same one
// that stamps the rows it asserts about.
func (f *runnerFixture) dbNow(t *testing.T) time.Time {
	t.Helper()
	var now time.Time
	if err := f.env.Pool.QueryRow(context.Background(), `SELECT now()`).Scan(&now); err != nil {
		t.Fatalf("reading the database clock: %v", err)
	}
	return now
}

func (f *runnerFixture) feed(t *testing.T) (live, settled []aiactivity.Item) {
	t.Helper()
	var midnight time.Time
	if err := f.env.Pool.QueryRow(context.Background(),
		`SELECT date_trunc('day', now())`).Scan(&midnight); err != nil {
		t.Fatalf("reading the database's idea of today: %v", err)
	}
	live, settled, err := aiactivity.NewStore(f.env.DB()).
		Mine(f.env.As(f.owner, nil, principal.Permissions{}), midnight, nil)
	if err != nil {
		t.Fatalf("Mine: %v", err)
	}
	return live, settled
}

// A scheduled run reaches the rail through the projection, attributed to the
// human its passport acts for — not to whoever happened to run the scheduler.
func TestAScheduledRunReachesTheFeedAsItsOwnPersonsWork(t *testing.T) {
	f := newRunnerFixture(t)
	ctx := f.env.AgentCtxWithPassport(f.passport.UUID)

	if err := f.runs.EnqueueJob(ctx, f.spec.Name, f.trigger, &f.passport, f.dbNow(t)); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	f.drain(t)

	live, _ := f.feed(t)
	if len(live) != 1 {
		t.Fatalf("live = %d occurrences, want 1 — a queued run the rail cannot see is the regression moving the read would cause", len(live))
	}
	if live[0].State != "queued" || live[0].Kind != f.spec.Name {
		t.Fatalf("state/kind = %s/%s, want queued/%s", live[0].State, live[0].Kind, f.spec.Name)
	}
}

// The queued job and the run that claims it are ONE line, because they are one
// occurrence and the key says so.
func TestAJobAndTheRunThatClaimsItAreOneOccurrence(t *testing.T) {
	f := newRunnerFixture(t)
	ctx := f.env.AgentCtxWithPassport(f.passport.UUID)

	if err := f.runs.EnqueueJob(ctx, f.spec.Name, f.trigger, &f.passport, f.dbNow(t)); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if _, created, err := f.runs.StartRun(ctx, f.spec, f.trigger, f.passport); err != nil || !created {
		t.Fatalf("StartRun: created=%t err=%v", created, err)
	}
	f.drain(t)

	live, _ := f.feed(t)
	if len(live) != 1 {
		t.Fatalf("live = %d occurrences, want exactly 1 — the queue entry and its run must not both render", len(live))
	}
	if live[0].State != "running" {
		t.Fatalf("state = %s, want running: the run is the authority once it exists", live[0].State)
	}
}

// A run that finishes settles in the same line, and lands in what settled today.
func TestAFinishedRunSettlesInTheFeed(t *testing.T) {
	f := newRunnerFixture(t)
	ctx := f.env.AgentCtxWithPassport(f.passport.UUID)

	runID, created, err := f.runs.StartRun(ctx, f.spec, f.trigger, f.passport)
	if err != nil || !created {
		t.Fatalf("StartRun: created=%t err=%v", created, err)
	}
	if err := f.runs.SaveOutcome(ctx, runID, runner.Result{
		Outcome: runner.OutcomeCompleted, Final: json.RawMessage(`"done"`),
	}); err != nil {
		t.Fatalf("SaveOutcome: %v", err)
	}
	f.drain(t)

	live, settled := f.feed(t)
	if len(live) != 0 || len(settled) != 1 {
		t.Fatalf("live/settled = %d/%d, want 0/1", len(live), len(settled))
	}
	if settled[0].State != "done" {
		t.Fatalf("state = %s, want done", settled[0].State)
	}
}

// A finished run carries its own prose to the rail.
//
// The read this replaced pulled `summary` out of agent_run.result and showed it
// under a settled run; an announcement that dropped it would be a silent
// user-visible regression, invisible to every test that only checks state.
func TestAFinishedRunCarriesItsSummaryToTheFeed(t *testing.T) {
	f := newRunnerFixture(t)
	ctx := f.env.AgentCtxWithPassport(f.passport.UUID)

	runID, created, err := f.runs.StartRun(ctx, f.spec, f.trigger, f.passport)
	if err != nil || !created {
		t.Fatalf("StartRun: created=%t err=%v", created, err)
	}
	if err := f.runs.SaveOutcome(ctx, runID, runner.Result{
		Outcome: runner.OutcomeCompleted,
		Final:   json.RawMessage(`{"summary":"one at-risk deal flagged"}`),
	}); err != nil {
		t.Fatalf("SaveOutcome: %v", err)
	}
	f.drain(t)

	_, settled := f.feed(t)
	if len(settled) != 1 {
		t.Fatalf("settled = %d, want 1", len(settled))
	}
	if settled[0].Summary == nil || *settled[0].Summary != "one at-risk deal flagged" {
		t.Fatalf("summary = %v, want the run's own prose — the rail showed this before the read moved", settled[0].Summary)
	}
}

// The sweep gives up on a run, and the slow worker then finishes it anyway.
//
// SaveOutcome guards on id rather than on status precisely so a slow-but-alive
// run has the last word, and the feed has to follow the source rather than
// contradict it: both writes are terminal for one occurrence, so without a
// number that rises with the correction the projection keeps whichever landed
// first and the rail reports failed for a run that completed.
func TestALateFinishCorrectsARunTheSweepHadGivenUpOn(t *testing.T) {
	f := newRunnerFixture(t)
	ctx := f.env.AgentCtxWithPassport(f.passport.UUID)

	runID, created, err := f.runs.StartRun(ctx, f.spec, f.trigger, f.passport)
	if err != nil || !created {
		t.Fatalf("StartRun: created=%t err=%v", created, err)
	}
	// Age the row past any grace, which no writer can do: updated_at is stamped
	// inside each write's own transaction.
	if _, err := f.env.Pool.Exec(context.Background(),
		`UPDATE agent_run SET updated_at = now() - interval '2 hours' WHERE id = $1`, runID); err != nil {
		t.Fatalf("ageing the run: %v", err)
	}
	if _, err := f.runs.FailStuckRuns(ctx, time.Minute, runner.FailureReason("abandoned")); err != nil {
		t.Fatalf("FailStuckRuns: %v", err)
	}
	f.drain(t)
	if _, settled := f.feed(t); len(settled) != 1 || settled[0].State != "failed" {
		t.Fatalf("after the sweep, settled = %v, want one failed occurrence", settled)
	}

	if err := f.runs.SaveOutcome(ctx, runID, runner.Result{
		Outcome: runner.OutcomeCompleted, Final: json.RawMessage(`{"summary":"late but done"}`),
	}); err != nil {
		t.Fatalf("SaveOutcome: %v", err)
	}
	f.drain(t)

	_, settled := f.feed(t)
	if len(settled) != 1 {
		t.Fatalf("settled = %d occurrences, want 1 — one occurrence is one line however many writers touched it", len(settled))
	}
	if settled[0].State != "done" {
		t.Fatalf("state = %s, want done — the source says completed, so the rail must not keep saying failed", settled[0].State)
	}
}
