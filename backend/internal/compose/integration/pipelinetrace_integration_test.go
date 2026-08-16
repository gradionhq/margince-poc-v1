// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The ladder assembled through the REAL wiring, from both doors.
//
// The assembler crosses two modules and neither may import the other, so the
// edge it depends on is one compose injects — a test that built its own
// assembler over hand-made stores would be testing a copy of the wiring rather
// than the wiring.
//
// The claim under test in every case is the same: what a rung says must be true
// of the reader looking at it. The failures that matter here are not crashes,
// they are a rung stating a fact about somebody else's mailbox.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/pipelinetrace"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	trace "github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// traceReaderPerms holds the activity grant the ladder's derived rungs need.
var traceReaderPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects:  map[string]principal.ObjectGrant{"activity": {Read: true}},
	RowScope: principal.RowScopeAll,
}

// noActivityGrant is the same member with no `activity` object at all — the
// caller who owns their trace rows and may not open what they point at.
var noActivityGrant = principal.Permissions{
	RoleKeys: []string{"rep"},
	RowScope: principal.RowScopeAll,
}

func ladderAssembler(e *Env, payloads bool) *pipelinetrace.Assembler {
	db := database.BindTo(e.Pool, ids.From[ids.WorkspaceKind](e.WS))
	return pipelinetrace.NewAssembler(
		capture.NewTraceStore(db), activities.NewStore(db), payloads)
}

// seedTracedMessage writes one activity and the trace row explaining it.
func seedTracedMessage(t *testing.T, e *Env, owner ids.UUID, sourceID string) ids.UUID {
	t.Helper()
	activityID := ids.NewV7()
	e.WsExec(t, `
		INSERT INTO activity (id, workspace_id, kind, occurred_at, source, captured_by,
		                      counterparty_email, subject)
		VALUES ($1, $2, 'email', now(), 'gmail', 'connector:gmail', $3, 'Q3 pricing')`,
		activityID, e.WS, "dana-"+sourceID+"@client.io")
	e.WsExec(t, `
		INSERT INTO capture_trace (workspace_id, user_id, connector, source_system, source_id,
		                           stage, outcome, activity_id, occurred_at)
		VALUES ($1, $2, 'gmail', 'gmail', $3, 'tier_ladder', 'captured', $4, now())`,
		e.WS, owner, sourceID, activityID)
	return activityID
}

