// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// What happens to a disposition the engine will not decide for itself: the
// human-review lane over a real Postgres (ADR-0072/A118 §4). Every path out of
// `unsure` is here — a human accepts, a human declines, the row exhausts its
// attempts and is retired into the queue, nobody answers and it ages out, or a
// human answers just as the age-out sweep arrives. The one invariant across all
// of them is that a question gets asked once and closed once: the engine never
// re-offers a decided row, and never records an outcome the database
// contradicts.

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The accept branch: a human says yes, and the records capture withheld are
// created while the disposition closes as `real`, both on the redemption's
// transaction — the only path by which an `unsure` sender becomes a record.
func TestCounterpartyAcceptCreatesTheRecordsAndClosesTheDisposition(t *testing.T) {
	e := integration.Setup(t)
	activityID := seedCapturedMail(t, e, "dana@acceptco.example", "about your services")
	dispositionID := seedPendingDisposition(t, e, "dana@acceptco.example", "acceptco.example", activityID)
	retireToUnsure(t, e, dispositionID)

	svc := approvalsServiceWithEffects(e.Pool)
	engine := NewCounterpartyVerdictEngine(e.Pool, &scriptedVerdictBrain{}, slog.Default())
	if err := engine.StageReviewsWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), 0); err != nil {
		t.Fatalf("staging reviews: %v", err)
	}
	approvalID := stagedProposalID(t, e, dispositionID)

	// A REAL app_user with the create grants the mapping demands — the point
	// being that an actual human can decide this kind. (e.Admin() mints a
	// synthetic id, which the approval's decided_by foreign key rejects.)
	decider := e.As(e.Rep1, nil, integration.AdminPerms)
	if _, err := svc.Decide(decider, ids.From[ids.ApprovalKind](approvalID), true, nil); err != nil {
		t.Fatalf("approving the counterparty proposal: %v", err)
	}

	if n := countIn(t, e, `
		SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
		 WHERE pe.email = 'dana@acceptco.example'`); n != 1 {
		t.Fatalf("%d persons after accept, want 1 — accepting must create what capture withheld", n)
	}
	if got := dispositionStatus(t, e, dispositionID); got != capture.PendingStatusReal {
		t.Fatalf("disposition status after accept = %q, want real", got)
	}
	// Accepting ADDS; it never touches the message.
	if n := countIn(t, e, `SELECT count(*) FROM activity WHERE id = $1 AND archived_at IS NULL`, activityID); n != 1 {
		t.Fatal("accepting a counterparty proposal archived the message")
	}
}

// retireToUnsure puts a disposition in the state a terminal below-floor
// judgement leaves it in, without spending two scripted model calls to get there.
func retireToUnsure(t *testing.T, e *integration.Env, id ids.UUID) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			UPDATE capture_pending_counterparty
			   SET status = 'unsure', resolved_at = now(), next_attempt_at = NULL
			 WHERE id = $1`, id)
		return err
	})
	if err != nil {
		t.Fatalf("retiring the disposition: %v", err)
	}
}

func stagedProposalID(t *testing.T, e *integration.Env, dispositionID ids.UUID) ids.UUID {
	t.Helper()
	var id ids.UUID
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT proposal_id FROM capture_pending_counterparty WHERE id = $1`, dispositionID).Scan(&id)
	})
	if err != nil {
		t.Fatalf("reading the staged proposal id: %v", err)
	}
	return id
}

