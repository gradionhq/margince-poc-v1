// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Reading a meeting transcript for the next steps in it, over real migrated
// Postgres (S-E04.3, MEET-AC-3/4).
//
// The four things that have to be true: a proposal appears staged citing the
// transcript lines it was read from; NOTHING is written to the timeline before
// a human confirms (GATE-AI-2); confirming creates the task exactly once, even
// when the decision is driven twice; and a transcript stating no next steps
// produces no proposal rather than a guess (GATE-AI-1).

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// transcriptBody is already in ADR-0058's canonical form, which is what the
// write path stores: the commitment is on line 3.
const transcriptBody = "Dana: Thanks for walking us through the rollout plan.\n" +
	"Priya: Any concerns from the security side?\n" +
	"Priya: I'll send the revised pricing over by Friday.\n" +
	"Dana: Perfect, we'll review it then."

// transcriptPerms is a rep who may create activities and read the timeline —
// exactly what confirming a next step needs and no more.
var transcriptPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"activity":              {Create: true, Read: true, Update: true},
		"deal":                  {Read: true},
		"pipeline":              {Read: true},
		"installation_settings": {Read: true},
	},
	RowScope: principal.RowScopeTeam,
}

// cannedBrain answers with one fixed reply, so a test states what the model
// said rather than depending on one having been called.
type cannedBrain struct {
	reply string
	err   error
}

func (b cannedBrain) Complete(context.Context, model.Request) (model.Response, error) {
	if b.err != nil {
		return model.Response{}, b.err
	}
	return model.Response{Text: b.reply}, nil
}

// groundedReply cites line 3, which is where the commitment actually is.
func groundedReply(t *testing.T, line int, confidence float64) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"proposals": []map[string]any{{
		"summary": "Send the revised pricing", "owner": "Priya",
		"source_lines": []int{line}, "confidence": confidence,
	}}})
	if err != nil {
		t.Fatalf("building the model reply: %v", err)
	}
	return string(raw)
}

type transcriptEnv struct {
	*integration.Env
	owner    *pgx.Conn
	svc      *approvals.Service
	ctx      context.Context
	activity ids.ActivityID
}

func setupTranscript(t *testing.T) *transcriptEnv {
	t.Helper()
	e := &transcriptEnv{Env: integration.Setup(t), owner: integration.OwnerConn(t)}
	e.ctx = e.As(e.Rep1, []ids.UUID{e.Team1}, transcriptPerms)
	e.svc = approvals.NewService(e.DB())
	e.svc.WithEffect(TranscriptProposalKind, transcriptProposalEffect(e.svc, e.Activities))

	subject := "Rollout call"
	sourceSystem := "transcript"
	body := transcriptBody
	activity, _, err := e.Activities.LogActivity(e.ctx, activities.LogActivityInput{
		Kind: "meeting", Subject: &subject, Body: &body,
		SourceSystem: &sourceSystem, Source: "ui",
	})
	if err != nil {
		t.Fatalf("logging the transcript: %v", err)
	}
	e.activity = ids.From[ids.ActivityKind](ids.UUID(activity.Id))
	return e
}

// read drives one whole reading the way the worker does.
func (e *transcriptEnv) read(t *testing.T, brain completer) activities.TranscriptRead {
	t.Helper()
	started, _, err := e.Activities.StartTranscriptReadQueued(e.ctx, e.activity, "human:"+e.Rep1.String(), nil)
	if err != nil {
		t.Fatalf("starting the reading: %v", err)
	}
	quiet := slog.New(slog.NewTextHandler(os.Stderr, nil))
	proposer := NewTranscriptProposer(e.Pool, brain, e.svc, time.Now, quiet)
	if err := proposer.Read(e.ctx, e.Activities, started.ID, e.activity); err != nil {
		t.Fatalf("reading the transcript: %v", err)
	}
	done, err := e.Activities.GetTranscriptRead(e.ctx, e.activity, started.ID)
	if err != nil {
		t.Fatalf("reading back the run record: %v", err)
	}
	return done
}

func (e *transcriptEnv) taskCount(t *testing.T) int {
	t.Helper()
	return e.WsCount(t, `SELECT count(*) FROM activity WHERE kind = 'task'`)
}

