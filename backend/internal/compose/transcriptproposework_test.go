// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The worker's own decisions, and the transport's answers when nothing is
// wired — both reachable without a database, because neither is about storage.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// recordingReadStore stands in for the run record. It is a true boundary — the
// database — so faking it here is the mock the craft rules allow, and every
// method records what it was asked so a test can assert on the DECISION rather
// than on call order.
type recordingReadStore struct {
	claimErr  error
	claimed   activities.TranscriptRead
	finished  []activities.TranscriptReadOutcome
	finishErr error
	lease     time.Duration
}

func (s *recordingReadStore) BeginTranscriptRead(
	_ context.Context, _ ids.UUID, reclaimAfter time.Duration,
) (activities.TranscriptRead, error) {
	s.lease = reclaimAfter
	return s.claimed, s.claimErr
}

func (s *recordingReadStore) ReadTranscript(context.Context, ids.ActivityID) (activities.TranscriptReading, error) {
	return activities.TranscriptReading{}, errors.New("not asked for in these tests")
}

func (s *recordingReadStore) FinishTranscriptRead(
	_ context.Context, _ ids.UUID, outcome activities.TranscriptReadOutcome,
) error {
	s.finished = append(s.finished, outcome)
	return s.finishErr
}

func transcriptJob(args TranscriptProposeArgs) *river.Job[TranscriptProposeArgs] {
	return &river.Job[TranscriptProposeArgs]{Args: args}
}

func TestAWorkerWithNoModelLaneFailsTheReadingInsteadOfLeavingItQueued(t *testing.T) {
	store := &recordingReadStore{}
	worker := &transcriptProposeWorker{activities: store, log: slog.New(slog.DiscardHandler)}

	err := worker.Work(context.Background(), transcriptJob(TranscriptProposeArgs{
		Workspace: ids.NewV7(), ActivityID: ids.NewV7(), TranscriptReadID: ids.NewV7(),
	}))
	if err != nil {
		t.Fatalf("declining a reading is an outcome, not a job fault: %v", err)
	}
	if len(store.finished) != 1 {
		t.Fatalf("the reading must be closed, or a rep watches a spinner nothing will ever move; got %d finishes", len(store.finished))
	}
	outcome := store.finished[0]
	if outcome.Status != activities.TranscriptReadFailed {
		t.Errorf("want the reading failed, got %q", outcome.Status)
	}
	if !strings.Contains(outcome.Detail, "no AI model configured") {
		t.Errorf("the detail must say what is missing so an operator can act: %q", outcome.Detail)
	}
}

func TestAWorkerClaimsWithTheLeaseTheDoorReArmsWith(t *testing.T) {
	store := &recordingReadStore{}
	worker := &transcriptProposeWorker{activities: store, log: slog.New(slog.DiscardHandler)}

	if err := worker.Work(context.Background(), transcriptJob(TranscriptProposeArgs{
		Workspace: ids.NewV7(), TranscriptReadID: ids.NewV7(),
	})); err != nil {
		t.Fatalf("working the job: %v", err)
	}
	if store.lease != activities.TranscriptReadLease {
		t.Errorf("the worker must claim with the one shared lease (%s), got %s — two numbers leave a reading nothing can reclaim",
			activities.TranscriptReadLease, store.lease)
	}
}

func TestAReadingAnotherWorkerHoldsIsLeftAlone(t *testing.T) {
	store := &recordingReadStore{claimErr: apperrors.ErrConflict}
	worker := &transcriptProposeWorker{activities: store, log: slog.New(slog.DiscardHandler)}

	err := worker.Work(context.Background(), transcriptJob(TranscriptProposeArgs{
		Workspace: ids.NewV7(), TranscriptReadID: ids.NewV7(),
	}))
	if err != nil {
		t.Fatalf("losing the claim is not a fault — the holder is working it: %v", err)
	}
	if len(store.finished) != 0 {
		t.Errorf("a reading somebody else holds must not be closed from here, got %d finishes", len(store.finished))
	}
}

