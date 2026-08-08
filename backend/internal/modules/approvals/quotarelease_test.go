// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

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

// recordingReleaser is the meter as the decision path uses it.
type recordingReleaser struct {
	ws       ids.UUID
	passport ids.UUID
	counter  agentquota.Counter
	bucket   int64
	calls    int
	err      error
}

func (r *recordingReleaser) Release(_ context.Context, ws, passport ids.UUID, c agentquota.Counter, bucket int64) (bool, error) {
	r.calls++
	r.ws, r.passport, r.counter, r.bucket = ws, passport, c, bucket
	return r.err == nil, r.err
}

// stepUpRow is a staged step-up as the decision path reads it back.
func stepUpRow(t *testing.T, passport ids.UUID, proposal agentquota.ReleaseProposal) row {
	t.Helper()
	raw, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	id := ids.From[ids.PassportKind](passport)
	return row{Kind: KindQuotaRelease, PassportID: &id, ProposedChange: raw}
}

func releaseCtx(ws ids.UUID) context.Context {
	return principal.WithWorkspaceID(context.Background(), ws)
}

// The release names the window the AGENT was refused in, from the row's own
// passport — not the approver's context. A version that read the context would
// widen the approving human's counter, which is not metered at all, so it would
// look like it worked while the agent stayed refused.
func TestAReleaseWidensTheAgentsWindowAndNotTheApproversContext(t *testing.T) {
	ws, passport := ids.New[ids.WorkspaceKind]().UUID, ids.New[ids.PassportKind]().UUID
	meter := &recordingReleaser{}
	svc := NewService(nil).WithQuotaReleaser(meter)
	a := stepUpRow(t, passport, agentquota.ReleaseProposal{
		Counter: agentquota.Reads, Observed: 2431, Limit: 2000, Bucket: 42, Tool: "search_records",
	})

	if err := svc.applyQuotaRelease(releaseCtx(ws), a); err != nil {
		t.Fatal(err)
	}

	if meter.passport != passport {
		t.Errorf("released passport %s, want the one the staging was stamped with (%s)", meter.passport, passport)
	}
	if meter.ws != ws {
		t.Errorf("released workspace %s, want %s", meter.ws, ws)
	}
	if meter.counter != agentquota.Reads || meter.bucket != 42 {
		t.Errorf("released %s in window %d, want reads in 42", meter.counter, meter.bucket)
	}
}

// A release that applied to NOTHING is not an error. The ordinary cause is a
// human answering after the window rolled, and by then the agent is not refused
// anyway — there is nothing to widen because there is nothing to release it
// from. Their decision still stands as the record that they said yes.
func TestAReleaseThatWidenedNothingIsNotAnError(t *testing.T) {
	meter := &recordingReleaser{}
	svc := NewService(nil).WithQuotaReleaser(meter)
	a := stepUpRow(t, ids.New[ids.PassportKind]().UUID,
		agentquota.ReleaseProposal{Counter: agentquota.Reads, Bucket: 1})

	if err := svc.applyQuotaRelease(releaseCtx(ids.New[ids.WorkspaceKind]().UUID), a); err != nil {
		t.Errorf("a release into a rolled window reported an error: %v", err)
	}
}

// A payload naming a quota no human can release is refused. It matters because
// the staged payload is EDITABLE — the modify-then-approve arm pins entity
// references, and this payload has none — so "egress" is a value an approver
// can type into a question that was asked about reads.
func TestAnEditedPayloadCannotTurnAStepUpIntoAHardStopRelease(t *testing.T) {
	meter := &recordingReleaser{}
	svc := NewService(nil).WithQuotaReleaser(meter)

	for _, counter := range []agentquota.Counter{agentquota.Egress, agentquota.Calls, agentquota.Cost} {
		a := stepUpRow(t, ids.New[ids.PassportKind]().UUID,
			agentquota.ReleaseProposal{Counter: counter, Bucket: 1})

		err := svc.applyQuotaRelease(releaseCtx(ids.New[ids.WorkspaceKind]().UUID), a)

		if err == nil {
			t.Errorf("an edited payload released %s, which no rung of the ladder allows", counter)
		}
	}
	if meter.calls != 0 {
		t.Errorf("the meter was asked to release a hard stop %d times", meter.calls)
	}
}

