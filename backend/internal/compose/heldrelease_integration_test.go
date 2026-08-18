// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Releasing a held draft, against a real database.
//
// It runs HERE rather than in the integration package because the executor and
// the wiring that binds it are both unexported, and because the thing under
// test is a TRANSACTION boundary: the redemption that spends a human's
// authority, the send it authorizes, and the parked run's completion all commit
// together or none of them do. No fake approvals engine can say anything about
// that — a stub would answer whatever it was written to answer, and the whole
// question is what the database ends up holding.
//
// The failure this is written against is specific and unrecoverable. Redeem
// first and the approval is consumed by a send that never happened: single-use
// authority spent, a human who saw a 500, and no way to release the message
// again. Send first and a crash leaves mail on the wire that nothing records
// anyone approving.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// draftRecipient is the one counterparty every fixture here answers, named once
// so an assertion and the seed cannot disagree about who the mail went to.
const draftRecipient = "anna@example.com"

// decider binds a REAL seeded human, not a synthetic one.
//
// approval.decided_by is a foreign key, so a made-up principal id is refused by
// the database rather than by the code under test — and the refusal arrives
// looking like a bug in the release.
func decider(e *integration.Env) context.Context {
	return e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
}

// heldDraftFixture seeds everything one released draft needs and returns the
// approval waiting to be decided, plus the run parked behind it.
//
// The run row is real rather than implied: CompleteApprovedRunTx matches on
// detail->>'approval_id', and a test that skipped the row would assert the
// send and silently prove nothing about the transition that stops a released
// draft leaving its automation waiting forever.
type heldDraftFixture struct {
	env      *integration.Env
	svc      *approvals.Service
	approval ids.ApprovalID
	anchor   ids.UUID
	handler  string
}