func TestAWorkerRefusesArgsThatNameNoWorkspace(t *testing.T) {
	store := &recordingReadStore{}
	worker := &transcriptProposeWorker{activities: store, log: slog.New(slog.DiscardHandler)}

	err := worker.Work(context.Background(), transcriptJob(TranscriptProposeArgs{TranscriptReadID: ids.NewV7()}))
	if err == nil {
		t.Fatal("an empty workspace binds the GUC to nothing and reads whatever the connection carries")
	}
	if len(store.finished) != 0 {
		t.Error("the guard must refuse before touching the store")
	}
}

func TestTheTranscriptOperationsAnswer501UntilAJobRunnerIsWired(t *testing.T) {
	var unwired transcriptReadHandlers
	id := openapi_types.UUID(ids.NewV7())

	for _, tc := range []struct {
		name string
		call func(w http.ResponseWriter, r *http.Request)
	}{
		{"start", func(w http.ResponseWriter, r *http.Request) { unwired.ReadTranscriptForNextSteps(w, r, id) }},
		{"report", func(w http.ResponseWriter, r *http.Request) { unwired.GetTranscriptRead(w, r, id, id) }},
		{"latest", func(w http.ResponseWriter, r *http.Request) { unwired.GetLatestTranscriptRead(w, r, id) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("an unwired role must DECLARE the absence, not pretend to queue work nothing will pick up; got %d", rec.Code)
			}
		})
	}
}

func TestTheReportAlwaysAccountsForItsProposalsEvenWhenThereAreNone(t *testing.T) {
	read := activities.TranscriptRead{
		ID: ids.NewV7(), ActivityID: ids.From[ids.ActivityKind](ids.NewV7()),
		Status: activities.TranscriptReadDone, LineCount: 48,
	}
	report := transcriptReadReport(read)

	if report.ProposalIds == nil {
		t.Error("the proposal list must be concrete: a reading that found nothing is an explicit account of having found nothing, not a null")
	}
	if len(report.ProposalIds) != 0 {
		t.Errorf("want no proposals, got %d", len(report.ProposalIds))
	}
	if report.LineCount != 48 {
		t.Errorf("the report must carry how much was read, got %d", report.LineCount)
	}

	staged := ids.NewV7()
	read.ProposalIDs = []ids.UUID{staged}
	if got := transcriptReadReport(read); len(got.ProposalIds) != 1 || ids.UUID(got.ProposalIds[0]) != staged {
		t.Errorf("the staged proposals must reach the client so it can send the rep to them, got %+v", got.ProposalIds)
	}
}

func TestALiveReadingIsTheOneQuestionThePollAsks(t *testing.T) {
	for status, wantLive := range map[string]bool{
		activities.TranscriptReadQueued:  true,
		activities.TranscriptReadRunning: true,
		activities.TranscriptReadDone:    false,
		activities.TranscriptReadFailed:  false,
	} {
		if got := (activities.TranscriptRead{Status: status}).Live(); got != wantLive {
			t.Errorf("%s: Live() = %v, want %v", status, got, wantLive)
		}
	}
}

func TestAReadingIsAttributedToTheReaderNotToANeighbouringAgent(t *testing.T) {
	requester := ids.NewV7()
	ctx := withTranscriptReader(context.Background(), "human:"+requester.String(), ids.NewV7())

	actor, ok := principal.Actor(ctx)
	if !ok {
		t.Fatal("the reading must run as a named principal, or its writes are unattributable")
	}
	if actor.ID != transcriptProposalActor {
		t.Errorf("a transcript proposal must say the transcript reader read it, got %q — the inbox shows this to the person deciding", actor.ID)
	}
	if actor.OnBehalfOf != requester {
		t.Errorf("what the reading produces is owned by the human who asked; got %s, want %s", actor.OnBehalfOf, requester)
	}
}