// A row that spends its attempts without ever getting an answer must reach a
// terminal state. ClaimDue refuses it at the bound, so without a retiring sweep
// it is stranded exactly where nobody looks: still `pending`, invisible to the
// review queue, and holding a slot against the deferral ceiling forever.
func TestAnExhaustedDispositionIsRetiredRatherThanStranded(t *testing.T) {
	e := integration.Setup(t)
	activityID := seedCapturedMail(t, e, "stuck@limbo.example", "hello")
	dispositionID := seedPendingDisposition(t, e, "stuck@limbo.example", "limbo.example", activityID)
	spendAttempts(t, e, dispositionID, capture.PendingMaxAttempts)

	engine := NewCounterpartyVerdictEngine(e.Pool, &scriptedVerdictBrain{}, slog.Default())
	if err := engine.ReconcileLedgerWorkspace(principal.WithWorkspaceID(context.Background(), e.WS)); err != nil {
		t.Fatalf("reconciling the ledger: %v", err)
	}

	if got := dispositionStatus(t, e, dispositionID); got != capture.PendingStatusUnsure {
		t.Fatalf("exhausted disposition = %q, want unsure — exhaustion must be terminal, not a dead end", got)
	}
	// And having reached `unsure`, it is now something a human can be offered.
	if err := engine.StageReviewsWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), 0); err != nil {
		t.Fatalf("staging reviews: %v", err)
	}
	if n := countIn(t, e, `SELECT count(*) FROM approval WHERE kind = 'capture_counterparty'`); n != 1 {
		t.Fatalf("%d proposals for a retired row, want 1", n)
	}
}

// A human's decline closes the question. The approvals engine has no reject
// hook, so the ledger reconciles against the approval row — without which the
// row stays `unsure`, gets re-staged on the next tick, and asks the same person
// the same question every hour forever while holding a cap slot.
func TestADeclinedReviewClosesTheDispositionInsteadOfReasking(t *testing.T) {
	e := integration.Setup(t)
	activityID := seedCapturedMail(t, e, "declined@maybe.example", "hi")
	dispositionID := seedPendingDisposition(t, e, "declined@maybe.example", "maybe.example", activityID)
	retireToUnsure(t, e, dispositionID)

	svc := approvalsServiceWithEffects(e.Pool)
	engine := NewCounterpartyVerdictEngine(e.Pool, &scriptedVerdictBrain{}, slog.Default())
	if err := engine.StageReviewsWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), 0); err != nil {
		t.Fatalf("staging reviews: %v", err)
	}
	approvalID := stagedProposalID(t, e, dispositionID)
	if _, err := svc.Decide(e.As(e.Rep1, nil, integration.AdminPerms),
		ids.From[ids.ApprovalKind](approvalID), false, nil); err != nil {
		t.Fatalf("declining the proposal: %v", err)
	}

	if err := engine.ReconcileLedgerWorkspace(principal.WithWorkspaceID(context.Background(), e.WS)); err != nil {
		t.Fatalf("reconciling the ledger: %v", err)
	}
	if got := dispositionStatus(t, e, dispositionID); got != capture.PendingStatusRejected {
		t.Fatalf("declined disposition = %q, want rejected — a decline must close the question", got)
	}
	// Declining is non-destructive: no records, and the mail stays put.
	if n := countIn(t, e, `SELECT count(*) FROM activity WHERE id = $1 AND archived_at IS NULL`, activityID); n != 1 {
		t.Fatal("declining a proposal hid the message")
	}

	// And the human is not asked again.
	if err := engine.StageReviewsWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), 0); err != nil {
		t.Fatalf("second staging pass: %v", err)
	}
	if n := countIn(t, e, `SELECT count(*) FROM approval WHERE kind = 'capture_counterparty'`); n != 1 {
		t.Fatalf("%d proposals after a decline, want 1 — a decided offer must never be re-staged", n)
	}
}

