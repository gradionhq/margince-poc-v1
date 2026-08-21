// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The transcript-reading door: the three routes, what each answers, and the
// refusals a client can actually provoke.
//
// What one READING does — the citation, the staging, the zero writes before a
// human confirms — is proven against the engine in
// compose/transcriptpropose_integration_test.go. What can only be shown here is
// that a real client reaches all of it, that a request needing no model still
// answers, and that the 202 hands back a handle the same client can poll.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// transcriptDoorApp stands the composed application up with the reading
// transport wired. The api role only ever INSERTS the job — the worker reads
// the transcript — so an insert-only runner is the whole dependency, and these
// tests exercise the door without a model lane anywhere.
func transcriptDoorApp(t *testing.T, slug, name, email string) *apptest.AppEnv {
	t.Helper()
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if appDSN == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	// A separate pool from the one SetupAppWithOptions opens: the inserter
	// needs SOME pool reaching the same Postgres, not the same object.
	wirePool, err := database.NewPool(context.Background(), appDSN)
	if err != nil {
		t.Fatalf("opening the insert-only wiring pool: %v", err)
	}
	t.Cleanup(wirePool.Close)
	inserter, err := jobs.NewInserter(wirePool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("jobs.NewInserter: %v", err)
	}
	e := apptest.SetupAppWithOptions(t, compose.WithTranscriptRead(inserter))
	// The door enqueues in the same transaction as the run record, so the job
	// schema has to exist before the FIRST request, not merely before a worker.
	ApplyRiverSchema(t)
	e.Slug = slug
	apptest.BootstrapWorkspaceSession(t, e, name, email, "Ada Admin")
	return e
}

// transcriptText is already in ADR-0058's canonical form; the commitment is on
// line 3, which is what a proposal read out of it would cite.
const transcriptText = "Dana: Thanks for walking us through the rollout plan.\n" +
	"Priya: Any concerns from the security side?\n" +
	"Priya: I'll send the revised pricing over by Friday.\n" +
	"Dana: Perfect, we'll review it then."

type startedRead struct {
	ReadID string `json:"read_id"`
	Status string `json:"status"`
}

type readReport struct {
	ReadID       string   `json:"read_id"`
	ActivityID   string   `json:"activity_id"`
	Status       string   `json:"status"`
	StatusDetail *string  `json:"status_detail"`
	LineCount    int      `json:"line_count"`
	ProposalIDs  []string `json:"proposal_ids"`
}

// logTranscript puts a meeting transcript on the timeline through the real
// write path, so the body under test is the one logActivity normalizes.
func logTranscript(t *testing.T, e *apptest.AppEnv, body string) string {
	t.Helper()
	var activity struct {
		ID string `json:"id"`
	}
	status := e.Call(t, "POST", "/v1/activities", apptest.AnyMap{
		"kind": "meeting", "subject": "Rollout call", "body": body,
		"source_system": "transcript", "source": "ui",
	}, nil, &activity)
	if status != http.StatusCreated {
		t.Fatalf("logging the transcript → %d", status)
	}
	return activity.ID
}

func TestTheTranscriptReadingDoorQueuesAndThenReportsOnItself(t *testing.T) {
	e := transcriptDoorApp(t, "transcript-door", "Transcript Door", "ada@transcript.test")
	activityID := logTranscript(t, e, transcriptText)

	var started startedRead
	if status := e.Call(t, "POST", "/v1/activities/"+activityID+"/transcript-proposals", nil, nil, &started); status != http.StatusAccepted {
		t.Fatalf("starting a reading → %d, want 202: a model call cannot happen inside the request that asks for it", status)
	}
	if started.ReadID == "" {
		t.Fatal("the 202 must hand back a read id, or the client has nothing to poll")
	}
	if started.Status != "queued" && started.Status != "running" {
		t.Errorf("a fresh reading is live, got %q", started.Status)
	}

	// Pressing again joins rather than paying for the same transcript twice.
	var second startedRead
	if status := e.Call(t, "POST", "/v1/activities/"+activityID+"/transcript-proposals", nil, nil, &second); status != http.StatusAccepted {
		t.Fatalf("asking again → %d", status)
	}
	if second.ReadID != started.ReadID {
		t.Errorf("a second press must join the reading in flight; got %s, want %s", second.ReadID, started.ReadID)
	}

	var report readReport
	if status := e.Call(t, "GET", "/v1/activities/"+activityID+"/transcript-proposals/"+started.ReadID, nil, nil, &report); status != http.StatusOK {
		t.Fatalf("polling the reading → %d", status)
	}
	if report.ReadID != started.ReadID || report.ActivityID != activityID {
		t.Errorf("the report must describe the reading that was started: %+v", report)
	}
	if report.LineCount != 4 {
		t.Errorf("the report must say how much was read; want 4 lines, got %d", report.LineCount)
	}
	if report.ProposalIDs == nil {
		t.Error("the proposal list must be concrete, so 'found nothing' is an account rather than a null")
	}

	// The tab that started it is gone; the reading must still be findable.
	var latest readReport
	if status := e.Call(t, "GET", "/v1/activities/"+activityID+"/transcript-proposals/latest", nil, nil, &latest); status != http.StatusOK {
		t.Fatalf("asking for the latest reading → %d", status)
	}
	if latest.ReadID != started.ReadID {
		t.Errorf("latest must be the reading just started; got %s", latest.ReadID)
	}
}

