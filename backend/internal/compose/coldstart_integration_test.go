//go:build integration

package compose

// The cold-start read-back: the no-guess gate drops everything the
// model cannot evidence VERBATIM from the fetched page, the surviving
// fields stage a 🟡 approval (nothing touches real records), and the
// decision echoes the coldstart lifecycle events.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type fixturePage string

func (p fixturePage) Fetch(context.Context, string) (string, error) { return string(p), nil }

const acmePage = fixturePage(`Acme GmbH — Onboard your team in minutes, not weeks. ` +
	`Built for RevOps leaders at scaling SaaS companies. ` +
	`Registered in Berlin, HRB 12345.`)

func TestColdStartStagesOnlyEvidencedFields(t *testing.T) {
	e := setupAuthz(t)
	brain := ai.NewFakeClient().Script(`{"fields":[
		{"field":"value_proposition","value":"Fast onboarding","evidence_snippet":"Onboard your team in minutes, not weeks","confidence":0.9},
		{"field":"icp","value":"RevOps at SaaS scale-ups","evidence_snippet":"Built for RevOps leaders at scaling SaaS companies","confidence":0.7},
		{"field":"legal_name","value":"Acme GmbH","evidence_snippet":"this text is NOT on the page","confidence":0.9},
		{"field":"industry","value":"Software","evidence_snippet":"Acme GmbH","confidence":1.7},
		{"field":"made_up_field","value":"x","evidence_snippet":"Acme GmbH","confidence":0.5}]}`)
	engine := &coldStartEngine{fetch: acmePage, brain: brain, approvals: approvals.NewService(e.pool)}

	ctx := e.as(e.rep1, []ids.UUID{e.team1}, schedulerPerms)
	proposal, err := engine.Propose(ctx, "https://acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Fields) != 2 {
		t.Fatalf("no-guess gate let %d fields through, want 2 (hallucinated evidence, bad confidence and unknown names must drop): %+v",
			len(proposal.Fields), proposal.Fields)
	}
	if proposal.Status != "staged" || proposal.ProposalId.String() == ids.Nil.String() {
		t.Fatalf("proposal not staged: %+v", proposal)
	}

	// The staged approval row is the proposal; the staging emitted both
	// the approval.requested and the coldstart lifecycle event.
	var kind, status string
	var eventCount int
	err = database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT kind, status FROM approval WHERE id = $1`, ids.UUID(proposal.ProposalId)).Scan(&kind, &status); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM event_outbox WHERE envelope->>'type' IN ('approval.requested', 'coldstart.read_back_proposed')`).Scan(&eventCount)
	})
	if err != nil {
		t.Fatal(err)
	}
	if kind != "coldstart" || status != "pending" || eventCount != 2 {
		t.Fatalf("staging landed as kind=%s status=%s events=%d, want coldstart/pending/2", kind, status, eventCount)
	}

	// Accepting needs organization.update (the effect the proposal
	// writes on acceptance) — the admin has it; the decision echoes
	// coldstart.accepted.
	svc := approvals.NewService(e.pool)
	if _, err := svc.Decide(e.as(e.rep2, nil, adminPerms), ids.UUID(proposal.ProposalId), true, nil); err != nil {
		t.Fatalf("accepting the proposal: %v", err)
	}
	var accepted int
	err = database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'coldstart.accepted'`).Scan(&accepted)
	})
	if err != nil || accepted != 1 {
		t.Fatalf("coldstart.accepted events = %d (%v), want 1", accepted, err)
	}
}

func TestColdStartRefusesWhenNothingSurvivesTheGate(t *testing.T) {
	e := setupAuthz(t)
	brain := ai.NewFakeClient().Script(
		`{"fields":[{"field":"icp","value":"guessed","evidence_snippet":"nowhere on the page","confidence":0.9}]}`,
		`not even JSON`)
	engine := &coldStartEngine{fetch: acmePage, brain: brain, approvals: approvals.NewService(e.pool)}
	ctx := e.as(e.rep1, []ids.UUID{e.team1}, schedulerPerms)

	var unreadable *unreadableError
	if _, err := engine.Propose(ctx, "https://acme.example"); !errors.As(err, &unreadable) {
		t.Fatalf("all-hallucinated extraction → %v, want unreadable", err)
	}
	if _, err := engine.Propose(ctx, "https://acme.example"); !errors.As(err, &unreadable) {
		t.Fatalf("unparseable model output → %v, want unreadable", err)
	}
	// A page below the readable floor never reaches the model.
	tiny := &coldStartEngine{fetch: fixturePage("hi"), brain: brain, approvals: approvals.NewService(e.pool)}
	if _, err := tiny.Propose(ctx, "https://acme.example"); !errors.As(err, &unreadable) {
		t.Fatalf("tiny page → %v, want unreadable", err)
	}
}
