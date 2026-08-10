// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The binding marker's RUN lifecycle. A reindex is one claim and N job rows, so
// the marker has to say which run holds it and which workspaces that run is
// still waiting on — and every one of these scenarios is a way the two could
// come apart: a straggler of a finished run, a dispatcher fanning out twice, and
// a run that stopped moving with no job left to release it.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// claimOf is an ordinary confirm's claim: a fresh run over identity, with no
// licence to take the marker off anybody (StealAfter zero).
func claimOf(identity string) search.ReembedClaim {
	return search.ReembedClaim{Run: ids.NewV7(), TargetIdentity: identity}
}

// TestAStragglerOfAFinishedRunCannotActOnTheRunThatReplacedIt is why the fence
// is the run and not the target identity. A forced rebuild re-runs deliberately
// under the SAME identity, so an identity fence does not fence at all between
// consecutive runs: a child that outlived its own run — one whose confirm was
// forced over it, or one whose process was still finishing an embed the run had
// given up on — would remove its workspace from the NEXT run's set and, if that
// emptied it, release a marker whose own children are still working. That is the
// fleet reporting itself re-embedded while it is not.
func TestAStragglerOfAFinishedRunCannotActOnTheRunThatReplacedIt(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	const identity = "fake/same-identity@1024"
	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	ws := ids.From[ids.WorkspaceKind](e.WS)

	finished := claimOf(identity)
	if err := e.Store.ClaimAndEnqueueReembedding(ctx, finished, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("claiming the first run: %v", err)
	}
	if err := e.Store.SeedReembeddingFleet(ctx, finished.Run, []ids.WorkspaceID{ws}, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("seeding the first run: %v", err)
	}
	if err := e.Store.FinishWorkspaceReembedding(ctx, finished.Run, ws); err != nil {
		t.Fatalf("finishing the first run's only workspace: %v", err)
	}

	// The replacement run: same identity, as `force` produces.
	current := claimOf(identity)
	if err := e.Store.ClaimAndEnqueueReembedding(ctx, current, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("claiming the replacement run: %v", err)
	}
	if err := e.Store.SeedReembeddingFleet(ctx, current.Run, []ids.WorkspaceID{ws}, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("seeding the replacement run: %v", err)
	}

	// The first run's straggler finally returns.
	if err := e.Store.FinishWorkspaceReembedding(ctx, finished.Run, ws); err != nil {
		t.Fatalf("the straggler must be a no-op, got: %v", err)
	}

	_, status, _, err := e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "reembedding" {
		t.Fatalf("marker status = %q, want reembedding — a straggler of the previous run released a run whose own workspace is still outstanding", status)
	}
	// The set itself, not only the status: a straggler that emptied it without
	// releasing would leave the replacement run holding a marker its own child
	// can no longer account for, which the status alone does not show.
	var pending int
	if err := e.Owner.QueryRow(ctx,
		`SELECT cardinality(reembedding_pending) FROM embed_store_binding WHERE singleton`).Scan(&pending); err != nil {
		t.Fatalf("reading the replacement run's pending set: %v", err)
	}
	if pending != 1 {
		t.Fatalf("the replacement run has %d workspaces outstanding, want 1 — a straggler of the previous run took one out of a set that was never its own", pending)
	}
	// And the replacement's own child still empties its set when it reports.
	if err := e.Store.FinishWorkspaceReembedding(ctx, current.Run, ws); err != nil {
		t.Fatalf("finishing the replacement run's workspace: %v", err)
	}
	if _, status, _, err = e.Store.PopulatedIdentity(ctx); err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "idle" {
		t.Fatalf("marker status = %q after the replacement run's own workspace finished, want idle", status)
	}
}

