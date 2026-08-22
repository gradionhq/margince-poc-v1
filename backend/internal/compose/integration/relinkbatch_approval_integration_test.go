// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// What an agent's batch relink onto a project stages, who may decide it, and
// what the released retry moves. The card binds to the DESTINATION project,
// so deciding it takes the project's read floor; and it covers exactly the ids
// the approved call names, so a message that joins the conversation after the
// approval is not filed under it.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// relinkAgentCtx is a passport principal lent by Rep1, holding the write scope
// — the kind the confirm-first tier gates. The passport is a real row, because
// the staged approval is foreign-keyed to it.
func relinkAgentCtx(e *Env, passport ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:relink-probe", SeatType: principal.SeatFull,
		OnBehalfOf: e.Rep1, UserID: e.Rep1, PassportID: passport,
		Scopes:      principal.NewScopeSet(principal.ScopeRead, principal.ScopeWrite),
		Permissions: AdminPerms,
	})
}

// deciderPerms is a seat holding the decision grant relink_activities resolves
// to (activity.update) with or without the read the destination project
// demands.
func deciderPerms(projectRead bool) principal.Permissions {
	return principal.Permissions{
		RoleKeys: []string{"custom"},
		Objects: map[string]principal.ObjectGrant{
			"activity": {Read: true, Update: true},
			"project":  {Read: projectRead},
		},
		RowScope: principal.RowScopeAll,
	}
}

func TestAStagedBatchRelinkBindsToItsProjectAndMovesOnlyTheApprovedIDs(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	f := seedThreadFixture(t, e, owner)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	// The gate re-derives the lender's LIVE role from the database, and this
	// harness seeds users but no roles at all — so the lender gets a real role
	// row holding what the staging and the release read and write.
	e.WsExec(t, `INSERT INTO role (key, name, permissions) VALUES ('relink-lender', 'Relink lender', $1::jsonb)`,
		`{"objects":{"activity":{"read":true,"update":true},"project":{"read":true},"person":{"read":true}},"row_scope":"all"}`)
	e.WsExec(t, `INSERT INTO role_assignment (role_id, user_id) SELECT r.id, $1 FROM role r WHERE r.key = 'relink-lender'`, e.Rep1)
	agent := relinkAgentCtx(e, e.SeedPassport(t, owner, "relink probe"))
	svc := approvals.NewService(e.DB())

	// The thread form does not stage a project destination at all: the rows a
	// human would release are re-read at redemption, so the refusal sends the
	// caller to the named-set form instead.
	_, err := registry.Invoke(agent, "relink_thread",
		json.RawMessage(`{"thread_key":"`+f.key+`","entity_type":"project","entity_id":"`+f.project.String()+`"}`))
	var bad *agents.BadArgsError
	if !errors.As(err, &bad) {
		t.Fatalf("relink_thread onto a project → %v, want a refusal naming relink_activities", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval`); n != 0 {
		t.Fatalf("relink_thread left %d staged approval(s); a thread key cannot be approved", n)
	}

	// The named set stages, and the card targets the PROJECT.
	args := `{"activity_ids":["` + f.mine[0].String() + `","` + f.mine[1].String() +
		`"],"entity_type":"project","entity_id":"` + f.project.String() + `"}`
	_, err = registry.Invoke(agent, "relink_activities", json.RawMessage(args))
	var staged *workflow.StagedApprovalError
	if !errors.As(err, &staged) {
		t.Fatalf("relink_activities onto a project → %v, want a staged approval", err)
	}
	approvalID := staged.ApprovalID
	row, err := svc.Get(e.Admin(), approvalID)
	if err != nil {
		t.Fatalf("reading the staged card: %v", err)
	}
	if row.TargetType == nil || *row.TargetType != "project" || row.TargetID == nil || *row.TargetID != f.project {
		t.Fatalf("the card targets %v/%v, want project/%s — a card with no destination target is decidable "+
			"by anyone holding activity.update over rows they cannot write", row.TargetType, row.TargetID, f.project)
	}

	// A seat holding activity.update and NOT project.read cannot decide it.
	blind := e.As(e.Rep2, []ids.UUID{e.Team1}, deciderPerms(false))
	assertCannotDecideStagedApproval(blind, t, svc, "a seat holding activity.update and not project.read", approvalID)

	// A message joins the conversation after the card was staged.
	late := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, body, occurred_at, source, captured_by, thread_key)
		VALUES ($1, 'email', 'Re: milestone (late)', 'body', now(), 'manual', 'human:x', $2)`, f.key)

	// A seat holding both decides, and the released retry moves exactly the
	// approved ids.
	sighted := e.As(e.Rep3, []ids.UUID{e.Team2}, deciderPerms(true))
	if _, err := svc.Decide(sighted, approvalID, true, nil); err != nil {
		t.Fatalf("a seat holding activity.update and project.read approving → %v", err)
	}
	out, err := registry.Invoke(agent, "relink_activities",
		json.RawMessage(args[:len(args)-1]+`,"approval_id":"`+approvalID.String()+`"}`))
	if err != nil {
		t.Fatalf("the released retry → %v", err)
	}
	var envelope struct {
		Data struct {
			Relinked int `json:"relinked"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("decoding the answer: %v", err)
	}
	if envelope.Data.Relinked != 2 {
		t.Errorf("relinked = %d, want the two approved ids", envelope.Data.Relinked)
	}
	for _, id := range f.mine {
		if projectLinks(t, e, id, f.project) != 1 {
			t.Errorf("approved activity %s was not filed under the project", id)
		}
	}
	if projectLinks(t, e, late, f.project) != 0 {
		t.Error("a message that joined the thread after the approval was filed under the project — the release covered rows nobody approved")
	}
	if got := readProjectStamp(t, e, late); got.class != nil {
		t.Error("the late message was stamped as commercial correspondence under an approval that never described it")
	}
}
