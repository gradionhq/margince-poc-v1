// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package approvals

// Whose approval a call is offered, and for how long — the two questions
// StageAgentCall answers with SQL, so neither can be shown without a database.
//
// The credential half is the one that has to be right. A caller is handed an id
// it never presented, which is safe only while that id is one the caller could
// actually spend: the redemption enforces the passport binding only against a
// caller that presents a passport, so a laxer probe would volunteer one
// credential's authority object to another and a passport-less caller could then
// spend it.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// agentCall is the staging every case below re-makes: byte-identical, which is
// the whole premise — one call, one authority object.
func (e *stagingEnv) agentCall(target ids.UUID) StageInput {
	return StageInput{
		Kind:           "enrich",
		ProposedChange: []byte(`{"organization_id":"` + target.String() + `","url":"https://stainzer.at"}`),
		DiffHash:       "c276f78957b5b2fc-one-call",
		TargetType:     "organization",
		TargetID:       target,
		Summary:        "Read stainzer.at and propose what it says",
	}
}

// asPassport binds an agent principal presenting one passport, which is what a
// governed MCP tool call arrives as.
func (e *stagingEnv) asPassport(passport ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:" + passport.String(),
		OnBehalfOf: e.rep, PassportID: passport,
	})
}

func (e *stagingEnv) seedPassport(t *testing.T) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO passport (id, workspace_id, label, on_behalf_of, granted_by, token_hash, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $4, $5, $6, now() + interval '30 days')`,
		id, e.ws, "agent "+id.String(), e.rep, "hash-"+id.String(), []string{"read", "enrich"}); err != nil {
		t.Fatalf("seeding a passport: %v", err)
	}
	return id
}

func (e *stagingEnv) seedOrg(t *testing.T) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO organization (id, display_name, source, captured_by)
		VALUES ($1, 'Steirische Molkerei AG', 'gmail:seed', 'connector:gmail')`, id); err != nil {
		t.Fatalf("seeding an organization: %v", err)
	}
	return id
}

// approve settles the staged call as the human who lent the passport would.
//
// A decider needs to SEE the target: a staging against an organization is
// refused as not-found for anyone whose row scope does not reach that row
// (approvals/targetvisibility.go), which is existence-hiding working correctly
// and not the property under test here.
func (e *stagingEnv) approve(t *testing.T, id ids.ApprovalID) {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.rep.String(), UserID: e.rep,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			RowScope: principal.RowScopeAll,
			Objects: map[string]principal.ObjectGrant{
				"organization": {Create: true, Read: true, Update: true, Delete: true},
				"approval":     {Create: true, Read: true, Update: true, Delete: true},
			},
		},
	})
	if _, err := e.svc.Decide(ctx, id, true, nil); err != nil {
		t.Fatalf("approving %s: %v", id, err)
	}
}

// A released decision belongs to the credential it was granted to. Another
// passport asking the identical question gets its own, and the SQL predicate is
// what makes that true — the redemption would have refused the cross-credential
// id anyway, but only after the agent had been sent to present it.
func TestAnAgentCallIsOnlyOfferedApprovalsItsOwnCredentialHolds(t *testing.T) {
	e := setupStaging(t)
	target := e.seedOrg(t)
	in := e.agentCall(target)
	mine, theirs := e.seedPassport(t), e.seedPassport(t)

	staged, approved, err := e.svc.StageAgentCall(e.asPassport(mine), in)
	if err != nil || approved {
		t.Fatalf("first staging → (%v, approved=%v), want a fresh undecided approval", err, approved)
	}
	e.approve(t, staged)

	// The credential that was granted the decision is pointed at it.
	again, approvedAgain, err := e.svc.StageAgentCall(e.asPassport(mine), in)
	if err != nil {
		t.Fatal(err)
	}
	if again != staged || !approvedAgain {
		t.Fatalf("the granted passport got (%s, approved=%v), want (%s, approved=true)", again, approvedAgain, staged)
	}

	// A DIFFERENT passport is not, and is not told the call is approved.
	other, otherApproved, err := e.svc.StageAgentCall(e.asPassport(theirs), in)
	if err != nil {
		t.Fatal(err)
	}
	if otherApproved {
		t.Fatal("a second passport was told the call it is making is already approved")
	}
	if other == staged {
		t.Fatal("a second passport was handed the approval granted to the first")
	}

	// And neither is a caller presenting NO passport — the case the redemption
	// alone would have admitted, since it checks the binding only against a
	// caller that presents one. It must not be offered either credential's row:
	// not the approved one, and not the undecided one the second passport is
	// waiting on.
	none, noneApproved, err := e.svc.StageAgentCall(e.agentWithoutPassport(), in)
	if err != nil {
		t.Fatal(err)
	}
	if noneApproved {
		t.Fatal("a passport-less caller was offered a decision granted to a passport")
	}
	if none == staged || none == other {
		t.Fatalf("a passport-less caller was handed %s, a row staged by a passport", none)
	}
}

