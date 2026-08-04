// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// How far a `noise` verdict reaches, and what it is allowed to destroy
// (ADR-0072/A118 §4). The verdict is evidence about ONE inbound message whose
// From header nobody authenticates, so its reach is drawn deliberately: wide
// enough to cover every message that sender already wrote, and no wider — never
// the workspace's own correspondence, never mail that arrives long afterwards.
// Destruction is narrower still than hiding: hiding is reversible and a reply
// releases it, so an uncorroborated verdict may hide forever but never redact.

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// A disposition covers the SENDER, so its effects cover every message that
// sender wrote — not just the one that happened to raise the question. The
// second and third mail from a stranger join the open question rather than
// raising their own, so an effect keyed on the ledger row's single activity_id
// would hide message #1 and leave the rest on the timeline with full bodies:
// "noise is not shown" defeated by sending two emails instead of one.
func TestANoiseVerdictHidesEveryMessageThatSenderWrote(t *testing.T) {
	e := integration.Setup(t)
	// All three carry List-Unsubscribe, so the redaction half below is reachable
	// too — that corroboration is per-message (migration 0137), while the
	// sender-wide reach this test exists to pin is per-address.
	first := seedBulkCapturedMail(t, e, "bulk@flood.example", "offer one")
	second := seedBulkCapturedMail(t, e, "bulk@flood.example", "offer two")
	third := seedBulkCapturedMail(t, e, "bulk@flood.example", "offer three")
	dispositionID := seedPendingDisposition(t, e, "bulk@flood.example", "flood.example", first)

	brain := &scriptedVerdictBrain{verdicts: map[string]string{dispositionID.String(): capture.PendingStatusNoise}}
	engine := NewCounterpartyVerdictEngine(e.Pool, brain, slog.Default())
	if err := engine.RunWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), 0); err != nil {
		t.Fatalf("verdict pass: %v", err)
	}

	for _, id := range []ids.UUID{first, second, third} {
		if n := countIn(t, e, `SELECT count(*) FROM activity WHERE id = $1 AND archived_at IS NOT NULL`, id); n != 1 {
			t.Fatalf("activity %s stayed visible — the verdict must cover the sender, not one message", id)
		}
	}

	for _, id := range []ids.UUID{first, second, third} {
		backdateArchive(t, e, id)
	}
	if err := engine.RedactNoiseWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), capture.NoiseUndoWindow, 0); err != nil {
		t.Fatalf("redaction sweep: %v", err)
	}
	if n := countIn(t, e, `
		SELECT count(*) FROM activity
		 WHERE counterparty_email = 'bulk@flood.example'
		   AND (subject IS NOT NULL OR body IS NOT NULL OR raw IS NOT NULL)`); n != 0 {
		t.Fatalf("%d of the sender's messages kept their content past the undo window", n)
	}
}

