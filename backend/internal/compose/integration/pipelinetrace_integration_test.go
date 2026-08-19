// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The ladder assembled through the REAL wiring, from both doors.
//
// The assembler crosses two modules that may not import each other, so the edge
// it depends on is one compose injects. A test that built its own assembler over
// hand-made stores would be testing a copy of that wiring rather than the wiring
// — which is the whole reason these live here rather than beside either module.
//
// Every deny arm below carries its own control over the SAME seed, read by a
// caller who IS allowed. Two absences assert nothing on their own: an empty
// ladder satisfies "no activity id" and "cannot tell" perfectly well, so a test
// checking only those passes just as happily when the read returns nothing.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/pipelinetrace"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	trace "github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func ladderAssembler(e *Env, payloads bool) *pipelinetrace.Assembler {
	return pipelinetrace.NewAssembler(capture.NewTraceStore(e.DB()), e.Activities, payloads)
}

// seededMessage is one captured message: the activity, and the trace row the
// pipeline would have written to explain it.
type seededMessage struct {
	activityID ids.UUID
	traceID    ids.UUID
	// counterparty is the address BOTH the activity and the trace row carry, as
	// the sink writes it. Returned rather than re-derived at a call site: a
	// hand-copied copy of the template below is a join key that can drift from
	// the row it is meant to find, and a test asserting an absence would then
	// stay green for the wrong reason.
	counterparty string
}

// seedTracedMessage writes the activity directly and the trace row THROUGH THE
// PRODUCTION WRITER.
//
// capture.Trace is what the pipeline runs, and it is where the stage gate, the
// channel-id hashing, the payload posture and the erased-subject suppression all
// live. A hand-written INSERT bypasses every one of them, and a reader asserted
// against a row no writer could produce proves nothing about the reader.
func seedTracedMessage(t *testing.T, e *Env, owner ids.UUID, in seed) seededMessage {
	t.Helper()
	activityID := ids.NewV7()
	// Per-seed, so two tests in this file cannot couple through the disposition
	// ledger: its LATERAL join and the classifier's undecided-sender probe both
	// key on this address.
	counterparty := "dana-" + in.trace.SourceID + "@client.io"
	e.WsExec(t, `
		INSERT INTO activity (id, kind, occurred_at, source, captured_by, counterparty_email, subject, channel_provider, source_system, source_id)
		VALUES ($1, $2, now(), $3, $4, $5, 'Q3 pricing', nullif($6, ''), $3, $7)`,
		activityID, in.kind, in.trace.SourceSystem,
		// The provenance the sink stamps when a granting human exists
		// (connectorProvenance): connector:<CONNECTOR NAME>:<user>. The connector
		// name, not the source system — they coincide for gmail and diverge for a
		// channel unit, whose records say `telegram` over `dispact`.
		"connector:"+in.trace.Connector+":"+owner.String(),
		counterparty, in.channelProvider, in.trace.SourceID)

	entry := in.trace
	entry.UserID, entry.ActivityID = owner, activityID
	if entry.Counterparty != "" {
		// One address on both rows, as the sink writes it.
		entry.Counterparty = counterparty
	}
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := e.DB().Tx(ctx, func(tx pgx.Tx) error {
		return capture.Trace(ctx, tx, entry, entry.Counterparty != "")
	}); err != nil {
		t.Fatalf("seeding the trace through the real writer: %v", err)
	}
	return seededMessage{
		activityID:   activityID,
		traceID:      traceIDFor(t, e, in.trace.SourceID),
		counterparty: counterparty,
	}
}

// seed is one message to write. The activity's SHAPE is declared rather than
// inferred from the connector's name: a helper that guessed `kind` by string
// equality would silently seed a mail activity beside a chat trace for every
// channel it had not been told about.
type seed struct {
	trace           capture.TraceEntry
	kind            string
	channelProvider string
}

// capturedMail is the ordinary shape: an email the tier ladder let through.
func capturedMail(sourceID string) seed {
	return seed{kind: "email", trace: capture.TraceEntry{
		Stage: trace.StageTierLadder, Outcome: capture.TraceCaptured,
		Connector: "gmail", SourceSystem: "gmail", SourceID: sourceID,
	}}
}