// agentWithoutPassport is an agent principal carrying no passport — the shape the
// REST gate's session-authenticated agent path produces.
func (e *stagingEnv) agentWithoutPassport() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:sessionbound", OnBehalfOf: e.rep,
	})
}

// A human's yes is a judgment about the world NOW: past the redemption window it
// buys nothing, so the call has to be asked again rather than pointed at a
// decision the redemption will refuse.
func TestAnAgentCallWhoseApprovalOutlivedItsWindowIsAskedAgain(t *testing.T) {
	e := setupStaging(t)
	target := e.seedOrg(t)
	in := e.agentCall(target)
	passport := e.seedPassport(t)

	staged, _, err := e.svc.StageAgentCall(e.asPassport(passport), in)
	if err != nil {
		t.Fatal(err)
	}
	e.approve(t, staged)
	// Backdated through the owner connection, which is how this suite reaches a
	// clock the service cannot be asked to lie about.
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE approval SET decided_at = now() - $1::interval WHERE id = $2`,
		(RedemptionWindow + time.Minute).String(), staged); err != nil {
		t.Fatalf("backdating the decision: %v", err)
	}

	fresh, approved, err := e.svc.StageAgentCall(e.asPassport(passport), in)
	if err != nil {
		t.Fatal(err)
	}
	if approved {
		t.Fatal("a decision past its redemption window was offered as spendable")
	}
	if fresh == staged {
		t.Fatal("the lapsed approval was handed back instead of the question being asked again")
	}
}

// The rows the old behaviour already wrote. Four approved, unspent approvals for
// one call exist on at least one installation, and the fix stops more being
// created without deleting decisions a human actually made — so what they mean
// has to be specified rather than left to whichever one the probe happens to
// reach first.
//
// Each row IS an authority object (ADR-0036), so each is spendable exactly once
// and a human's four answers buy four executions — not one answer buying four,
// which is what the sentence "the gate hands back an approval it was not shown"
// could otherwise be read as licensing. The order is the canonical one, oldest
// first, so the pile drains deterministically instead of the same row being
// offered until it happens to be spent.
func TestLegacyDuplicateApprovalsAreOfferedOnceEachAndOldestFirst(t *testing.T) {
	e := setupStaging(t)
	target := e.seedOrg(t)
	in := e.agentCall(target)
	passport := e.seedPassport(t)
	ctx := e.asPassport(passport)

	// The duplicates are written the only way that proves anything about the
	// rows in production: by the real stager, before the fix could collapse
	// them. insertProposalInTx is what StageAgentCall calls when it finds
	// nothing live, so calling it directly reproduces the old path exactly.
	first, second := e.stageDuplicateInTx(ctx, t, in), e.stageDuplicateInTx(ctx, t, in)
	e.approve(t, first)
	e.approve(t, second)

	offered, approved, err := e.svc.StageAgentCall(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !approved || offered != first {
		t.Fatalf("offered (%s, approved=%v), want the oldest approved row (%s)", offered, approved, first)
	}
	// Spend it, exactly as the agent's retry does.
	if _, _, err := e.svc.Redeem(ctx, first, in.Kind, in.DiffHash); err != nil {
		t.Fatalf("redeeming the offered approval: %v", err)
	}

	next, nextApproved, err := e.svc.StageAgentCall(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !nextApproved || next != second {
		t.Fatalf("offered (%s, approved=%v) after spending the first, want the second (%s)", next, nextApproved, second)
	}
	if _, _, err := e.svc.Redeem(ctx, second, in.Kind, in.DiffHash); err != nil {
		t.Fatalf("redeeming the second approval: %v", err)
	}

	// Both decisions spent, so the next identical call is a new question — no
	// spent row is ever offered again.
	fresh, freshApproved, err := e.svc.StageAgentCall(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if freshApproved || fresh == first || fresh == second {
		t.Fatalf("offered (%s, approved=%v) with both decisions spent, want a fresh question", fresh, freshApproved)
	}
}

// stageDuplicateInTx writes one approval the way the pre-fix gate did: straight
// to insertProposalInTx, with no probe and no join.
func (e *stagingEnv) stageDuplicateInTx(ctx context.Context, t *testing.T, in StageInput) ids.ApprovalID {
	t.Helper()
	var id ids.ApprovalID
	if err := e.svc.db.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		id, err = e.svc.insertProposalInTx(ctx, tx, in)
		return err
	}); err != nil {
		t.Fatalf("staging a duplicate the old way: %v", err)
	}
	return id
}