func TestATranscriptReadingStagesACitedProposalAndWritesNothingYet(t *testing.T) {
	e := setupTranscript(t)
	before := e.taskCount(t)

	read := e.read(t, cannedBrain{reply: groundedReply(t, 3, 0.9)})

	if read.Status != activities.TranscriptReadDone {
		t.Fatalf("the reading must finish done, got %s (%v)", read.Status, read.StatusDetail)
	}
	if len(read.ProposalIDs) != 1 {
		t.Fatalf("want one staged proposal, got %d", len(read.ProposalIDs))
	}
	if read.LineCount != 4 {
		t.Errorf("the report must say how many lines were addressed; want 4, got %d", read.LineCount)
	}

	// GATE-AI-2: nothing on the timeline until a human confirms.
	if after := e.taskCount(t); after != before {
		t.Errorf("staging must write no task activity; task count went %d → %d", before, after)
	}

	staged, err := e.svc.Get(e.ctx, ids.From[ids.ApprovalKind](read.ProposalIDs[0]))
	if err != nil {
		t.Fatalf("reading back the staged proposal: %v", err)
	}
	if staged.Kind != TranscriptProposalKind {
		t.Errorf("want kind %q, got %q", TranscriptProposalKind, staged.Kind)
	}
	evidence := storedEvidence(t, staged.Evidence)
	if len(evidence) != 1 {
		t.Fatalf("the proposal must carry the evidence it was read from, got %d elements", len(evidence))
	}
	if len(evidence[0].SourceLines) != 1 || evidence[0].SourceLines[0] != 3 {
		t.Errorf("the proposal must cite the transcript line it was read from, got %v", evidence[0].SourceLines)
	}
	if !strings.Contains(evidence[0].Snippet, "revised pricing") {
		t.Errorf("the evidence must quote the transcript's own line, got %q", evidence[0].Snippet)
	}
	if evidence[0].SourceID != e.activity.String() {
		t.Errorf("the evidence must point back at the transcript activity, got %q", evidence[0].SourceID)
	}
}

