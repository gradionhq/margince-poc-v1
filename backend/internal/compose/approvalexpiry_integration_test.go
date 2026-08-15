// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Expiry as a written decision, against a real database.
//
// The reading has always been right — a stale staging displays as expired. What
// was missing is everything that follows from it: the row still said pending, so
// nothing was audited, nothing was emitted, and an automation parked behind it
// waited on a verdict already taken against it.
//
// These run here rather than in the approvals package because the invariants
// are cross-boundary: the audit row and the outbox event are written by the
// store, and the run transition is carried by a consumer in another module. A
// unit test with a fake engine would prove the call and skip the guarantee.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// expiryCtx binds the system actor the sweep runs under. The pass writes audit
// rows and events, and both need a principal — but nobody decided any of this,
// so it is the clock's id rather than a person's.
func expiryCtx(e *integration.Env) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: approvals.ExpiryActor,
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// stageThenAge stages one approval and backdates its window so it is due.
//
// Backdating rather than waiting: the clock is the predicate under test, and a
// test that slept for a real TTL would be both slow and a lie about what it
// proves. The owner connection does the update because expires_at is not a
// column any app-role path may set after the fact.
func stageThenAge(t *testing.T, e *integration.Env, svc *approvals.Service, kind string, age time.Duration) ids.ApprovalID {
	t.Helper()
	id, err := svc.Stage(e.Admin(), approvals.StageInput{
		Kind:           kind,
		ProposedChange: json.RawMessage(`{"note":"nobody will answer this"}`),
		DiffHash:       "expiry-" + ids.NewV7().String(),
		Summary:        "a staging nobody decided",
	})
	if err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		e.WsExec(t, `UPDATE approval SET expires_at = now() - $2::interval WHERE id = $1`,
			id, age.String())
	}
	return id
}

func TestAnUnactionedStagingIsWrittenExpiredWithASystemActor(t *testing.T) {
	e := integration.Setup(t)
	svc := approvals.NewService(e.DB())
	id := stageThenAge(t, e, svc, "advance_deal", time.Hour)

	expired, err := svc.ExpireDue(expiryCtx(e))
	if err != nil {
		t.Fatalf("ExpireDue → %v", err)
	}
	if len(expired) != 1 || expired[0].ID != id {
		t.Fatalf("ExpireDue returned %v, want exactly the staged approval", expired)
	}

	// The STATUS, not the reading. Before this the row stayed 'pending' forever
	// and only displayed as expired, which is why nothing downstream could act.
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE id = $1 AND status = 'expired'`, id); n != 1 {
		t.Error("the approval still does not say expired — expiry is a reading again, not a decision")
	}
	// decided_by stays NULL: nobody decided this, and a human's id here would
	// put their name on a refusal they never made.
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE id = $1 AND decided_by IS NULL`, id); n != 1 {
		t.Error("the expiry named a deciding human")
	}
	// The audit row APPR-AC-2 asks for, attributed to the clock.
	if n := e.WsCount(t, `SELECT count(*) FROM audit_log
		WHERE entity_id = $1 AND action = 'expire' AND actor_id = $2`, id, approvals.ExpiryActor); n != 1 {
		t.Error("no system-actor audit row for the expiry — an auto-rejection nobody can find")
	}
	// And the event, in the same transaction, carrying the decision vocabulary.
	if n := e.WsCount(t, `SELECT count(*) FROM event_outbox
		WHERE envelope->>'type' = 'approval.decided'
		  AND envelope->'payload'->>'verdict' = 'expired'`); n != 1 {
		t.Error("no approval.decided/expired event — nothing downstream can learn the window closed")
	}
}

// The sweep must not touch what is still live. This is the assertion that fails
// if the predicate is ever loosened into "everything pending".
func TestAStagingInsideItsWindowIsLeftAlone(t *testing.T) {
	e := integration.Setup(t)
	svc := approvals.NewService(e.DB())
	id := stageThenAge(t, e, svc, "advance_deal", 0)

	expired, err := svc.ExpireDue(expiryCtx(e))
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 0 {
		t.Fatalf("ExpireDue decided %d live stagings", len(expired))
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE id = $1 AND status = 'pending'`, id); n != 1 {
		t.Error("a staging inside its window was expired")
	}
}

// A kind exempt from expiry stays pending however old it is. The exemption is a
// property of the SUBJECT — a held message waits until somebody answers — and a
// sweep that reaped it would recreate the silent stop the card exists to
// prevent.
func TestAKindThatNeverExpiresSurvivesTheSweep(t *testing.T) {
	e := integration.Setup(t)
	if !approvals.ExpiresNever(approvals.KindScheduledSendHeld) {
		t.Fatal("held scheduled sends are no longer exempt from expiry — the sweep will now reap a message whose subject is still waiting on it, and this test would have skipped rather than said so")
	}
	svc := approvals.NewService(e.DB())
	id := stageThenAge(t, e, svc, approvals.KindScheduledSendHeld, 30*24*time.Hour)

	if _, err := svc.ExpireDue(expiryCtx(e)); err != nil {
		t.Fatal(err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE id = $1 AND status = 'pending'`, id); n != 1 {
		t.Error("a non-expiring staging was swept — the message it is about now waits with nothing asking about it")
	}
}