// counterparty_email comes from the message's own From header, which nobody
// authenticates. So an outsider can mail the connected mailbox claiming to be
// anyone — and if a noise verdict acted on "every message bearing this address",
// one forged message would hide and then destroy the workspace's real
// correspondence with whoever was named.
//
// The scope is therefore narrower than the address: inbound only, never
// provider-attested, never linked to a person, and it stops applying entirely
// once the workspace has written to that address.
func TestAForgedSenderCannotReachTheWorkspacesOwnCorrespondence(t *testing.T) {
	e := integration.Setup(t)
	victim := "bigcustomer@corp.example"

	// The workspace's genuine relationship with the named party: mail it sent,
	// and inbound mail the provider attested as part of that correspondence.
	ownSent := seedOutboundMail(t, e, victim, "our proposal")
	// The attacker's single forged message, which is what actually gets judged.
	forged := seedCapturedMail(t, e, victim, "🚀 buy followers now")
	dispositionID := seedPendingDisposition(t, e, victim, "corp.example", forged)

	brain := &scriptedVerdictBrain{verdicts: map[string]string{dispositionID.String(): capture.PendingStatusNoise}}
	engine := NewCounterpartyVerdictEngine(e.Pool, brain, slog.Default())
	if err := engine.RunWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), 0); err != nil {
		t.Fatalf("verdict pass: %v", err)
	}
	if err := engine.HideNoiseStragglersWorkspace(principal.WithWorkspaceID(context.Background(), e.WS)); err != nil {
		t.Fatalf("straggler sweep: %v", err)
	}

	// The workspace's own sent mail is untouched — a stranger's forged header
	// has no authority over the record the workspace made itself.
	if n := countIn(t, e, `SELECT count(*) FROM activity WHERE id = $1 AND archived_at IS NULL`, ownSent); n != 1 {
		t.Fatal("a forged From header hid the workspace's OWN outbound mail")
	}
	// Writing to a wrongly-hidden sender is the recovery path: correspondence is
	// the T1 signal that they are a counterparty, so the sweep lets go. Both
	// messages are archived and aged past the window first — otherwise the
	// assertions below would hold whatever the scope rule did.
	backdateArchive(t, e, forged)
	backdateArchive(t, e, ownSent)
	if err := engine.RedactNoiseWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), capture.NoiseUndoWindow, 0); err != nil {
		t.Fatalf("redaction sweep: %v", err)
	}
	if n := countIn(t, e, `SELECT count(*) FROM activity WHERE id = $1 AND body IS NOT NULL`, forged); n != 1 {
		t.Fatal("mail from an address the workspace corresponds with was redacted — replying must call the sweep off")
	}
	if n := countIn(t, e, `SELECT count(*) FROM activity WHERE id = $1 AND body IS NOT NULL`, ownSent); n != 1 {
		t.Fatal("the workspace's own outbound mail was redacted by a stranger's verdict")
	}
}