// TestASecondFanOutOfTheSameRunIsRefused pins the seed-once rule. A dispatcher
// that committed its fan-out and then died before River finalised it comes back
// on a retry; re-seeding would put back workspaces whose children have since
// finished, and ByArgs uniqueness may decline to re-enqueue a child that is
// still running — leaving those workspaces in the set with nothing left to take
// them out.
func TestASecondFanOutOfTheSameRunIsRefused(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	const identity = "fake/seed-once@1024"
	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	claim := claimOf(identity)
	if err := e.Store.ClaimAndEnqueueReembedding(ctx, claim, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("claiming: %v", err)
	}
	fleet := []ids.WorkspaceID{ids.From[ids.WorkspaceKind](e.WS)}
	if err := e.Store.SeedReembeddingFleet(ctx, claim.Run, fleet, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("first seed: %v", err)
	}

	var reFannedOut bool
	err := e.Store.SeedReembeddingFleet(ctx, claim.Run, fleet, func(pgx.Tx) error {
		reFannedOut = true
		return nil
	})
	if !errors.Is(err, search.ErrReembeddingSuperseded) {
		t.Fatalf("second seed of the same run = %v, want ErrReembeddingSuperseded", err)
	}
	if reFannedOut {
		t.Fatal("a refused seed must not fan the fleet out a second time")
	}
}

// steppingClock advances by step on every read, so a pass that consults it once
// per entity behaves as one whose embeds take that long — the suite pins the
// clock rather than waiting out a reporting interval it would otherwise have to
// sleep through (T11).
func steppingClock(step time.Duration) func() time.Time {
	at := time.Now()
	return func() time.Time {
		at = at.Add(step)
		return at
	}
}

// markerAge is how long ago the binding marker last moved, measured by the
// database's own clock — the same comparison the steal predicate makes.
func markerAge(t *testing.T, e *SearchEnv) time.Duration {
	t.Helper()
	var seconds float64
	if err := e.Owner.QueryRow(context.Background(),
		`SELECT extract(epoch FROM now() - updated_at)::float8 FROM embed_store_binding WHERE singleton`).Scan(&seconds); err != nil {
		t.Fatalf("reading the marker's age: %v", err)
	}
	return time.Duration(seconds * float64(time.Second))
}

// ageMarkerPastTheStealWindow puts the marker's last movement two hours back,
// which is what a run that stopped reporting leaves behind. Aged rather than
// waited out: a suite that waits an hour is a suite nobody runs.
func ageMarkerPastTheStealWindow(t *testing.T, e *SearchEnv) {
	t.Helper()
	if _, err := e.Owner.Exec(context.Background(),
		`UPDATE embed_store_binding SET updated_at = now() - interval '2 hours' WHERE singleton`); err != nil {
		t.Fatalf("ageing the marker past the steal window: %v", err)
	}
}