// spendAttempts drives a row to the attempt bound without running the model.
func spendAttempts(t *testing.T, e *integration.Env, id ids.UUID, attempts int) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE capture_pending_counterparty SET attempts = $2 WHERE id = $1`, id, attempts)
		return err
	})
	if err != nil {
		t.Fatalf("spending the attempts: %v", err)
	}
}

// A question nobody ever answers must stop being asked. A staged offer expires
// after a day and StageReviews honestly re-offers the row, so without an age-out
// an unanswered `unsure` cycles forever — holding a slot against the deferral
// ceiling and against its sender's address the whole time. That tail is what
// makes filling the ceiling worth an outsider's while.
func TestAnUnansweredReviewAgesOutAndTakesItsOfferWithIt(t *testing.T) {
	e := integration.Setup(t)
	activityID := seedCapturedMail(t, e, "ignored@maybe.example", "hi")
	dispositionID := seedPendingDisposition(t, e, "ignored@maybe.example", "maybe.example", activityID)
	retireToUnsure(t, e, dispositionID)

	engine := NewCounterpartyVerdictEngine(e.Pool, &scriptedVerdictBrain{}, slog.Default())
	if err := engine.StageReviewsWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), 0); err != nil {
		t.Fatalf("staging reviews: %v", err)
	}
	approvalID := stagedProposalID(t, e, dispositionID)

	// A window still open changes nothing: the question is young, and a
	// workspace that took a weekend off must not lose it.
	if err := engine.AgeOutStaleReviewsWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), capture.UnsureReviewWindow); err != nil {
		t.Fatalf("ageing out reviews: %v", err)
	}
	if got := dispositionStatus(t, e, dispositionID); got != capture.PendingStatusUnsure {
		t.Fatalf("disposition = %q inside its window, want it still waiting for a human", got)
	}

	// Now close the window by backdating when the row became `unsure`.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			UPDATE capture_pending_counterparty
			   SET resolved_at = now() - interval '31 days' WHERE id = $1`, dispositionID)
		return err
	}); err != nil {
		t.Fatalf("backdating the review: %v", err)
	}
	if err := engine.AgeOutStaleReviewsWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), capture.UnsureReviewWindow); err != nil {
		t.Fatalf("ageing out reviews: %v", err)
	}

	if got := dispositionStatus(t, e, dispositionID); got != capture.PendingStatusRejected {
		t.Fatalf("aged-out disposition = %q, want rejected — the ledger must stop asking", got)
	}
	// The offer goes with it. Leaving it in the inbox would mean an accept whose
	// records land while the ledger says the question was closed unanswered.
	if n := countIn(t, e, `
		SELECT count(*) FROM approval
		 WHERE id = $1 AND status = 'pending' AND expires_at > now()`, approvalID); n != 0 {
		t.Fatal("the aged-out question left a live offer standing in the review queue")
	}
	// Nothing was created and no mail was touched: ageing out is the same
	// non-destructive close a human's decline is.
	if n := countIn(t, e, `
		SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
		 WHERE pe.email = 'ignored@maybe.example'`); n != 0 {
		t.Fatal("ageing out a question created the person it stopped asking about")
	}
	if n := countIn(t, e, `
		SELECT count(*) FROM activity WHERE id = $1 AND archived_at IS NULL`, activityID); n != 1 {
		t.Fatal("ageing out a question touched the message that raised it")
	}
}

// A human deciding the offer between the stale-row scan and the age-out write
// must win. Losing that race would create the records AND record the question as
// closed unanswered — the ledger describing an outcome the database contradicts.
func TestAgeingOutLosesToAHumanWhoDecidedFirst(t *testing.T) {
	e := integration.Setup(t)
	activityID := seedCapturedMail(t, e, "decided@maybe.example", "hi")
	dispositionID := seedPendingDisposition(t, e, "decided@maybe.example", "maybe.example", activityID)
	retireToUnsure(t, e, dispositionID)

	svc := approvalsServiceWithEffects(e.Pool)
	engine := NewCounterpartyVerdictEngine(e.Pool, &scriptedVerdictBrain{}, slog.Default())
	if err := engine.StageReviewsWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), 0); err != nil {
		t.Fatalf("staging reviews: %v", err)
	}
	approvalID := stagedProposalID(t, e, dispositionID)
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			UPDATE capture_pending_counterparty
			   SET resolved_at = now() - interval '31 days' WHERE id = $1`, dispositionID)
		return err
	}); err != nil {
		t.Fatalf("backdating the review: %v", err)
	}

	// The human answers first — the row is stale by the clock, and decided.
	if _, err := svc.Decide(e.As(e.Rep1, nil, integration.AdminPerms),
		ids.From[ids.ApprovalKind](approvalID), true, nil); err != nil {
		t.Fatalf("approving the proposal: %v", err)
	}
	if err := engine.AgeOutStaleReviewsWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), capture.UnsureReviewWindow); err != nil {
		t.Fatalf("ageing out reviews: %v", err)
	}

	if got := dispositionStatus(t, e, dispositionID); got == capture.PendingStatusRejected {
		t.Fatal("the sweep overwrote a decision a human had already made")
	}
}
