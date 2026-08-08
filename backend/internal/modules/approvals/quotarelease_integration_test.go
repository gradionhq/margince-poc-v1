// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package approvals

// The step-up's decision path against a real database — the two properties the
// unit suite cannot reach, because both are about what a transaction leaves
// behind.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// asAgent is the caller a step-up is ALWAYS staged by: an agent asserting a
// passport, acting for the human whose authority it borrows. That human is what
// the staging stamps as on_behalf_of, and it is the only person who can decide
// the question — staging as a human instead leaves the row with no lender and
// nobody able to answer it, which is a property the unit suite asserts.
func (e *stagingEnv) asAgent(t *testing.T) context.Context {
	t.Helper()
	// A real passport row, because the approval's passport_id is a foreign key:
	// the staging this file exercises is the one an agent actually makes, and a
	// fabricated id would exercise a shape the database refuses.
	passport := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO passport (id, workspace_id, on_behalf_of, granted_by, token_hash, scopes, expires_at)
		VALUES ($1, $2, $3, $3, $4, ARRAY['read'], now() + interval '30 days')`,
		passport, e.ws, e.rep, "hash-"+passport.String()); err != nil {
		t.Fatalf("seeding the lent passport: %v", err)
	}
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:" + e.rep.String(),
		PassportID: passport, OnBehalfOf: e.rep,
	})
}

// stageStepUp puts one step-up in the inbox, exactly as the tool surface does.
func (e *stagingEnv) stageStepUp(t *testing.T, counter agentquota.Counter) ids.ApprovalID {
	passport := ids.NewV7()
	t.Helper()
	proposal := agentquota.NewReleaseProposal(
		agentquota.Reading{Counter: counter, Observed: 2431, Limit: 2000, Allowance: 2000, Bucket: 42},
		passport, "search_records")
	payload, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := proposal.Identity()
	if err != nil {
		t.Fatal(err)
	}
	id, staged, err := e.svc.StageUnlessDeclined(e.asAgent(t), StageInput{
		Kind: KindQuotaRelease, ProposedChange: payload, DiffHash: string(identity),
		Summary: "continue?", JoinPending: true, Identity: identity,
	})
	if err != nil || !staged {
		t.Fatalf("staging a step-up: staged=%v err=%v", staged, err)
	}
	return id
}

// A step-up carries nothing a human should rewrite: its payload IS the question
// they were shown. An edit is refused, and — the half only a real transaction
// proves — refused before ANYTHING is written, so the row is neither rewritten
// nor recorded as decided.
func TestAStepUpCannotBeEditedIntoADifferentQuestion(t *testing.T) {
	e := setupStaging(t)
	id := e.stageStepUp(t, agentquota.Reads)
	// The possible edit, not an impossible one: writes is releasable too, so
	// the meter would have accepted it. What makes it wrong is that nobody was
	// shown it.
	edited, err := json.Marshal(agentquota.ReleaseProposal{
		Counter: agentquota.Writes, Observed: 1, Limit: 2, Allowance: 2, Bucket: "42",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, decideErr := e.svc.DecideEdited(e.as(), id, edited)

	var invalid *InvalidEditError
	if !errors.As(decideErr, &invalid) || !strings.Contains(decideErr.Error(), "answered yes or no") {
		t.Fatalf("editing a step-up → %v, want the edit refusal", decideErr)
	}

	after, err := e.svc.Get(e.as(), id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "pending" {
		t.Errorf("the refused edit left the row %q; nothing should have been decided", after.Status)
	}
	// jsonb round-trips with its own spacing, so the check is on the decoded
	// value rather than on bytes a formatter owns.
	kept, err := agentquota.DecodeReleaseProposal(after.ProposedChange)
	if err != nil {
		t.Fatalf("the stored payload no longer decodes: %v", err)
	}
	if kept.Counter != agentquota.Reads {
		t.Errorf("the refused edit rewrote the payload to %s", kept.Counter)
	}
}

// A plain yes is still a yes. The guard above must refuse the EDIT, not the
// kind — a step-up nobody can approve is a control with no release.
func TestAStepUpIsStillApprovableWithoutAnEdit(t *testing.T) {
	e := setupStaging(t)
	id := e.stageStepUp(t, agentquota.Reads)

	// No meter composed, so the release itself reports the absence loudly —
	// which is the point: the DECISION was reached, and only the effect could
	// not be applied.
	_, err := e.svc.Decide(e.as(), id, true, nil)

	if err == nil || !strings.Contains(err.Error(), "no quota meter") {
		t.Fatalf("approving a step-up → %v, want the decision to be reached and the meter absence reported", err)
	}
	after, getErr := e.svc.Get(e.as(), id)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if after.Status != approvalStatusApproved {
		t.Errorf("the row is %q after a plain approval, want approved", after.Status)
	}
}