func traceIDFor(t *testing.T, e *Env, sourceID string) ids.UUID {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	var id ids.UUID
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id FROM capture_trace WHERE source_id = $1 LIMIT 1`, sourceID).Scan(&id)
	}); err != nil {
		t.Fatalf("reading back the trace id for %s: %v", sourceID, err)
	}
	return id
}

func TestTheLadderAnswersFromBothDoorsForTheSameMessage(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, traceReaderPerms)
	activityID := seedTracedMessage(t, e, e.Rep1, "both-doors")

	a := ladderAssembler(e, false)
	byActivity, err := a.ByActivityID(ctx, activityID)
	if err != nil {
		t.Fatalf("the activity door refused: %v", err)
	}
	byTrace, err := a.ByTraceID(ctx, traceIDFor(t, e, "both-doors"))
	if err != nil {
		t.Fatalf("the trace door refused: %v", err)
	}

	// Every registered stage appears, in order. A ladder returning only the
	// stages it had rows for would leave a reader unable to tell which of the
	// missing steps mattered — the silence this surface exists to remove.
	if len(byActivity.Rungs) != len(trace.Registrations()) {
		t.Errorf("activity door returned %d rungs, want one per registered stage (%d)",
			len(byActivity.Rungs), len(trace.Registrations()))
	}
	if len(byTrace.Rungs) != len(byActivity.Rungs) {
		t.Errorf("the two doors disagree on rung count: %d vs %d",
			len(byTrace.Rungs), len(byActivity.Rungs))
	}
	if got := rungFor(byActivity, trace.StageActivityWrite); got.Status != trace.StatusDone {
		t.Errorf("activity-write status = %q, want done — the activity exists", got.Status)
	}
	// The motivating rung: this is an EMAIL, so the classifier would read it,
	// and the honest answer is that the batch has not reached it.
	label := rungFor(byActivity, trace.StageAttentionLabel)
	if label.Reason != trace.ReasonAwaitingBatch {
		t.Errorf("attention reason = %q, want awaiting_batch for an eligible email", label.Reason)
	}
}

func TestAChatTransportIsToldWhyTheClassifierSkippedIt(t *testing.T) {
	// The question this whole surface was built to answer, through the real
	// assembler rather than a hand-built rung.
	e := Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, traceReaderPerms)
	activityID := ids.NewV7()
	e.WsExec(t, `
		INSERT INTO activity (id, workspace_id, kind, occurred_at, source, captured_by,
		                      counterparty_email, channel_provider)
		VALUES ($1, $2, 'message', now(), 'dispact', 'connector:dispact', $3, 'telegram')`,
		activityID, e.WS, "luu@dispact.example")
	e.WsExec(t, `
		INSERT INTO capture_trace (workspace_id, user_id, connector, source_system, source_id,
		                           stage, outcome, activity_id, occurred_at)
		VALUES ($1, $2, 'telegram', 'dispact', 'chat-msg', 'tier_ladder', 'captured', $3, now())`,
		e.WS, e.Rep1, activityID)

	got, err := ladderAssembler(e, false).ByActivityID(ctx, activityID)
	if err != nil {
		t.Fatalf("reading the ladder: %v", err)
	}
	label := rungFor(got, trace.StageAttentionLabel)
	if label.Status != trace.StatusSkipped {
		t.Errorf("attention status = %q, want skipped", label.Status)
	}
	if label.Reason != trace.ReasonTransportNotRead {
		t.Errorf("attention reason = %q, want transport_not_read", label.Reason)
	}
}

func TestTheTraceDoorWithholdsAnActivityTheReaderCannotOpen(t *testing.T) {
	// The disclosure this surface shipped with once: the window read strips the
	// link for an activity out of the caller's reach, and the drawer opened
	// from that same row must strip it too.
	e := Setup(t)
	seedTracedMessage(t, e, e.Rep1, "hidden-activity")
	narrow := e.As(e.Rep1, []ids.UUID{e.Team1}, noActivityGrant)

	got, err := ladderAssembler(e, false).ByTraceID(narrow, traceIDFor(t, e, "hidden-activity"))
	if err != nil {
		t.Fatalf("the trace door refused a row the caller owns: %v", err)
	}
	if got.ActivityID != nil {
		t.Errorf("activity_id = %v travelled to a caller who may not open it", got.ActivityID)
	}
	// And nothing derived from it may be reported — including a claim that the
	// write never happened, which would contradict the captured rung beside it.
	write := rungFor(got, trace.StageActivityWrite)
	if write.Status != trace.StatusUnknown || write.Reason != trace.ReasonRecordNotAvailable {
		t.Errorf("activity-write rung = %q/%q, want unknown/record_not_available",
			write.Status, write.Reason)
	}
}

func TestTheActivityDoorRefusesAMessageTheCallerCannotRead(t *testing.T) {
	e := Setup(t)
	activityID := seedTracedMessage(t, e, e.Rep1, "no-activity-grant")
	narrow := e.As(e.Rep1, []ids.UUID{e.Team1}, noActivityGrant)

	if _, err := ladderAssembler(e, false).ByActivityID(narrow, activityID); err == nil {
		t.Error("a caller with no activity grant read the ladder through the activity door")
	}
}

func TestThePayloadPostureReachesTheAssembledLadder(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, traceReaderPerms)
	seedTracedMessage(t, e, e.Rep1, "posture")
	e.WsExec(t, `UPDATE capture_trace SET counterparty = $1, subject = $2 WHERE source_id = 'posture'`,
		"dana@client.io", "Q3 pricing")

	got, err := ladderAssembler(e, true).ByTraceID(ctx, traceIDFor(t, e, "posture"))
	if err != nil {
		t.Fatalf("reading the ladder: %v", err)
	}
	if !got.PayloadsEnabled {
		t.Error("the posture did not reach the assembled ladder")
	}
	if rung := rungFor(got, trace.StageTierLadder); rung.Counterparty != "dana@client.io" {
		t.Errorf("the stored rung's counterparty = %q, want the row's", rung.Counterparty)
	}
}

func rungFor(l pipelinetrace.Ladder, stage trace.Stage) pipelinetrace.Rung {
	for _, r := range l.Rungs {
		if r.Stage == stage {
			return r
		}
	}
	return pipelinetrace.Rung{}
}