// capturedChat is the same, carried by a channel unit: the transport is the
// channel provider and the source system is the unit that polled it, which is
// the split traceConnector documents.
func capturedChat(sourceID, provider, unit string) seed {
	return seed{kind: "message", channelProvider: provider, trace: capture.TraceEntry{
		Stage: trace.StageTierLadder, Outcome: capture.TraceCaptured,
		Connector: provider, SourceSystem: unit, SourceID: sourceID,
	}}
}

func traceIDFor(t *testing.T, e *Env, sourceID string) ids.UUID {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	var id ids.UUID
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id FROM capture_trace
			 WHERE source_id = $1
			 ORDER BY occurred_at LIMIT 1`, sourceID).Scan(&id)
	}); err != nil {
		t.Fatalf("reading back the trace id for %s: %v", sourceID, err)
	}
	return id
}

func TestTheLadderAnswersFromBothDoorsForTheSameMessage(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)
	msg := seedTracedMessage(t, e, e.Rep1, capturedMail("both-doors"))

	a := ladderAssembler(e, false)
	byActivity, err := a.ByActivityID(ctx, msg.activityID)
	if err != nil {
		t.Fatalf("the activity door refused: %v", err)
	}
	byTrace, err := a.ByTraceID(ctx, msg.traceID)
	if err != nil {
		t.Fatalf("the trace door refused: %v", err)
	}

	// THE ALLOW ARM the withholding test below is measured against: a reader who
	// may open the activity gets its id and a ladder that answers.
	if byTrace.ActivityID == nil || *byTrace.ActivityID != msg.activityID {
		t.Fatalf("activity_id = %v, want %v for a caller who may open it",
			byTrace.ActivityID, msg.activityID)
	}
	if got := rungFor(t, byTrace, trace.StageTierLadder); got.Status != trace.StatusDone {
		t.Errorf("tier-ladder status = %q, want done — the stored rung is theirs to read", got.Status)
	}

	// Every registered stage appears. A ladder returning only the stages it had
	// rows for leaves a reader unable to tell which of the missing steps
	// mattered, which is the silence this surface exists to remove.
	// Fatal, not Errorf: the loop below indexes byActivity by byTrace's length,
	// so a short ladder — exactly what this check reports — would panic rather
	// than fail.
	if len(byActivity.Rungs) != len(trace.Registrations()) {
		t.Fatalf("activity door returned %d rungs, want one per registered stage (%d)",
			len(byActivity.Rungs), len(trace.Registrations()))
	}
	// Rung-by-rung, not a count: both ladders are built by the same loop over
	// the registry, so their LENGTHS are equal by construction and comparing
	// them cannot fail. Their CONTENT can — force `owned: false` on one door and
	// its stored rungs go blank while the other's answer in full.
	for i, want := range byTrace.Rungs {
		got := byActivity.Rungs[i]
		if got.Stage != want.Stage || got.Status != want.Status || got.Reason != want.Reason {
			t.Errorf("the doors disagree on %s: activity %q/%q vs trace %q/%q",
				want.Stage, got.Status, got.Reason, want.Status, want.Reason)
		}
	}
	if got := rungFor(t, byActivity, trace.StageActivityWrite); got.Status != trace.StatusDone {
		t.Errorf("activity-write status = %q, want done — the activity exists", got.Status)
	}
	// An eligible email, so the classifier WOULD read it. Paired with the chat
	// case below, this is what proves `transport_not_read` discriminates rather
	// than being whatever the code always says.
	if got := rungFor(t, byActivity, trace.StageAttentionLabel); got.Reason != trace.ReasonAwaitingBatch {
		t.Errorf("attention reason = %q, want awaiting_batch for an eligible email", got.Reason)
	}
}

func TestAChatTransportIsToldWhyTheClassifierSkippedIt(t *testing.T) {
	// activities owns the exclusion table and proves it per-reason; the rung
	// unit test proves the pass-through. What THIS adds is that the composed
	// wiring carries the module's answer end to end rather than the assembler
	// re-deciding it.
	e := Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)
	msg := seedTracedMessage(t, e, e.Rep1, capturedChat("chat-msg", "telegram", "dispact"))
	got, err := ladderAssembler(e, false).ByActivityID(ctx, msg.activityID)
	if err != nil {
		t.Fatalf("reading the ladder: %v", err)
	}
	label := rungFor(t, got, trace.StageAttentionLabel)
	if label.Status != trace.StatusSkipped || label.Reason != trace.ReasonTransportNotRead {
		t.Errorf("attention rung = %q/%q, want skipped/transport_not_read",
			label.Status, label.Reason)
	}
}

func TestTheTraceDoorWithholdsAnActivityOutsideTheReadersRowScope(t *testing.T) {
	// The regression this surface shipped once: the window read strips the link
	// for an activity the caller cannot reach, and the drawer opened from that
	// same row must strip it too.
	//
	// The refusal has to be a ROW-SCOPE miss. Every seeded role holds `activity`
	// read, so an absent object grant is a seat that cannot exist — and it
	// refuses in auth.Require before any SQL, which is not the half that breaks
	// silently. Two things are therefore load-bearing in this seed: the activity
	// is LINKED only to a person another rep captured privately (an unlinked
	// activity is workspace-shared and visible at every scope, and a person
	// who is merely owned by somebody else is readable by every seat), and the
	// reader is at TEAM scope (an unbounded reader makes the scope clause
	// empty). Remove either and the row-scope half of the gate can be deleted
	// with nothing red.
	e := Setup(t)
	owner := OwnerConn(t)
	msg := seedTracedMessage(t, e, e.Rep1, capturedMail("hidden-activity"))
	theirPerson := e.SeedPerson(t, "Out Of Reach", &e.Rep3)
	e.MakeCapturePrivate(t, "person", theirPerson, e.Rep3)
	LinkActivity(t, owner, msg.activityID, "person", theirPerson)

	narrow := e.As(e.Rep1, []ids.UUID{e.Team1}, AccountRepPerms)
	got, err := ladderAssembler(e, false).ByTraceID(narrow, msg.traceID)
	if err != nil {
		t.Fatalf("the trace door refused a row the caller owns: %v", err)
	}
	if got.ActivityID != nil {
		t.Errorf("activity_id = %v travelled to a caller whose row scope excludes it", got.ActivityID)
	}
	// The stored rungs still answer — they describe the caller's own message —
	// which is also what proves the ladder was not simply empty.
	if stored := rungFor(t, got, trace.StageTierLadder); stored.Status != trace.StatusDone {
		t.Errorf("tier-ladder status = %q, want done: the caller owns this row", stored.Status)
	}
	// And nothing derived from the activity may be reported, including a claim
	// that the write never happened — which would contradict the rung above it.
	write := rungFor(t, got, trace.StageActivityWrite)
	if write.Status != trace.StatusUnknown || write.Reason != trace.ReasonRecordNotAvailable {
		t.Errorf("activity-write rung = %q/%q, want unknown/record_not_available",
			write.Status, write.Reason)
	}
}

func TestTheActivityDoorRefusesAMessageOutsideTheReadersRowScope(t *testing.T) {
	// The gate is taken FIRST, so a caller who may not open the message never
	// reaches the trace store at all.
	//
	// ErrNotFound, pinned rather than "some error": the row-scope miss hides
	// existence, and if it ever started answering ErrPermissionDenied the door
	// would confirm that an out-of-scope message exists — while a test accepting
	// any error stayed green through the change.
	e := Setup(t)
	owner := OwnerConn(t)
	msg := seedTracedMessage(t, e, e.Rep1, capturedMail("out-of-scope"))
	theirPerson := e.SeedPerson(t, "Out Of Reach", &e.Rep3)
	e.MakeCapturePrivate(t, "person", theirPerson, e.Rep3)
	LinkActivity(t, owner, msg.activityID, "person", theirPerson)
	narrow := e.As(e.Rep1, []ids.UUID{e.Team1}, AccountRepPerms)

	_, err := ladderAssembler(e, false).ByActivityID(narrow, msg.activityID)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound — any error would otherwise pass, "+
			"including a nil dereference", err)
	}
	// The control, over the SAME seed: the owner of the linked private person
	// reads it. Without this, an activity that was never seeded — or a link
	// that landed nowhere — answers ErrNotFound too, and the refusal above
	// would prove only that the fixture failed.
	reaches := e.As(e.Rep3, []ids.UUID{e.Team2}, AccountRepPerms)
	if _, err := ladderAssembler(e, false).ByActivityID(reaches, msg.activityID); err != nil {
		t.Fatalf("the owner of the linked person could not read it either: %v — "+
			"the seed did not land, so the refusal above proves nothing", err)
	}
}

func TestThePostureFlagTravelsButDoesNotGateTheRung(t *testing.T) {
	// The posture reaches the assembled ladder, and enforcement is NOT here:
	// wireRung applies it at the wire (see pipelinetrace/handlers_test.go). The
	// off-arm below is what keeps this test honest — without it the counterparty
	// assertion reads as proof of a gate this layer does not have.
	e := Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)
	withPayload := capturedMail("posture")
	// Any non-empty value turns the payload posture on in the helper, which then
	// writes the address the activity carries — one address on both rows, as the
	// sink does it.
	withPayload.trace.Counterparty, withPayload.trace.Subject = "seed", "Q3 pricing"
	msg := seedTracedMessage(t, e, e.Rep1, withPayload)

	on, err := ladderAssembler(e, true).ByTraceID(ctx, msg.traceID)
	if err != nil {
		t.Fatalf("reading the ladder: %v", err)
	}
	off, err := ladderAssembler(e, false).ByTraceID(ctx, msg.traceID)
	if err != nil {
		t.Fatalf("reading the ladder: %v", err)
	}
	if !on.PayloadsEnabled || off.PayloadsEnabled {
		t.Errorf("PayloadsEnabled = %v / %v, want true / false", on.PayloadsEnabled, off.PayloadsEnabled)
	}
	if got := rungFor(t, on, trace.StageTierLadder).Counterparty; got != msg.counterparty {
		t.Errorf("counterparty under the payload posture = %q, want %q", got, msg.counterparty)
	}
	if got := rungFor(t, off, trace.StageTierLadder).Counterparty; got != msg.counterparty {
		t.Errorf("counterparty with the flag off = %q — this layer carries what the row "+
			"held either way, and wireRung is where the posture bites", got)
	}
}

func TestAColleagueReadingASharedRecordGetsNoneOfTheOwnersMailboxRungs(t *testing.T) {
	// The boundary 0258 makes categorical: a capture_trace row is the member's
	// alone and NO grant widens it. A colleague may read what the pipeline did
	// to the RECORD; what one member's connection recorded about their own
	// mailbox stays theirs.
	//
	// The activity here is deliberately UNLINKED, which makes it
	// workspace-shared — so every seat in the installation can open it and the
	// activity door admits them. That is the realistic shape of the leak: the
	// door opens, and the only thing keeping the owner's rungs out of the answer
	// is the `user_id` predicate in anchorByActivityID. Nothing pinned that.
	//
	// Payloads ON is load-bearing. With the posture off, wireRung masks the
	// content at the wire and this assertion would pass straight over a leak.
	e := Setup(t)
	withPayload := capturedMail("colleague-read")
	withPayload.trace.Counterparty, withPayload.trace.Subject = "seed", "Q3 pricing"
	msg := seedTracedMessage(t, e, e.Rep1, withPayload)

	colleague := e.As(e.Rep2, []ids.UUID{e.Team1}, AccountRepPerms)
	got, err := ladderAssembler(e, true).ByActivityID(colleague, msg.activityID)
	if err != nil {
		t.Fatalf("a colleague could not open a workspace-shared activity: %v — "+
			"the seed did not land, so the assertions below prove nothing", err)
	}

	// The allow arm, inside the same read: a DERIVED rung still answers, because
	// it is ordinary product state. Without this an empty ladder would satisfy
	// everything below it.
	if write := rungFor(t, got, trace.StageActivityWrite); write.Status != trace.StatusDone {
		t.Errorf("activity-write = %q, want done: the colleague may read the record", write.Status)
	}
	// And the stored rung — the owner's — tells them nothing at all.
	stored := rungFor(t, got, trace.StageTierLadder)
	if stored.Status != trace.StatusUnknown || stored.Reason != trace.ReasonRecordNotAvailable {
		t.Errorf("tier-ladder = %q/%q for a colleague, want unknown/record_not_available",
			stored.Status, stored.Reason)
	}
	if stored.Counterparty != "" || stored.Subject != "" {
		t.Errorf("another member's mailbox content reached a colleague: %q / %q",
			stored.Counterparty, stored.Subject)
	}
}

// rungFor fails rather than returning a zero Rung: a missing rung silently
// compared against a status constant is one registry edit away from passing.
func rungFor(t *testing.T, l pipelinetrace.Ladder, stage trace.Stage) pipelinetrace.Rung {
	t.Helper()
	for _, r := range l.Rungs {
		if r.Stage == stage {
			return r
		}
	}
	t.Fatalf("no rung for %s in a ladder of %d", stage, len(l.Rungs))
	return pipelinetrace.Rung{}
}
