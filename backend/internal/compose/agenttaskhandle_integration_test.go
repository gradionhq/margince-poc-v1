// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Whose handle Create answers, against the real agent_task row.
//
// It exercises the adapter rather than the surface above it, and deliberately so:
// the surface reaches Create with a shared approval id only if the approval probe
// ever offers one approval to two credentials, which it does not
// (approvals.StageAgentCall scopes to the caller's own passport). That makes the
// credential half of Create's own predicate unreachable from outside — and a
// guard nothing exercises is a guard nobody knows still works. What Create
// promises is checkable here: a handle is answered to the passport that holds it
// and to nobody else, whatever id it is asked about.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// asPassport binds an agent principal presenting one passport — what taskPassport
// reads to decide whose handle this is.
func asPassport(e *integration.Env, passport ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:" + passport.String(),
		SeatType: principal.SeatFull, PassportID: passport,
	})
}

func TestATaskHandleIsAnsweredToThePassportThatHoldsItAndToNoOther(t *testing.T) {
	e := integration.Setup(t)
	tasks := toolTasks(e.Pool)
	approvalID := stageApprovalForTask(t, e)
	newTask := agents.NewTask{
		ApprovalID: approvalID, Tool: "disqualify_lead",
		StatusMessage: "staged for a human", ExpiresAt: time.Now().Add(time.Hour),
	}

	mine, theirs := seedTaskPassport(t, e, "holder"), seedTaskPassport(t, e, "other")
	held, err := tasks.Create(asPassport(e, mine), newTask)
	if err != nil {
		t.Fatalf("creating the handle: %v", err)
	}

	// The holder asking again gets its own handle back — one approval, one handle.
	again, err := tasks.Create(asPassport(e, mine), newTask)
	if err != nil {
		t.Fatalf("re-creating for the holder: %v", err)
	}
	if again.ID != held.ID {
		t.Fatalf("the holder was answered %s, want its own handle %s", again.ID, held.ID)
	}

	// Another passport is answered NOTHING rather than this one's handle. The
	// caller above it degrades to the plain refusal, which names the approval and
	// the header that redeems it — a handle it cannot poll would name neither.
	stolen, err := tasks.Create(asPassport(e, theirs), newTask)
	if err == nil {
		t.Fatalf("a second passport was answered handle %s for another passport's task", stolen.ID)
	}
	// The no-row refusal specifically, not merely "some error". The conflict arm's
	// predicate declines and RETURNING yields nothing, which is what mintTask
	// degrades from; any OTHER failure here — a broken predicate, a missing FK —
	// would satisfy "an error happened" while proving nothing about the contract.
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a second passport's create → %v, want the no-row refusal the fallback reads", err)
	}
	if !stolen.ID.IsZero() {
		t.Fatalf("a refused create still answered handle %s", stolen.ID)
	}
}

// stageApprovalForTask stages one 🟡 proposal through the REAL approvals writer,
// because agent_task.approval_id references it and a hand-inserted row would
// prove nothing about the approvals a task actually points at.
func stageApprovalForTask(t *testing.T, e *integration.Env) ids.ApprovalID {
	t.Helper()
	svc := approvals.NewService(database.BindTo(e.Pool, ids.From[ids.WorkspaceKind](e.WS)))
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.Rep1.String(), UserID: e.Rep1,
	})
	id, err := svc.Stage(ctx, approvals.StageInput{
		Kind: "disqualify_lead", ProposedChange: []byte(`{"lead_id":"x"}`),
		DiffHash: "one-call-hash", Summary: "Disqualify this lead?",
	})
	if err != nil {
		t.Fatalf("staging the approval a task points at: %v", err)
	}
	return id
}

// seedTaskPassport creates a passport row, since agent_task references one.
func seedTaskPassport(t *testing.T, e *integration.Env, label string) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.Pool.Exec(context.Background(), `
		INSERT INTO passport (id, label, on_behalf_of, granted_by, token_hash, scopes, expires_at)
		VALUES ($1, $2, $3, $3, $4, $5, now() + interval '30 days')`,
		id, label, e.Rep1, "hash-"+id.String(), []string{"read", "write"}); err != nil {
		t.Fatalf("seeding the %s passport: %v", label, err)
	}
	return id
}