// A row with no passport names no window. Releasing "the current context's"
// would widen the approver's own counter — unmetered, so it would silently
// succeed — which is exactly the failure this refuses loudly.
func TestAStepUpWithNoPassportIsRefusedRatherThanReleasedAgainstTheApprover(t *testing.T) {
	meter := &recordingReleaser{}
	svc := NewService(nil).WithQuotaReleaser(meter)
	raw, err := json.Marshal(agentquota.ReleaseProposal{Counter: agentquota.Reads, Bucket: 1})
	if err != nil {
		t.Fatal(err)
	}

	applyErr := svc.applyQuotaRelease(releaseCtx(ids.New[ids.WorkspaceKind]().UUID),
		row{Kind: KindQuotaRelease, ProposedChange: raw})

	if applyErr == nil || !strings.Contains(applyErr.Error(), "no passport") {
		t.Fatalf("a passportless step-up → %v, want a refusal naming the missing passport", applyErr)
	}
	if meter.calls != 0 {
		t.Error("the meter was asked to release a window nobody owns")
	}
}

// A composition with no meter fails LOUDLY rather than reporting a release it
// did not make. A silent success here is the worst of both: the human believes
// they released the agent, and the agent stays refused.
func TestApprovingAStepUpWithNoMeterComposedFails(t *testing.T) {
	svc := NewService(nil)
	a := stepUpRow(t, ids.New[ids.PassportKind]().UUID,
		agentquota.ReleaseProposal{Counter: agentquota.Reads, Bucket: 1})

	if err := svc.applyQuotaRelease(releaseCtx(ids.New[ids.WorkspaceKind]().UUID), a); err == nil {
		t.Error("a service with no meter reported a release it could not have made")
	}
}

// The meter's own failure surfaces to the deciding human. It is the one branch
// where the approval is recorded and the effect is not, and saying so is what
// lets them try again rather than believe the agent is free to continue.
func TestAMeterFailureReachesTheDecidingHuman(t *testing.T) {
	meter := &recordingReleaser{err: errors.New("redis is unreachable")}
	svc := NewService(nil).WithQuotaReleaser(meter)
	a := stepUpRow(t, ids.New[ids.PassportKind]().UUID,
		agentquota.ReleaseProposal{Counter: agentquota.Reads, Bucket: 1})

	err := svc.applyQuotaRelease(releaseCtx(ids.New[ids.WorkspaceKind]().UUID), a)

	if err == nil {
		t.Fatal("a meter failure was swallowed; the human is told the agent may continue when it may not")
	}
}

// THE authority rule: a step-up is decided by the human who lent the passport,
// and by nobody else. Not an admin, not the workspace owner — an agent's ceiling
// is the granting human's authority, so the only person who can widen what it
// may be handed is the person whose reading it is doing.
//
// A target-less row needs no transaction to answer, which is what lets this run
// as a unit test over the real predicate rather than a paraphrase of it.
func TestAStepUpIsDecidedByTheLenderAlone(t *testing.T) {
	lender := ids.New[ids.UserKind]().UUID
	a := row{Kind: KindQuotaRelease, OnBehalfOf: ptr(ids.From[ids.UserKind](lender))}
	// Every object grant there is, held by someone who is not the lender.
	admin := principal.Principal{
		UserID: ids.New[ids.UserKind]().UUID,
		Permissions: principal.Permissions{Objects: map[string]principal.ObjectGrant{
			tableDeal: {Create: true, Read: true, Update: true, Delete: true},
		}},
	}

	decidableByAdmin, err := decidable(context.Background(), nil, admin, a)
	if err != nil {
		t.Fatal(err)
	}
	if decidableByAdmin {
		t.Error("a workspace admin can release another human's agent; an agent's ceiling is its lender's authority, not everyone's")
	}

	byLender, err := decidable(context.Background(), nil, principal.Principal{UserID: lender}, a)
	if err != nil {
		t.Fatal(err)
	}
	if !byLender {
		t.Error("the human who lent the passport cannot answer the question asked of them")
	}
}

// And a step-up with no lender recorded is decidable by NOBODY, rather than by
// everybody. The empty decision-grant set is only safe while the self-only
// clause holds; this is the case that proves the clause fails closed.
func TestAStepUpWithNoLenderIsDecidableByNobody(t *testing.T) {
	anyone := principal.Principal{UserID: ids.New[ids.UserKind]().UUID}

	got, err := decidable(context.Background(), nil, anyone, row{Kind: KindQuotaRelease})
	if err != nil {
		t.Fatal(err)
	}

	if got {
		t.Error("a step-up recorded for nobody was decidable by whoever asked")
	}
}

func ptr[T any](v T) *T { return &v }