// seedOutboundMail inserts one message the workspace SENT, attested by the
// provider — the T1 correspondence evidence.
func seedOutboundMail(t *testing.T, e *integration.Env, to, subject string) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO activity (id, workspace_id, kind, subject, body, direction,
			                      source_system, source_id, source, captured_by,
			                      counterparty_email, counterparty_outbound_attested)
			VALUES ($1, $2, 'email', $3, 'our own words', 'outbound',
			        'gmail', $4, 'gmail:'||$4, 'connector:gmail', $5, true)`,
			id, e.WS, subject, "out-"+id.String(), to)
		return err
	})
	if err != nil {
		t.Fatalf("seeding outbound mail: %v", err)
	}
	return id
}

// The destruction is one transaction, so a failure cannot leave the activity
// stripped while the provider original survives. This drives the case that
// worried a reviewer: redact the activity's content by itself — the state a
// crash between two separate transactions would leave — and assert the sweep
// still collects it. A message whose original outlives its text is unfinished
// work, not finished work, and nothing else in the system would ever collect it.
func TestRedactionCollectsMailWhoseOriginalOutlivedItsText(t *testing.T) {
	e := integration.Setup(t)
	activityID := seedBulkCapturedMail(t, e, "loud@bulk.example", "offer")
	dispositionID := seedPendingDisposition(t, e, "loud@bulk.example", "bulk.example", activityID)

	brain := &scriptedVerdictBrain{verdicts: map[string]string{dispositionID.String(): capture.PendingStatusNoise}}
	engine := NewCounterpartyVerdictEngine(e.Pool, brain, slog.Default())
	if err := engine.RunWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), 0); err != nil {
		t.Fatalf("verdict pass: %v", err)
	}

	// Simulate the half-done state: content gone, original still held.
	stripActivityContent(t, e, activityID)
	if n := rawCaptureRows(t, e, activityID); n != 1 {
		t.Fatal("the fixture did not reproduce the half-done state")
	}

	backdateArchive(t, e, activityID)
	if err := engine.RedactNoiseWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), capture.NoiseUndoWindow, 0); err != nil {
		t.Fatalf("redaction sweep: %v", err)
	}
	if n := rawCaptureRows(t, e, activityID); n != 0 {
		t.Fatal("the sweep skipped mail whose original outlived its text — that original would be retained forever")
	}
}

// The forged-bulk attack, and the line that stops it costing somebody their
// mail: write a message as an address the workspace has never corresponded
// with, shape it to read as marketing, and the model's noise verdict lands on
// the ADDRESS. Hiding on that evidence is fine — it is reversible, and a reply
// releases it. Destroying on it is not, and the victim cannot object to a
// destruction they never saw coming, because the mail was hidden first.
//
// So an ordinary message — no List-Unsubscribe header, nothing corroborating
// that it is bulk — is hidden and stays hidden. It is never destroyed, however
// long the undo window runs out.
func TestANoiseVerdictWithoutBulkCorroborationHidesButNeverDestroys(t *testing.T) {
	e := integration.Setup(t)
	// seedCapturedMail, NOT seedBulkCapturedMail: an ordinary message.
	activityID := seedCapturedMail(t, e, "real.person@partner.example", "about the contract")
	dispositionID := seedPendingDisposition(t, e, "real.person@partner.example", "partner.example", activityID)

	brain := &scriptedVerdictBrain{verdicts: map[string]string{dispositionID.String(): capture.PendingStatusNoise}}
	engine := NewCounterpartyVerdictEngine(e.Pool, brain, slog.Default())
	if err := engine.RunWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), 0); err != nil {
		t.Fatalf("verdict pass: %v", err)
	}

	// The reversible half still works — the verdict is acted on immediately.
	if n := countIn(t, e, `SELECT count(*) FROM activity WHERE id = $1 AND archived_at IS NOT NULL`, activityID); n != 1 {
		t.Fatal("the noise verdict did not hide the message — the reversible half of the effect must be unchanged")
	}

	// Age it well past the undo window and sweep twice: no amount of waiting
	// turns an uncorroborated verdict into permission to destroy.
	backdateArchive(t, e, activityID)
	for range 2 {
		if err := engine.RedactNoiseWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), capture.NoiseUndoWindow, 0); err != nil {
			t.Fatalf("redaction sweep: %v", err)
		}
	}
	if n := countIn(t, e, `SELECT count(*) FROM activity WHERE id = $1 AND subject IS NOT NULL AND body IS NOT NULL`, activityID); n != 1 {
		t.Fatal("the sweep destroyed mail on the model's word alone — a forged bulk message would take a real correspondent's mail with it")
	}
	if n := rawCaptureRows(t, e, activityID); n != 1 {
		t.Fatalf("%d provider originals left, want 1 — the original must survive alongside the text", n)
	}
}

// stripActivityContent nulls only the activity's text, leaving the provider
// original — the state a non-atomic redaction would commit.
func stripActivityContent(t *testing.T, e *integration.Env, id ids.UUID) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE activity SET subject = NULL, body = NULL, raw = NULL WHERE id = $1`, id)
		return err
	})
	if err != nil {
		t.Fatalf("stripping the activity content: %v", err)
	}
}