func seedHeldDraft(t *testing.T, e *integration.Env, svc *approvals.Service) heldDraftFixture {
	t.Helper()
	const to = draftRecipient
	owner := integration.OwnerConn(t)
	person := e.SeedPerson(t, "Anna Weber", nil)
	anchor := integration.SeedIDRow(t, owner, `
		INSERT INTO activity (id, kind, direction, subject, occurred_at, source, captured_by)
		VALUES ($1, 'email', 'inbound', 'Kickoff', now(), 'test', 'human:seed')`)
	// The purpose the send is gated against. Seeded with class
	// business_correspondence, which is the class that is never consent-gated
	// (ADR-0098 D1) — answering somebody who wrote to you rests on
	// Art 6(1)(b)/(f), not on a consent record. Without the row the gate
	// answers "purpose is not defined" and refuses, which is correct and is
	// exactly what a workspace that never configured one should get.
	e.WsExec(t, `
		INSERT INTO consent_purpose (key, label, requires_double_opt_in, class)
		VALUES ('business_correspondence', 'Business correspondence', false, 'business_correspondence')
		ON CONFLICT (key) DO NOTHING`)

	// The address on the PERSON record as well as on the thread. The consent
	// gate resolves a recipient address to a subject through person_email, so
	// without this the counterparty is a stranger to the gate and the send
	// refuses — correctly, and for a reason that has nothing to do with the
	// release under test.
	e.WsExec(t, `
		INSERT INTO person_email (person_id, email, is_primary, source, captured_by)
		VALUES ( $1, $2, true, 'test', 'human:seed')`, person, to)

	// The counterparty on the thread, carrying the address a reply answers.
	e.WsExec(t, `
		INSERT INTO activity_participant (id, activity_id, role, person_id, address)
		VALUES ($1, $2, 'from', $3, $4)`, ids.NewV7(), anchor, person, to)
	e.WsExec(t, `
		INSERT INTO activity_link (id, activity_id, entity_type, person_id)
		VALUES ($1, $2, 'person', $3)`, ids.NewV7(), anchor, person)

	proposal := automation.HeldDraftProposal{
		AnchorActivityID: anchor,
		To:               to,
		Subject:          "Re: Kickoff",
		Body:             "Hi Anna — here is what we agreed.",
		ConsentPurpose:   "business_correspondence",
		Intent:           "recap the meeting",
	}
	raw, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	// Staged under a HUMAN-less server principal, which is what makes it a
	// server-proposed staging the approve-side executor may run at all: an
	// agent-minted row deliberately reaches no executor.
	approvalID, err := svc.Stage(e.Admin(), approvals.StageInput{
		Kind:           automation.HeldDraftKind,
		ProposedChange: raw,
		DiffHash:       "held-" + ids.NewV7().String(),
		TargetType:     "activity",
		TargetID:       anchor,
		Summary:        "an automation drafted a reply to " + to,
		JoinPending:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The same columns the engine's own claim writes (engine_run.go): the
	// table lost its tenant column to the A136 sweep, so a run row is named by
	// (handler, idempotency_key) and nothing else.
	handler := "held_draft_probe_" + ids.NewV7().String()[:8]
	e.WsExec(t, `
		INSERT INTO workflow_run (handler, idempotency_key, trigger_event, planned, status, detail)
		VALUES ($1, $2, $3, $4, 'requires_approval', $5)`,
		handler, handler+":1", ids.NewV7(),
		[]byte(`{"actions":[{"Kind":"draft_email"}]}`),
		[]byte(`{"approval_id":"`+approvalID.String()+`"}`))

	return heldDraftFixture{env: e, svc: svc, approval: approvalID, anchor: anchor, handler: handler}
}

// releaseService builds the approvals service with the held-draft executor
// bound the way applySendPath binds it — through the real configured send
// store, with a real delivery stager.
//
// Not a hand-assembled store: the whole point of registering late is that the
// send path is configured by then, and a test that built a bare one would
// compare two equally under-configured things and pass.
func releaseService(t *testing.T, e *integration.Env) *approvals.Service {
	t.Helper()
	integration.ApplyRiverSchema(t)
	inserter, err := jobs.NewInserter(e.Pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("jobs.NewInserter: %v", err)
	}
	// A CONFIGURED send path, not a bare one. The public base URL is not
	// decoration: the send refuses without it, because an unsubscribe header
	// pointing nowhere is worse than none. That refusal is the reason this
	// executor is registered late, after server options have filled the path in
	// — registering it beside the other approve-side effects would bind the
	// empty one, and the release would fail on every installation.
	send := SendPath{
		Delivery:      NewDeliveryStager(e.Pool, inserter),
		PublicBaseURL: "https://crm.example.test",
	}
	store, gate := sendStore(e.Pool, send), consentGateFor(e.Pool)
	svc := approvals.NewService(e.DB())
	// BOTH halves, because production registers both and they are two readings
	// of one question. An executor without its preflight answers "can this be
	// released" only after the decision it should have been able to prevent.
	svc.WithEffect(automation.HeldDraftKind,
		heldDraftReleaseEffect(svc, store, gate, send.Delivery))
	svc.WithPrecheck(automation.HeldDraftKind,
		heldDraftPrecheck(store, gate, send.Delivery))
	return svc
}

func TestReleasingAHeldDraftSendsItAndCompletesTheParkedRun(t *testing.T) {
	e := integration.Setup(t)
	svc := releaseService(t, e)
	f := seedHeldDraft(t, e, svc)

	if _, err := svc.Decide(decider(e), f.approval, true, nil); err != nil {
		t.Fatalf("approving a held draft → %v, want the release to succeed", err)
	}

	// One outbound activity, one delivery. DRAFT-AC-N-7's invariant applied to
	// a release: the send either produced both or neither.
	if n := e.WsCount(t, `SELECT count(*) FROM activity
		WHERE direction = 'outbound' AND kind = 'email'`); n != 1 {
		t.Errorf("outbound activities = %d, want exactly 1", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM comms_outbound`); n != 1 {
		t.Errorf("comms_outbound rows = %d, want exactly 1", n)
	}
	// The authority was spent in the same commit.
	if n := e.WsCount(t, `SELECT count(*) FROM approval
		WHERE id = $1 AND consumed_at IS NOT NULL`, f.approval); n != 1 {
		t.Error("the approval was not consumed — a send whose authority is still redeemable can be sent twice")
	}
	// And the automation stopped waiting. Without this the run reads
	// requires_approval forever, describing a decision a human already made.
	var status string
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT status FROM workflow_run WHERE handler = $1`, f.handler).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "applied" {
		t.Errorf("parked run status = %q, want applied", status)
	}
}

// The decision is single-use, so a second approve cannot produce a second
// message. This is the assertion the design was most likely to get wrong: an
// email is irreversible, and "approved twice" is an ordinary double-click.
func TestAHeldDraftCannotBeReleasedTwice(t *testing.T) {
	e := integration.Setup(t)
	svc := releaseService(t, e)
	f := seedHeldDraft(t, e, svc)

	if _, err := svc.Decide(decider(e), f.approval, true, nil); err != nil {
		t.Fatalf("first release → %v, want ok", err)
	}
	if _, err := svc.Decide(decider(e), f.approval, true, nil); err == nil {
		t.Error("a second approve succeeded — the decision is meant to be single-use")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM activity
		WHERE direction = 'outbound' AND kind = 'email'`); n != 1 {
		t.Errorf("outbound activities after two approvals = %d, want exactly 1", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM comms_outbound`); n != 1 {
		t.Errorf("comms_outbound rows after two approvals = %d, want exactly 1", n)
	}
}

// A refusal must leave the draft RELEASABLE, and that is a stronger claim than
// "nothing was sent".
//
// The approvals engine commits a decision and only then runs the effect, so a
// refusal that reaches the effect leaves an approved row decideInTx will not
// decide again and no surface can re-drive — for a send, the message is simply
// gone. Two guards stand in front of that, and an archived anchor meets the
// FIRST: decidability refuses a staging whose target is no longer visible,
// before any status is written. So this test does not prove the preflight, and
// saying otherwise would be claiming coverage it does not have —
// TestAPreflightRefusalLeavesTheDraftPending below drives that one, with a
// refusal decidability has no opinion about.
//
// What it does prove is the property both guards exist to produce, and the one
// a human experiences: nothing sent, nothing consumed, the approval still
// PENDING — then the cause is fixed, the same row is approved, and it goes.
func TestAReleaseThatRefusesLeavesTheDraftPendingAndReleasableLater(t *testing.T) {
	e := integration.Setup(t)
	svc := releaseService(t, e)
	f := seedHeldDraft(t, e, svc)

	// Ordinary work on the thread the draft answers, done while it waits.
	e.WsExec(t, `UPDATE activity SET archived_at = now() WHERE id = $1`, f.anchor)

	if _, err := svc.Decide(decider(e), f.approval, true, nil); err == nil {
		t.Fatal("releasing onto an archived thread succeeded, want a refusal")
	}

	if n := e.WsCount(t, `SELECT count(*) FROM activity
		WHERE direction = 'outbound' AND kind = 'email'`); n != 0 {
		t.Errorf("outbound activities = %d, want 0 — the refusal must transmit nothing", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM comms_outbound`); n != 0 {
		t.Errorf("comms_outbound rows = %d, want 0", n)
	}
	// Still PENDING, not "approved but unconsumed". The difference is the whole
	// point: an approved row is a dead end, a pending one is a decision waiting.
	if n := e.WsCount(t, `SELECT count(*) FROM approval
		WHERE id = $1 AND status = 'pending' AND consumed_at IS NULL`, f.approval); n != 1 {
		t.Fatal("the approval is no longer pending — a refused release that decides the row anyway strands the message with nothing able to re-drive it")
	}

	// The human fixes the cause and approves the same row again.
	e.WsExec(t, `UPDATE activity SET archived_at = NULL WHERE id = $1`, f.anchor)
	if _, err := svc.Decide(decider(e), f.approval, true, nil); err != nil {
		t.Fatalf("re-approving after fixing the cause → %v, want the send to go", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM activity
		WHERE direction = 'outbound' AND kind = 'email'`); n != 1 {
		t.Errorf("outbound activities after the retry = %d, want exactly 1", n)
	}
}

// The production wiring, not a hand-assembled copy of it.
//
// releaseService above registers the effect by calling WithEffect directly,
// which proves the executor and leaves the REGISTRATION unproven — and the
// registration is the part with a subtle ordering requirement, since the send
// path is only configured after server options run. If applySendPath stopped
// registering, or bound a different service instance, every other test here
// would stay green.
func TestApplySendPathRegistersTheHeldDraftRelease(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	inserter, err := jobs.NewInserter(e.Pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("jobs.NewInserter: %v", err)
	}

	// The approvals surface built the way New builds it — before any send
	// configuration exists — and then the send path applied over it. That
	// ordering IS the thing under test.
	srv := Server{
		approvalsHandlers:  approvalsHandlersWithEffects(e.Pool, nil, slog.New(slog.NewTextHandler(io.Discard, nil))),
		activitiesHandlers: newActivitiesHandlers(e.Pool),
		send: SendPath{
			Delivery:      NewDeliveryStager(e.Pool, inserter),
			PublicBaseURL: "https://crm.example.test",
		},
	}
	srv.applySendPath(e.Pool)

	f := seedHeldDraft(t, e, approvals.NewService(e.DB()))

	// Through the SERVED handler, the same entry point the router calls — no
	// reaching past it into a service this test assembled itself.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/v1/approvals/"+f.approval.String()+"/approve", nil).WithContext(decider(e))
	srv.ApproveApproval(rec, req,
		crmcontracts.Id(f.approval.UUID), crmcontracts.ApproveApprovalParams{})

	if rec.Code != http.StatusOK {
		t.Fatalf("approve through the served surface → %d %s", rec.Code, rec.Body.String())
	}
	if n := e.WsCount(t, `SELECT count(*) FROM activity
		WHERE direction = 'outbound' AND kind = 'email'`); n != 1 {
		t.Error("the served approvals surface decided a held draft and sent nothing — applySendPath is not registering the release")
	}
}

// The preflight specifically, driven by a refusal decidability cannot see.
//
// The anchor is untouched here, so the target-visibility check passes and the
// decision would go ahead; what refuses is the consent gate, reached only by
// preparing the send. Without the preflight this refusal lands after the
// decision commits — an approved row nothing can decide again, a run parked
// forever, and a message a human said yes to that will never exist.
func TestAPreflightRefusalLeavesTheDraftPending(t *testing.T) {
	e := integration.Setup(t)
	svc := releaseService(t, e)
	f := seedHeldDraft(t, e, svc)

	// The purpose stops being answerable while the draft waits. Archiving it is
	// how an admin retires a basis, and it is the kind of thing that happens in
	// the days a draft can sit in an inbox.
	e.WsExec(t, `UPDATE consent_purpose SET archived_at = now() WHERE key = 'business_correspondence'`)

	if _, err := svc.Decide(decider(e), f.approval, true, nil); err == nil {
		t.Fatal("releasing with no answerable consent purpose succeeded, want a refusal")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM activity
		WHERE direction = 'outbound' AND kind = 'email'`); n != 0 {
		t.Errorf("outbound activities = %d, want 0", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval
		WHERE id = $1 AND status = 'pending'`, f.approval); n != 1 {
		t.Fatal("the approval was decided by a release that refused — the preflight is not running before the decision, so this message is now unreleasable")
	}
	// The run is still waiting too: a draft a human can still release must not
	// have had its automation terminated underneath it.
	var status string
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT status FROM workflow_run WHERE handler = $1`, f.handler).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "requires_approval" {
		t.Errorf("parked run status = %q, want it still waiting", status)
	}
}

// An edit may correct what a message SAYS. It may not change who receives it.
//
// The generic edit scope pins entity references, which sees the anchor and is
// blind to an address — so without a check of its own, an API caller could
// approve an automation's draft, filed under one thread and composed for one
// counterparty, into a message addressed to somebody else. The inbox never
// offered the field; this is what makes that true of the API too.
func TestAnEditedHeldDraftCannotBeReAimedAtAnotherRecipient(t *testing.T) {
	e := integration.Setup(t)
	svc := releaseService(t, e)
	f := seedHeldDraft(t, e, svc)

	retargeted, err := json.Marshal(automation.HeldDraftProposal{
		AnchorActivityID: f.anchor,
		To:               "someone.else@example.com",
		Subject:          "Re: Kickoff",
		Body:             "Hi Anna — here is what we agreed.",
		ConsentPurpose:   "business_correspondence",
		Intent:           "recap the meeting",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.DecideEdited(decider(e), f.approval, retargeted)
	if err == nil {
		t.Fatal("an edited approval re-aimed the message and was accepted")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM activity
		WHERE direction = 'outbound' AND kind = 'email'`); n != 0 {
		t.Errorf("outbound activities = %d, want 0 — nothing may go to a recipient nobody approved", n)
	}
	// Refused BEFORE the decision, so the original draft is still there to
	// release to the person it was actually written to.
	if n := e.WsCount(t, `SELECT count(*) FROM approval
		WHERE id = $1 AND status = 'pending'`, f.approval); n != 1 {
		t.Error("the retargeting attempt decided the approval — a refused edit must leave the honest draft releasable")
	}
}