// Expiring twice would audit twice, and an audit trail that reports one event
// as two is worse than one that reports none.
func TestASweptStagingIsNotSweptAgain(t *testing.T) {
	e := integration.Setup(t)
	svc := approvals.NewService(e.DB())
	id := stageThenAge(t, e, svc, "advance_deal", time.Hour)

	if _, err := svc.ExpireDue(expiryCtx(e)); err != nil {
		t.Fatal(err)
	}
	second, err := svc.ExpireDue(expiryCtx(e))
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Errorf("the second sweep decided %d already-expired stagings", len(second))
	}
	if n := e.WsCount(t, `SELECT count(*) FROM audit_log
		WHERE entity_id = $1 AND action = 'expire'`, id); n != 1 {
		t.Errorf("expire audit rows = %d, want exactly 1", n)
	}
}

// A human deciding under the wire wins. The sweep re-reads under the row lock,
// so a decision taken between the scan and the write is left exactly as its
// decider left it — the clock loses that race, which is the right way round.
func TestAHumanDecisionUnderTheWireBeatsTheSweep(t *testing.T) {
	e := integration.Setup(t)
	svc := approvals.NewService(e.DB())
	id := stageThenAge(t, e, svc, "advance_deal", time.Hour)

	// Decided directly in SQL rather than through Decide: the point is the
	// sweep's re-read under the lock, and Decide would refuse an already-expired
	// row on its own (effectiveStatus), which is a different guard.
	e.WsExec(t, `UPDATE approval SET status = 'rejected', decided_at = now() WHERE id = $1`, id)

	if _, err := svc.ExpireDue(expiryCtx(e)); err != nil {
		t.Fatal(err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE id = $1 AND status = 'rejected'`, id); n != 1 {
		t.Error("the sweep overwrote a decision somebody had already taken")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE entity_id = $1 AND action = 'expire'`, id); n != 0 {
		t.Error("the sweep audited an expiry for a row it did not expire")
	}
}

// The per-item override APPR-PARAM-1 pins beside the default, proven by the
// only thing that can distinguish them: a window that closes when the item says
// it does rather than when the kind's default would.
func TestAStagingsOwnWindowOverridesTheDefault(t *testing.T) {
	e := integration.Setup(t)
	svc := approvals.NewService(e.DB())
	short := time.Second
	id, err := svc.Stage(e.Admin(), approvals.StageInput{
		Kind:           "advance_deal",
		ProposedChange: json.RawMessage(`{"note":"stale fast"}`),
		DiffHash:       "ttl-" + ids.NewV7().String(),
		Summary:        "a staging with its own short window",
		TTL:            &short,
	})
	if err != nil {
		t.Fatal(err)
	}

	var expires, created time.Time
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT expires_at, created_at FROM approval WHERE id = $1`, id).Scan(&expires, &created); err != nil {
		t.Fatal(err)
	}
	// Under a minute proves the item's own second-scale window was used; the
	// kind's default is measured in days, so the two cannot be confused.
	if gap := expires.Sub(created); gap > time.Minute {
		t.Errorf("expires_at is %v after creation, want the staging's own short window — the per-item override was ignored", gap)
	}
}

// The default itself, which is the half a wrong constant hides. 72h is what
// APPR-PARAM-1 pins, and the three days are what carry a Friday-afternoon
// staging to a Monday-morning inbox.
func TestTheDefaultWindowIsThreeDays(t *testing.T) {
	e := integration.Setup(t)
	svc := approvals.NewService(e.DB())
	id := stageThenAge(t, e, svc, "advance_deal", 0)

	var expires, created time.Time
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT expires_at, created_at FROM approval WHERE id = $1`, id).Scan(&expires, &created); err != nil {
		t.Fatal(err)
	}
	gap := expires.Sub(created)
	// A range rather than an equality: the two timestamps are taken from
	// different clocks (the service's and the database's), so a strict compare
	// would flake on their skew while telling us nothing more.
	if gap < 71*time.Hour || gap > 73*time.Hour {
		t.Errorf("default window is %v, want ~72h — at 24h a staging raised on Friday afternoon auto-rejects before Monday", gap)
	}
}

// The sweep is the clock's alone.
//
// ExpireDue decides approvals in bulk without consulting any human's row scope,
// so leaving it open would give an authenticated user a way to refuse every
// pending approval in the installation at once — each one audited as though the
// clock had done it, with their name nowhere in the record.
func TestOnlyTheClockMayExpireApprovals(t *testing.T) {
	e := integration.Setup(t)
	svc := approvals.NewService(e.DB())
	id := stageThenAge(t, e, svc, "advance_deal", time.Hour)

	// An admin: the most authority a human has, and still not this.
	if _, err := svc.ExpireDue(e.Admin()); err == nil {
		t.Fatal("an admin expired every due approval in the installation")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE id = $1 AND status = 'pending'`, id); n != 1 {
		t.Error("a human's refused sweep decided the approval anyway")
	}

	// A system principal that is not the sweep is refused too: the audit rows
	// this writes are attributed to ExpiryActor, so any other id would be
	// writing decisions under a name that is not its own.
	impostor := principal.WithActor(principal.WithWorkspaceID(context.Background(), e.WS),
		principal.Principal{Type: principal.PrincipalSystem, ID: "system:something-else"})
	if _, err := svc.ExpireDue(principal.WithCorrelationID(impostor, ids.NewV7())); err == nil {
		t.Error("a system principal that is not the expiry sweep expired approvals under the sweep's name")
	}

	// And the sweep itself still works — a guard that refuses everyone is not a
	// guard, it is an outage.
	expired, err := svc.ExpireDue(expiryCtx(e))
	if err != nil {
		t.Fatalf("the sweep itself was refused: %v", err)
	}
	if len(expired) != 1 {
		t.Errorf("the sweep expired %d approvals, want 1", len(expired))
	}
}