// TestAHealthyRunIsNotStealableHoweverLongItTakes is the other half of the
// steal, and the half that is easy to lose. A workspace pass is allowed to run
// for hours — its worker declares Timeout() == -1 precisely so a large corpus is
// not cut off mid-pass — and nothing else moves the marker between the fan-out
// and that workspace finishing. So without the pass reporting its own progress,
// "no movement for an hour" would mean "one big tenant is still going", and a
// routine forced rebuild would dispossess a run that is working perfectly:
// two children re-embedding the same corpus at once, doubling model spend on the
// largest tenant in the fleet.
func TestAHealthyRunIsNotStealableHoweverLongItTakes(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	fake := ai.NewFakeClient()
	embedder := fakeEmbedderNamed(t, fake, "model-slow-but-healthy")
	identity, _ := embedder.EmbedIdentity()
	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	ws := ids.From[ids.WorkspaceKind](e.WS)

	working := claimOf(identity)
	if err := e.Store.ClaimAndEnqueueReembedding(ctx, working, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("claiming the run: %v", err)
	}
	if err := e.Store.SeedReembeddingFleet(ctx, working.Run, []ids.WorkspaceID{ws}, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("seeding the run: %v", err)
	}

	for i := range 4 {
		e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, $3, 'manual', 'human:x')`,
			fmt.Sprintf("Slow Corpus Person %d", i))
	}

	// The run has been going long enough to look abandoned, and its one
	// workspace has not finished — the exact shape a steal must NOT take.
	ageMarkerPastTheStealWindow(t, e)

	// A clock that jumps a reporting interval per entity: the pass then behaves
	// exactly as one whose embeds are genuinely that slow, without the suite
	// waiting for any of it. The pass is what has to keep the marker fresh —
	// nothing here finishes the workspace.
	slow := steppingClock(search.ReembedProgressStaleness + time.Minute)
	pass := search.ReembedPass{Run: working.Run, Identity: identity, Now: slow}
	if err := e.Store.ReembedWorkspace(ctx, pass, ws, embedder); err != nil {
		t.Fatalf("the healthy pass: %v", err)
	}

	err := e.Store.ClaimAndEnqueueReembedding(ctx,
		search.ReembedClaim{Run: ids.NewV7(), TargetIdentity: identity, StealAfter: time.Hour},
		func(pgx.Tx) error { return nil })
	if !errors.Is(err, search.ErrReembeddingInFlight) {
		t.Fatalf("a forced confirm over a run that is embedding = %v, want ErrReembeddingInFlight — a pass reporting real progress was dispossessed, and two children now re-embed the same corpus at once", err)
	}
}

// probingEmbedder runs probe once, at the start of the FIRST embed of a pass,
// and then delegates every call unchanged. The first embed is the earliest
// moment a suite can stand INSIDE a pass and ask what the marker looks like from
// there — which is the whole question when the legs that run before it are the
// long ones.
type probingEmbedder struct {
	search.Embedder
	probe  func()
	probed bool
}

func (p *probingEmbedder) Embed(ctx context.Context, req model.EmbedRequest) (model.Embeddings, error) {
	if !p.probed {
		p.probed = true
		p.probe()
	}
	return p.Embedder.Embed(ctx, req)
}

// TestReembedReportsProgressBeforeItsScanAndItsFirstEmbed is the other half of
// "a working run is never stale enough to dispossess", and the half a pass that
// reported only after an entity completed did not have. Two legs run before the
// first entity ever finishes and neither can report from inside itself: the
// workspace's entity tables are scanned whole, and the first upsert waits on
// pool acquisition and row locks before it reaches the model. A run that spent
// both of them silent looks abandoned while it is working, and a routine forced
// rebuild would then take its marker.
//
// The suite stands in for both legs. The marker is aged before the pass, which
// is what the run's own dispatch and queue time leave behind; and it is aged
// AGAIN the moment the pass starts, which is what a scan too big to walk quickly
// does to it. By the time the pass reaches its first embed, a forced confirm
// must find a marker fresh enough to refuse.
func TestReembedReportsProgressBeforeItsScanAndItsFirstEmbed(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	fake := ai.NewFakeClient()
	delegate := fakeEmbedderNamed(t, fake, "model-slow-first-entity")
	identity, _ := delegate.EmbedIdentity()
	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	ws := ids.From[ids.WorkspaceKind](e.WS)

	working := claimOf(identity)
	if err := e.Store.ClaimAndEnqueueReembedding(ctx, working, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("claiming the run: %v", err)
	}
	if err := e.Store.SeedReembeddingFleet(ctx, working.Run, []ids.WorkspaceID{ws}, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("seeding the run: %v", err)
	}
	for i := range 2 {
		e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, $3, 'manual', 'human:x')`,
			fmt.Sprintf("Slow First Entity Person %d", i))
	}
	ageMarkerPastTheStealWindow(t, e)

	// The pass's own clock is the seam that says "the pass has begun": it is read
	// for the first time as the pass reports for the first time, so ageing the
	// marker there makes everything the pass does NEXT — its first scan above all
	// — behave as if it took the whole steal window.
	//
	// That the marker is already fresh at this read is checked rather than
	// assumed, and it is the whole reason this stands in for a scan: if a clock
	// read is ever added ahead of the pass's first report, what is aged here would
	// be the queue in front of the pass instead, and this suite would quietly stop
	// covering the scan at all.
	scanned := false
	slowScan := func() time.Time {
		if !scanned {
			scanned = true
			if age := markerAge(t, e); age > time.Minute {
				t.Fatalf("the pass's first clock read found a marker last moved %v ago, want one just written — nothing had reported yet, so ageing it here simulates the queue ahead of the pass rather than its first scan", age)
			}
			ageMarkerPastTheStealWindow(t, e)
		}
		return time.Now()
	}

	var stealDuringFirstEmbed error
	embedder := &probingEmbedder{Embedder: delegate, probe: func() {
		stealDuringFirstEmbed = e.Store.ClaimAndEnqueueReembedding(ctx,
			search.ReembedClaim{Run: ids.NewV7(), TargetIdentity: identity, StealAfter: time.Hour},
			func(pgx.Tx) error { return nil })
	}}

	pass := search.ReembedPass{Run: working.Run, Identity: identity, Now: slowScan}
	if err := e.Store.ReembedWorkspace(ctx, pass, ws, embedder); err != nil {
		t.Fatalf("the healthy pass: %v", err)
	}
	if !embedder.probed {
		t.Fatal("the pass embedded nothing, so the assertion below was never reached — the fixture owes this workspace at least one live entity")
	}
	if !errors.Is(stealDuringFirstEmbed, search.ErrReembeddingInFlight) {
		t.Fatalf("a forced confirm during the pass's FIRST embed = %v, want ErrReembeddingInFlight — a run that had scanned but not yet finished an entity was dispossessed, so two children now re-embed the same corpus at once", stealDuringFirstEmbed)
	}
}