// Asking for a reading of something nobody has read answers 404, not an empty
// report — the honest difference between never tried and tried and got nothing.
//
// The activity here is a plain meeting note rather than a transcript, because a
// transcript no longer HAS this state: one that lands with source_system
// 'transcript' starts its reading in the same transaction as the write, so
// "logged but never read" stopped being reachable through this door. The
// property is still worth holding — it is about what `latest` says when there
// is nothing to report, and a note is now the way to get there.
func TestAnActivityNobodyHasReadIsNotFoundRatherThanEmpty(t *testing.T) {
	e := transcriptDoorApp(t, "transcript-unread", "Transcript Unread", "ada@unread.test")

	var activity struct {
		ID string `json:"id"`
	}
	status := e.Call(t, "POST", "/v1/activities", apptest.AnyMap{
		"kind": "meeting", "subject": "Rollout call", "body": transcriptText,
		"source": "manual",
	}, nil, &activity)
	if status != http.StatusCreated {
		t.Fatalf("logging the note → %d", status)
	}

	if status := e.Call(t, "GET", "/v1/activities/"+activity.ID+"/transcript-proposals/latest", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("an activity nobody has read → %d, want 404 — the honest difference between never tried and tried and got nothing", status)
	}
}

// A transcript posted to POST /v1/activities is read WITHOUT anyone pressing
// anything.
//
// This is the door the first cut missed. WithTranscriptRead wired the reading
// onto the tool surface only, so a transcript arriving over REST was stored and
// silently never read — the same silence the feature exists to end, on the door
// a rep's integration actually posts to.
func TestATranscriptPostedOverRESTIsReadWithoutBeingAsked(t *testing.T) {
	e := transcriptDoorApp(t, "transcript-rest", "Transcript REST", "ada@rest.test")
	activityID := logTranscript(t, e, transcriptText)

	var latest readReport
	if status := e.Call(t, "GET", "/v1/activities/"+activityID+"/transcript-proposals/latest",
		nil, nil, &latest); status != http.StatusOK {
		t.Fatalf("asking for the reading of a transcript that landed over REST → %d, "+
			"want 200 — landing must start one, with nobody pressing anything", status)
	}
	if latest.ActivityID != activityID {
		t.Errorf("the reading describes %s, want the transcript that just landed (%s)",
			latest.ActivityID, activityID)
	}
}

func TestTheDoorRefusesWhatHasNoLinesToCite(t *testing.T) {
	e := transcriptDoorApp(t, "transcript-refusals", "Transcript Refusals", "ada@refuse.test")

	var note struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/activities", apptest.AnyMap{
		"kind": "note", "subject": "An ordinary note", "body": "No transcript here.", "source": "ui",
	}, nil, &note); status != http.StatusCreated {
		t.Fatalf("logging the note → %d", status)
	}

	var problem struct {
		Detail string `json:"detail"`
	}
	status := e.Call(t, "POST", "/v1/activities/"+note.ID+"/transcript-proposals", nil, nil, &problem)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("reading an activity with no transcript → %d, want 422", status)
	}
	if !strings.Contains(problem.Detail, "transcript") {
		t.Errorf("the refusal must say what is wrong in the caller's terms, got %q", problem.Detail)
	}

	longBody := strings.TrimSuffix(strings.Repeat("Dana: we went round this again.\n", 700), "\n")
	longID := logTranscript(t, e, longBody)
	var tooLong struct {
		Detail string `json:"detail"`
	}
	if status := e.Call(t, "POST", "/v1/activities/"+longID+"/transcript-proposals", nil, nil, &tooLong); status != http.StatusUnprocessableEntity {
		t.Fatalf("reading a transcript past the reading bound → %d, want 422 at the door", status)
	}
	if !strings.Contains(tooLong.Detail, "more than one transcript") {
		t.Errorf("the refusal must say what to do about it, got %q", tooLong.Detail)
	}
}

func TestAReadingOfATranscriptThatDoesNotExistIsNotFound(t *testing.T) {
	e := transcriptDoorApp(t, "transcript-missing", "Transcript Missing", "ada@missing.test")

	missing := "00000000-0000-7000-8000-000000000000"
	if status := e.Call(t, "POST", "/v1/activities/"+missing+"/transcript-proposals", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("reading a transcript that does not exist → %d, want 404", status)
	}
}