// storedEvidence reads the approval's persisted evidence. The test asserts on
// what was WRITTEN rather than on the wire mapping, which the approvals module
// owns and covers itself.
func storedEvidence(t *testing.T, raw json.RawMessage) []struct {
	Snippet     string `json:"evidence_snippet"`
	SourceType  string `json:"source_type"`
	SourceID    string `json:"source_id"`
	SourceLines []int  `json:"source_lines"`
} {
	t.Helper()
	var out []struct {
		Snippet     string `json:"evidence_snippet"`
		SourceType  string `json:"source_type"`
		SourceID    string `json:"source_id"`
		SourceLines []int  `json:"source_lines"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the persisted evidence is not readable JSON: %v", err)
	}
	return out
}

func TestConfirmingATranscriptProposalCreatesTheTaskExactlyOnce(t *testing.T) {
	e := setupTranscript(t)
	read := e.read(t, cannedBrain{reply: groundedReply(t, 3, 0.9)})
	approvalID := ids.From[ids.ApprovalKind](read.ProposalIDs[0])
	before := e.taskCount(t)

	if _, err := e.svc.Decide(e.ctx, approvalID, true, nil); err != nil {
		t.Fatalf("approving the proposal: %v", err)
	}
	if after := e.taskCount(t); after != before+1 {
		t.Fatalf("confirming must create exactly one task; count went %d → %d", before, after)
	}

	// A second decision on the same proposal is refused rather than replayed:
	// the staged row is the authority object and it has already been spent.
	if _, err := e.svc.Decide(e.ctx, approvalID, true, nil); err == nil {
		t.Error("a decided proposal must not be decidable twice")
	}
	if after := e.taskCount(t); after != before+1 {
		t.Errorf("a re-driven decision must create nothing more; count is now %d", after)
	}

	subject := e.wsString(t, `SELECT subject FROM activity WHERE kind = 'task' ORDER BY created_at DESC LIMIT 1`)
	if subject != "Send the revised pricing" {
		t.Errorf("the created task must carry what was promised, got %q", subject)
	}
	body := e.wsString(t, `SELECT body FROM activity WHERE kind = 'task' ORDER BY created_at DESC LIMIT 1`)
	if !strings.Contains(body, "Priya") || !strings.Contains(body, "line 3") {
		t.Errorf("the task must carry provenance back to who promised it and where, got %q", body)
	}
}

func TestRejectingATranscriptProposalCreatesNothing(t *testing.T) {
	rejectReason := "not a commitment"
	e := setupTranscript(t)
	read := e.read(t, cannedBrain{reply: groundedReply(t, 3, 0.9)})
	before := e.taskCount(t)

	if _, err := e.svc.Decide(e.ctx, ids.From[ids.ApprovalKind](read.ProposalIDs[0]), false, &rejectReason); err != nil {
		t.Fatalf("rejecting the proposal: %v", err)
	}
	if after := e.taskCount(t); after != before {
		t.Errorf("rejecting must create nothing; task count went %d → %d", before, after)
	}
}

func TestATranscriptStatingNothingProducesNoProposalAndSaysSo(t *testing.T) {
	e := setupTranscript(t)

	read := e.read(t, cannedBrain{reply: `{"proposals":[]}`})

	if read.Status != activities.TranscriptReadDone {
		t.Fatalf("finding nothing is a CORRECT reading, not a failure; got %s", read.Status)
	}
	if len(read.ProposalIDs) != 0 {
		t.Errorf("a transcript stating nothing must stage nothing, got %d proposals", len(read.ProposalIDs))
	}
	if read.StatusDetail == nil || *read.StatusDetail == "" {
		t.Error("an empty result must explain itself, or it reads as a broken feature")
	}
}

func TestAReadingThatCitesALineThatDoesNotExistStagesNothing(t *testing.T) {
	e := setupTranscript(t)

	// Line 99 of a four-line transcript: the citation cannot be checked, so
	// the whole reading is refused rather than staged with a false pointer.
	read := e.read(t, cannedBrain{reply: groundedReply(t, 99, 0.95)})

	if read.Status != activities.TranscriptReadFailed {
		t.Fatalf("an ungroundable reading must fail the read, got %s", read.Status)
	}
	if len(read.ProposalIDs) != 0 {
		t.Errorf("a refused reading must stage nothing, got %d proposals", len(read.ProposalIDs))
	}
	if read.StatusDetail == nil || !strings.Contains(*read.StatusDetail, "read again") {
		t.Errorf("the failure must tell the rep what they can do, got %+v", read.StatusDetail)
	}
}

func TestAnUnsureReadingIsDroppedRatherThanPutInFrontOfAHuman(t *testing.T) {
	e := setupTranscript(t)

	read := e.read(t, cannedBrain{reply: groundedReply(t, 3, transcriptConfidenceFloor-0.2)})

	if read.Status != activities.TranscriptReadDone {
		t.Fatalf("a below-floor reading is well-formed, so the read completes; got %s", read.Status)
	}
	if len(read.ProposalIDs) != 0 {
		t.Errorf("a reading below the confidence floor must not cost a human's attention, got %d proposals",
			len(read.ProposalIDs))
	}
}

func TestPressingReadTwiceJoinsTheReadingAlreadyInFlight(t *testing.T) {
	e := setupTranscript(t)

	first, joinedFirst, err := e.Activities.StartTranscriptReadQueued(e.ctx, e.activity, "human:"+e.Rep1.String(), nil)
	if err != nil {
		t.Fatalf("starting the first reading: %v", err)
	}
	if joinedFirst {
		t.Fatal("the first start creates a reading, it does not join one")
	}
	second, joinedSecond, err := e.Activities.StartTranscriptReadQueued(e.ctx, e.activity, "human:"+e.Rep1.String(), nil)
	if err != nil {
		t.Fatalf("starting the second reading: %v", err)
	}
	if !joinedSecond || second.ID != first.ID {
		t.Errorf("pressing the button twice must join the reading in flight, got %s vs %s (joined=%v)",
			second.ID, first.ID, joinedSecond)
	}
}

func TestAnActivityThatCarriesNoTranscriptCannotBeRead(t *testing.T) {
	e := setupTranscript(t)
	subject := "An ordinary note"
	body := "No transcript here."
	note, _, err := e.Activities.LogActivity(e.ctx, activities.LogActivityInput{
		Kind: "note", Subject: &subject, Body: &body, Source: "ui",
	})
	if err != nil {
		t.Fatalf("logging the note: %v", err)
	}

	_, _, err = e.Activities.StartTranscriptReadQueued(
		e.ctx, ids.From[ids.ActivityKind](ids.UUID(note.Id)), "human:"+e.Rep1.String(), nil)
	if err == nil {
		t.Fatal("an activity with no transcript has no lines to cite and must be refused")
	}
	var notTranscript *activities.NotATranscriptError
	if !errors.As(err, &notTranscript) {
		t.Errorf("want the not-a-transcript refusal, got %v", err)
	}
}

// wsString reads one text value under this workspace's binding.
func (e *transcriptEnv) wsString(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var out string
	if err := e.DB().Tx(principal.WithWorkspaceID(context.Background(), e.WS), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), sql, args...).Scan(&out)
	}); err != nil {
		t.Fatalf("reading %q: %v", sql, err)
	}
	return out
}
