// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// fakeReindexEmbedder stands in for the embed lane so the pass gets past its
// lane guard and reaches the store. It is never called: the store's first
// statement fails before any entity is read, which is the point of these tests.
type fakeReindexEmbedder struct{}

func (fakeReindexEmbedder) Embed(context.Context, model.EmbedRequest) (model.Embeddings, error) {
	return model.Embeddings{}, errors.New("compose: the reindex tests never reach an embed call")
}

func (fakeReindexEmbedder) EmbedIdentity() (string, int) { return "fake/held@1024", 1024 }

// unreachableStore is a store over a pool that has been closed: every statement
// it issues fails the way an outage fails, without a database to arrange one.
func unreachableStore(t *testing.T) *search.Store {
	t.Helper()
	// The DSN is never dialled — pgxpool connects lazily and this pool is closed
	// before the first statement — but it must parse, so the failure under test
	// is the closed pool and not a malformed config.
	pool, err := pgxpool.New(context.Background(), "postgres://unused:unused@127.0.0.1:1/unused")
	if err != nil {
		t.Fatalf("building the pool this test then closes: %v", err)
	}
	pool.Close()
	return search.NewStore(InstallationDB(pool))
}

// TestEmbedReindexWorkerWithNoEmbedLaneFailsInsteadOfSittingQueued pins the
// guard that runs before any store access. A worker role started without
// --ai-routing has no embed lane, and a row that answered nil there would report
// a rebuild that never happened — leaving the marker held by a run that looks
// finished.
func TestEmbedReindexWorkerWithNoEmbedLaneFailsInsteadOfSittingQueued(t *testing.T) {
	w := &embedReindexWorker{store: unreachableStore(t), embedder: nil}
	err := w.Work(context.Background(), &river.Job[EmbedReindexArgs]{
		// Not the last attempt: the release is the last attempt's business, so
		// this exercises the guard and nothing after it.
		JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: 25},
		Args:   EmbedReindexArgs{Run: ids.NewV7(), Identity: "fake/none@1024"},
	})
	if err == nil {
		t.Fatal("a pass with no embed lane answered nil — the row completes and the rebuild it reports never happened")
	}
	// Not cancelled: the lane is an operator's to configure, and a cancelled row
	// stops asking for it.
	var cancel *river.JobCancelError
	if errors.As(err, &cancel) {
		t.Error("a missing embed lane was cancelled rather than failed")
	}
	// Work redacts through jobs.FaultContext, so the actionable text is asserted
	// where it is produced. An operator who reads only the row still has to be
	// told which flag fixes this.
	if lane := w.reembed(context.Background(), EmbedReindexArgs{}); lane == nil ||
		!strings.Contains(lane.Error(), "--ai-routing") {
		t.Errorf("the lane guard says %v, want it to name the flag that fixes it", lane)
	}
}

// TestEmbedReindexWorkerReportsAMarkerItCouldNotHandBack pins the worst ending.
// On the last attempt the run gives the marker back before returning its error,
// because River discards a row that has run out and no hook can retry it. When
// that write ALSO fails the marker stays held, and the only way back is a forced
// confirm's steal — so both failures have to reach the job row, or the operator
// reads an outage where they should read a stuck marker.
func TestEmbedReindexWorkerReportsAMarkerItCouldNotHandBack(t *testing.T) {
	w := &embedReindexWorker{store: unreachableStore(t), embedder: fakeReindexEmbedder{}}
	err := w.Work(context.Background(), &river.Job[EmbedReindexArgs]{
		JobRow: &rivertype.JobRow{Attempt: 3, MaxAttempts: 3},
		Args:   EmbedReindexArgs{Run: ids.NewV7(), Identity: "fake/held@1024"},
	})
	if err == nil {
		t.Fatal("a pass that failed AND could not hand the marker back answered nil — the marker is held with nothing recording why")
	}
	// A failure, not a cancellation: this row has run out of attempts and River
	// discards it, which is a different thing from a defect it declined to
	// retry, and only one of the two tells an operator to go and steal the
	// marker back.
	var cancel *river.JobCancelError
	if errors.As(err, &cancel) {
		t.Error("a release the outage prevented was reported as a cancellation")
	}
	// Both halves reach the log through the joined error: the pass failure says
	// what went wrong, the release failure says what it left behind.
	joined, ok := errors.Unwrap(err).(interface{ Unwrap() []error })
	if !ok || len(joined.Unwrap()) != 2 {
		t.Errorf("the fault carries %v, want the pass failure joined to the release failure — either alone omits the held marker or its cause", err)
	}
}