// A noise verdict is evidence about the mail that was in front of it, so it
// cannot reach forward forever. Otherwise one forged message — sent as an
// address the workspace has never written to — would hide and destroy every mail
// the real owner of that address ever sends afterwards, unseen, with the
// "reply to recover" escape unreachable because the victim's mail is invisible.
func TestANoiseVerdictCannotReachMailSentLongAfterIt(t *testing.T) {
	e := integration.Setup(t)
	poisoned := seedCapturedMail(t, e, "cfo@bigcorp.example", "🚀 crypto deals")
	dispositionID := seedPendingDisposition(t, e, "cfo@bigcorp.example", "bigcorp.example", poisoned)

	brain := &scriptedVerdictBrain{verdicts: map[string]string{dispositionID.String(): capture.PendingStatusNoise}}
	engine := NewCounterpartyVerdictEngine(e.Pool, brain, slog.Default())
	if err := engine.RunWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), 0); err != nil {
		t.Fatalf("verdict pass: %v", err)
	}

	// Time passes — well past the window in which this verdict is evidence
	// about anything — and then the real owner writes.
	backdateResolution(t, e, dispositionID, 30*24*time.Hour)
	genuine := seedCapturedMail(t, e, "cfo@bigcorp.example", "re: our contract renewal")
	if err := engine.HideNoiseStragglersWorkspace(principal.WithWorkspaceID(context.Background(), e.WS)); err != nil {
		t.Fatalf("straggler sweep: %v", err)
	}

	// The forged message is theirs to hide. The genuine one is not.
	if n := countIn(t, e, `SELECT count(*) FROM activity WHERE id = $1 AND archived_at IS NOT NULL`, poisoned); n != 1 {
		t.Fatal("the judged message was not hidden")
	}
	if n := countIn(t, e, `SELECT count(*) FROM activity WHERE id = $1 AND archived_at IS NULL`, genuine); n != 1 {
		t.Fatal("mail sent long after the verdict was hidden by it — a forged message must not bar an address forever")
	}

	// A sender who stamps a far-future Date: header must not slip the reach
	// either. occurred_at is the message's own header, as forgeable as the From
	// this scope rule exists to distrust, so the bound reads the capture clock.
	dated := seedCapturedMail(t, e, "cfo@bigcorp.example", "posted from the future")
	stampOccurredAt(t, e, dated, 90*24*time.Hour)
	sync := seedPendingDisposition(t, e, "cfo@bigcorp.example", "bigcorp.example", dated)
	resolveAsNoise(t, e, sync)
	if err := engine.HideNoiseStragglersWorkspace(principal.WithWorkspaceID(context.Background(), e.WS)); err != nil {
		t.Fatalf("straggler sweep: %v", err)
	}
	if n := countIn(t, e, `SELECT count(*) FROM activity WHERE id = $1 AND archived_at IS NOT NULL`, dated); n != 1 {
		t.Fatal("a forged future Date header slipped the verdict's reach — the bound must read the capture clock")
	}

	// That the same mail also raises a FRESH question is the ladder's half of
	// this rule, proven where the real capture path runs
	// (capture_tiergate_integration_test.go) — this fixture inserts activities
	// directly and never consults the ladder.
}

// backdateResolution ages a resolved disposition so its undo window has passed,
// instead of sleeping out a seven-day wait.
func backdateResolution(t *testing.T, e *integration.Env, id ids.UUID, by time.Duration) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			UPDATE capture_pending_counterparty
			   SET resolved_at = now() - $2::interval WHERE id = $1`, id, by.String())
		return err
	})
	if err != nil {
		t.Fatalf("backdating the resolution: %v", err)
	}
}

// stampOccurredAt sets a message's self-reported arrival time — the Date header
// a sender chooses, as opposed to when capture actually saw it.
func stampOccurredAt(t *testing.T, e *integration.Env, id ids.UUID, ahead time.Duration) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE activity SET occurred_at = now() + $2::interval WHERE id = $1`, id, ahead.String())
		return err
	})
	if err != nil {
		t.Fatalf("stamping the arrival time: %v", err)
	}
}

// resolveAsNoise puts a disposition straight into the noise state, for cases
// where the verdict itself is not what is under test.
func resolveAsNoise(t *testing.T, e *integration.Env, id ids.UUID) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			UPDATE capture_pending_counterparty
			   SET status = 'noise', resolved_at = now(), next_attempt_at = NULL,
			       claimed_until = NULL, claimed_by = NULL
			 WHERE id = $1`, id)
		return err
	})
	if err != nil {
		t.Fatalf("resolving as noise: %v", err)
	}
}