// TestAForcedClaimTakesTheMarkerOffARunThatStoppedMoving is the recovery
// affordance. The release is not airtight and cannot be — a workspace job
// declares Timeout() == -1, which puts it outside River's rescuer at any age, so
// a child whose process died leaves a running row nothing ever retries or
// discards and a workspace that never leaves the set — and the marker would then
// be held forever, every confirm answering 409 with no job anywhere left to
// explain why. An ordinary confirm must still be refused, so the escape hatch
// cannot be mistaken for the normal path.
func TestAForcedClaimTakesTheMarkerOffARunThatStoppedMoving(t *testing.T) {
	e := SetupSearch(t)
	ctx := context.Background()
	const identity = "fake/stuck@1024"
	if err := e.Store.SeedBinding(ctx, identity); err != nil {
		t.Fatalf("SeedBinding: %v", err)
	}
	stuck := claimOf(identity)
	if err := e.Store.ClaimAndEnqueueReembedding(ctx, stuck, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("claiming the run that will wedge: %v", err)
	}
	if err := e.Store.SeedReembeddingFleet(ctx, stuck.Run,
		[]ids.WorkspaceID{ids.From[ids.WorkspaceKind](e.WS)}, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("seeding the wedged run: %v", err)
	}

	// The child was killed outright: its workspace never leaves the set, and the
	// marker has not moved since.
	ageMarkerPastTheStealWindow(t, e)

	if err := e.Store.ClaimAndEnqueueReembedding(ctx, claimOf(identity), func(pgx.Tx) error { return nil }); !errors.Is(err, search.ErrReembeddingInFlight) {
		t.Fatalf("an ordinary confirm over a stale marker = %v, want ErrReembeddingInFlight — stealing must be something a human asked for", err)
	}

	taking := search.ReembedClaim{Run: ids.NewV7(), TargetIdentity: identity, StealAfter: time.Hour}
	if err := e.Store.ClaimAndEnqueueReembedding(ctx, taking, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("a forced confirm over a marker nothing is moving: %v", err)
	}
	// The taken-over marker belongs to the new run outright: the wedged run's
	// pending set is gone, so its own straggler's BOOKKEEPING moves nothing.
	// It says nothing about that straggler's embedding work, which a steal does
	// not stop (search.ReembedClaim.StealAfter).
	if err := e.Store.FinishWorkspaceReembedding(ctx, stuck.Run, ids.From[ids.WorkspaceKind](e.WS)); err != nil {
		t.Fatalf("the dispossessed run's straggler must be a no-op, got: %v", err)
	}
	_, status, _, err := e.Store.PopulatedIdentity(ctx)
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if status != "reembedding" {
		t.Fatalf("marker status = %q, want reembedding — the run that took the marker still holds it", status)
	}

	// A run that IS moving keeps its marker, however old the wedged one was.
	if err := e.Store.ClaimAndEnqueueReembedding(ctx,
		search.ReembedClaim{Run: ids.NewV7(), TargetIdentity: identity, StealAfter: time.Hour},
		func(pgx.Tx) error { return nil }); !errors.Is(err, search.ErrReembeddingInFlight) {
		t.Fatalf("a forced confirm over a freshly claimed marker = %v, want ErrReembeddingInFlight", err)
	}
}
