// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package activities

// What the send path asks of the voice learning loop, and what an answer from
// it costs the send. The harness and the send stubs both these cases ride live
// in email_integration_test.go; every case drives Store.SendEmail rather than
// the HTTP handler, because the MCP tool surface calls the store directly.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// testDraftRef stands for the opaque reference the drafting operation handed
// back with the text a model served.
const testDraftRef = "vd-7f3a"

// draftedSendInput is a send that carries a served draft's reference — the
// only shape that resolves a learning signal at all.
func draftedSendInput(purpose string) SendEmailInput {
	in := sendInput(purpose)
	in.DraftRef = testDraftRef
	return in
}

// deliveryWritingStager stages a REAL row, the way the comms store does. The
// in-memory stub cannot show a rollback — a Go slice survives one — so proving
// that a failed learning write leaves no delivery behind needs a stager whose
// record lives in the transaction.
type deliveryWritingStager struct {
	userID ids.UUID
	staged int
}

func (s *deliveryWritingStager) StageTx(ctx context.Context, tx pgx.Tx, in DeliveryRequest) error {
	recipients, err := json.Marshal(in.Recipients)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO comms_outbound
		  (id, workspace_id, activity_id, user_id, provider, message_id,
		   recipients, subject, body, consent_purpose)
		VALUES ($1, current_setting('app.workspace_id')::uuid, $2, $3, $4, $5, $6, $7, $8, $9)`,
		ids.NewV7(), in.ActivityID, s.userID, in.Provider, in.MessageID,
		recipients, in.Subject, in.Body, in.ConsentPurpose)
	if err != nil {
		return err
	}
	// Counted after the INSERT, not before: the rollback test asserts on a
	// delivery that was actually written, and an attempt counter would let a
	// never-written row read as proof that the rollback removed one.
	s.staged++
	return nil
}

func (e *sendEnv) deliveryCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM comms_outbound WHERE workspace_id = $1`, e.ws).Scan(&n); err != nil {
		t.Fatalf("counting staged deliveries: %v", err)
	}
	return n
}

// The recorder is optional wiring, exactly like the unsubscribe linker: a
// deployment that runs no voice profile has no signal to close, and its mail
// must still leave. Nil records nothing SILENTLY, so this is also the case a
// mis-wired composition would look identical to — which is why the wiring
// itself is proven where it is assembled, not here.
func TestSendEmailProceedsWhenNoDraftOutcomeRecorderIsWired(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}

	if _, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeAll), anchor, draftedSendInput("transactional"), stubConsentGate{}, stager); err != nil {
		t.Fatalf("SendEmail with no learning loop wired: %v", err)
	}
	if n := e.outboundCount(t); n != 1 {
		t.Fatalf("%d outbound activities, want 1 — an unwired learning loop must not cost the send", n)
	}
}

// Every learning-domain refusal — an unknown reference, an erased one, one
// another user owns, one a previous send already decided — answers
// recorded=false with no error, and none of them may fail a message that
// legitimately went out.
func TestSendEmailProceedsWhenTheDraftReferenceResolvesNoSignal(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}
	recorder := &recordingDraftOutcome{recorded: false}

	if _, err := e.store(stubUnsubscribeLinker{}).WithDraftOutcome(recorder).SendEmail(
		e.as(principal.RowScopeAll), anchor, draftedSendInput("transactional"), stubConsentGate{}, stager); err != nil {
		t.Fatalf("SendEmail over an unresolvable draft reference: %v", err)
	}
	if len(recorder.calls) != 1 {
		t.Fatalf("the send path asked the learning loop %d times, want exactly 1", len(recorder.calls))
	}
	if got := recorder.calls[0].draftRef; got != testDraftRef {
		t.Fatalf("the learning loop was asked about %q, want the reference the send carried (%q)", got, testDraftRef)
	}
	if n := e.outboundCount(t); n != 1 {
		t.Fatalf("%d outbound activities, want 1 — a signal that resolved nothing must not undo the send", n)
	}
}

// A fault is the other half of the seam's asymmetry: it arrives inside the
// transaction that already holds the activity AND the delivery, and half a
// write shape must never commit. So the fault takes the whole send with it,
// rather than leaving a timeline entry and a queued message behind a judgment
// that was never written.
func TestSendEmailRollsBackTheWholeSendWhenTheLearningWriteFaults(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &deliveryWritingStager{userID: e.rep}
	fault := errors.New("voice_learning_signal is unavailable")
	recorder := &recordingDraftOutcome{err: fault}

	_, err := e.store(stubUnsubscribeLinker{}).WithDraftOutcome(recorder).SendEmail(
		e.as(principal.RowScopeAll), anchor, draftedSendInput("transactional"), stubConsentGate{}, stager)
	if !errors.Is(err, fault) {
		t.Fatalf("SendEmail over a faulting learning write → %v, want the fault", err)
	}
	// Without this the row counts below prove nothing: a delivery that was
	// never written cannot show that a rollback removed one.
	if stager.staged != 1 {
		t.Fatalf("the stager wrote %d deliveries before the fault, want 1", stager.staged)
	}
	if n := e.outboundCount(t); n != 0 {
		t.Fatalf("%d outbound activities survived the fault, want 0 (one transaction, one fact)", n)
	}
	if n := e.deliveryCount(t); n != 0 {
		t.Fatalf("%d staged deliveries survived the fault, want 0 — a message would be transmitted for a send that never committed", n)
	}
}

// The judgment is about the text the HUMAN approved, not about what the wire
// carried: the send appends an unsubscribe footer the sender never wrote, and
// judging that would score every marketing draft as edited. The case is a
// MARKETING send precisely because the two bodies differ there — with no
// footer applied, passing the wrong one would look identical.
func TestDraftOutcomeJudgesTheApprovedBodyNotTheTransmittedFooter(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}
	recorder := &recordingDraftOutcome{recorded: true}
	linker := stubUnsubscribeLinker{token: testUnsubscribeTok, ok: true}

	in := draftedSendInput("marketing_email")
	in.Recipients, in.Cc = []string{"buyer@example.test"}, nil
	if _, err := e.store(linker).WithDraftOutcome(recorder).SendEmail(
		e.as(principal.RowScopeAll), anchor, in, stubConsentGate{}, stager); err != nil {
		t.Fatalf("SendEmail: %v", err)
	}

	staged := stager.only(t)
	if !strings.Contains(staged.Body, "/unsubscribe?purpose=marketing_email") {
		t.Fatalf("the transmitted body carries no footer, so this case proves nothing:\n%s", staged.Body)
	}
	if len(recorder.calls) != 1 {
		t.Fatalf("the send path asked the learning loop %d times, want exactly 1", len(recorder.calls))
	}
	if got := recorder.calls[0].finalBody; got != in.Body {
		t.Fatalf("the learning loop judged %q, want the body the human approved (%q)", got, in.Body)
	}
}

// Mail composed independently of any draft resolves no signal and must not
// cost a query for a row that cannot exist.
func TestSendEmailAsksNothingOfTheLearningLoopWithoutADraftReference(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}
	recorder := &recordingDraftOutcome{}

	if _, err := e.store(stubUnsubscribeLinker{}).WithDraftOutcome(recorder).SendEmail(
		e.as(principal.RowScopeAll), anchor, sendInput("transactional"), stubConsentGate{}, stager); err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("a send carrying no draft reference asked the learning loop %d times, want none", len(recorder.calls))
	}
}
