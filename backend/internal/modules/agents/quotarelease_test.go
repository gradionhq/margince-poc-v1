// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// crossedQuota is a meter that reports one counter already spent — the state
// every rung of the ladder is entered from.
type crossedQuota struct {
	counter  agentquota.Counter
	observed int
	limit    int
	bucket   int64
}

func (q crossedQuota) Read(_ context.Context, c agentquota.Counter) agentquota.Reading {
	if c != q.counter {
		return agentquota.Reading{Counter: c, Limit: 1000, Bucket: q.bucket}
	}
	return agentquota.Reading{
		Counter: c, Observed: q.observed, Limit: q.limit, Exceeded: true, Bucket: q.bucket,
	}
}

// steppedUpRegistry is a surface whose caller has already crossed one counter.
func steppedUpRegistry(t *testing.T, quota crossedQuota, scope principal.Scope, egress bool) (*Registry, *recordingApprovals, context.Context) {
	t.Helper()
	staging := &recordingApprovals{}
	r := NewRegistry(staging, auth.NewGate(fullSeatAuthority{}, auth.WithQuota(quota)))
	spec := readToolSpec("search_records")
	spec.RequiredScope, spec.Egress = scope, egress
	if scope != principal.ScopeRead {
		spec.Name = "update_record"
	}
	if egress {
		spec.Name = "send_email"
	}
	r.Register(&servingTool{spec: spec, records: 1})
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(), Scopes: principal.NewScopeSet(scope),
	})
	return r, staging, ctx
}

// BYO-STEP-1, end to end through the surface: a read past the threshold is
// refused AND the question reaches the human who lent the passport. A refusal
// with no question is the control the spec explicitly does not want — it turns
// bulk reading into a dead end rather than into a visible, gated event.
func TestACrossedReadThresholdPutsTheQuestionToTheConnectingHuman(t *testing.T) {
	r, staging, ctx := steppedUpRegistry(t,
		crossedQuota{counter: agentquota.Reads, observed: 2431, limit: 2000, bucket: 42},
		principal.ScopeRead, false)

	_, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`))

	var staged *StepUpStagedError
	if !errors.As(err, &staged) {
		t.Fatalf("a read past its threshold → %v, want a staged step-up", err)
	}
	if !errors.Is(err, apperrors.ErrBudgetExceeded) {
		t.Error("a staged step-up no longer reads as a budget refusal; every budget-aware caller would miss it")
	}
	if errors.Is(err, apperrors.ErrRequiresApproval) {
		t.Error("a staged step-up reads as confirm-first, which tells the caller to retry with an approval_id it cannot get")
	}
	if len(staging.steppedUp) != 1 {
		t.Fatalf("the human was asked %d times, want once", len(staging.steppedUp))
	}
	asked := staging.steppedUp[0].Proposal
	if asked.Counter != agentquota.Reads || asked.Observed != 2431 || asked.Limit != 2000 || asked.Bucket != "42" {
		t.Errorf("the question carried %+v; it must name what was spent, against what, in which window", asked)
	}
	if !strings.Contains(staging.steppedUp[0].Summary, "2431") || !strings.Contains(staging.steppedUp[0].Summary, "2000") {
		t.Errorf("the sentence a human answers has no numbers in it: %q", staging.steppedUp[0].Summary)
	}
}

// BYO-STEP-2: a crossed WRITE threshold asks the same question, so one approval
// releases the batch. It is the same mechanism deliberately — a second decision
// path for "may it keep writing" is a second thing to get wrong.
func TestACrossedWriteThresholdAsksTheSameQuestion(t *testing.T) {
	r, staging, ctx := steppedUpRegistry(t,
		crossedQuota{counter: agentquota.Writes, observed: 240, limit: 200, bucket: 9},
		principal.ScopeWrite, false)

	_, err := r.Invoke(ctx, "update_record", json.RawMessage(`{}`))

	var staged *StepUpStagedError
	if !errors.As(err, &staged) {
		t.Fatalf("a write past its threshold → %v, want a staged step-up", err)
	}
	if len(staging.steppedUp) != 1 || staging.steppedUp[0].Proposal.Counter != agentquota.Writes {
		t.Fatalf("the human was asked %+v", staging.steppedUp)
	}
	if strings.Contains(staging.steppedUp[0].Summary, "records") {
		t.Errorf("a write step-up describes itself in records: %q", staging.steppedUp[0].Summary)
	}
}

// BYO-STEP-3: a hard stop reaches NO inbox. Staging it would put a question in
// front of a human that approving cannot answer — the release path refuses a
// non-releasable counter outright — so the human's yes would do nothing and the
// agent would wait for it.
func TestAHardStopNeverReachesAHumansInbox(t *testing.T) {
	r, staging, ctx := steppedUpRegistry(t,
		crossedQuota{counter: agentquota.Egress, observed: 21, limit: 20, bucket: 3},
		principal.ScopeSend, true)

	_, err := r.Invoke(ctx, "send_email", json.RawMessage(`{}`))

	var overQuota *auth.QuotaExceededError
	if !errors.As(err, &overQuota) {
		t.Fatalf("a send past its ceiling → %v, want a plain quota refusal", err)
	}
	var staged *StepUpStagedError
	if errors.As(err, &staged) {
		t.Error("a hard stop was staged as a question a human could answer")
	}
	if len(staging.steppedUp) != 0 {
		t.Errorf("a hard stop reached an inbox %d times", len(staging.steppedUp))
	}
}

// The suspension ceiling is a hard stop too, and it is checked first — so a
// suspended agent's read is refused on CALLS and asks nobody anything, even
// though reads are releasable.
func TestASuspendedAgentIsRefusedWithoutAskingAnyone(t *testing.T) {
	r, staging, ctx := steppedUpRegistry(t,
		crossedQuota{counter: agentquota.Calls, observed: 1001, limit: 1000, bucket: 3},
		principal.ScopeRead, false)

	_, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`))

	if !errors.Is(err, apperrors.ErrBudgetExceeded) {
		t.Fatalf("a suspended agent → %v, want a budget refusal", err)
	}
	if len(staging.steppedUp) != 0 {
		t.Error("a suspended agent's refusal was put to a human, who cannot release it")
	}
}

// A human who has already said NO is not asked again. Without this an agent
// looping on a refusal re-asks a question its human just answered, once per
// call, and the refusal they intended becomes a stream of notifications.
func TestAQuestionAlreadyRefusedIsNotAskedAgain(t *testing.T) {
	r, staging, ctx := steppedUpRegistry(t,
		crossedQuota{counter: agentquota.Reads, observed: 2431, limit: 2000, bucket: 42},
		principal.ScopeRead, false)
	staging.stepUpDeclined = true

	_, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`))

	var staged *StepUpStagedError
	if errors.As(err, &staged) {
		t.Fatal("the agent was told a human is looking at a question that was never staged")
	}
	if !errors.Is(err, apperrors.ErrBudgetExceeded) {
		t.Errorf("a declined step-up → %v, want the quota refusal to stand on its own", err)
	}
}

// A staging that FAILS leaves the quota refusal as the answer. Reporting the
// staging failure instead would tell the agent to retry the staging, which is
// our problem and not one it can do anything about — and would hide the refusal
// that is the real reason the call did not run.
func TestAFailedStagingLeavesTheQuotaRefusalAsTheAnswer(t *testing.T) {
	r, staging, ctx := steppedUpRegistry(t,
		crossedQuota{counter: agentquota.Reads, observed: 2431, limit: 2000, bucket: 42},
		principal.ScopeRead, false)
	staging.stepUpErr = errors.New("the inbox is unreachable")

	_, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`))

	if !errors.Is(err, apperrors.ErrBudgetExceeded) {
		t.Fatalf("a step-up whose staging failed → %v, want the quota refusal", err)
	}
	if strings.Contains(err.Error(), "unreachable") {
		t.Errorf("the staging failure crossed the trust boundary to the agent: %q", err)
	}
}

// A surface with no approvals engine still refuses, and refuses the same way.
// The Surface-B runner composes one; a quota refusal there has nowhere to land
// and must not become an error about the composition.
func TestASurfaceWithNoInboxStillRefusesOnTheQuota(t *testing.T) {
	quota := crossedQuota{counter: agentquota.Reads, observed: 2431, limit: 2000, bucket: 42}
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}, auth.WithQuota(quota)))
	r.Register(&servingTool{spec: readToolSpec("search_records"), records: 1})
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(), Scopes: principal.NewScopeSet(principal.ScopeRead),
	})

	_, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`))

	if !errors.Is(err, apperrors.ErrBudgetExceeded) {
		t.Fatalf("a quota refusal with no inbox → %v, want the refusal itself", err)
	}
}

// The agent is told what to DO, and the two rungs differ in exactly that: a
// step-up says wait for a human and retry unchanged, a hard stop says stop. An
// agent told to wait for an approval nobody can grant waits forever, and one
// told to stop when a human could release it gives up early.
func TestTheTwoRungsGiveTheAgentDifferentInstructions(t *testing.T) {
	d := NewDispatcher(nil, bindAuthenticated, "test", "1")

	stepUp := d.explain("search_records", &StepUpStagedError{
		ApprovalID: ids.New[ids.ApprovalKind](), Counter: agentquota.Reads,
	})
	hardStop := d.explain("send_email", &auth.QuotaExceededError{
		Tool:    "send_email",
		Reading: agentquota.Reading{Counter: agentquota.Egress, Observed: 21, Limit: 20, Exceeded: true},
	})

	if !strings.Contains(stepUp, "repeat this call unchanged") || !strings.Contains(stepUp, "Do not send an approval_id") {
		t.Errorf("a step-up does not tell the agent how to continue: %q", stepUp)
	}
	if !strings.Contains(hardStop, "no approval lifts") {
		t.Errorf("a hard stop does not say that waiting will not help: %q", hardStop)
	}
	if strings.Contains(hardStop, "asked whether it may continue") {
		t.Errorf("a hard stop claims a human is looking at it: %q", hardStop)
	}
}

// The third shape, which is neither: a RELEASABLE counter whose question nobody
// is holding — the human declined it, or the surface has no inbox. It must not
// borrow the hard stop's words, because the refusal it quotes says in the same
// breath that a release would end this. A reader told both is told nothing.
func TestADeclinedStepUpDoesNotClaimNoApprovalCouldLiftIt(t *testing.T) {
	d := NewDispatcher(nil, bindAuthenticated, "test", "1")

	answer := d.explain("search_records", &auth.QuotaExceededError{
		Tool:    "search_records",
		Reading: agentquota.Reading{Counter: agentquota.Reads, Observed: 4000, Limit: 4000, Exceeded: true},
	})

	if strings.Contains(answer, "no approval lifts it") {
		t.Errorf("a releasable counter was described as one no approval lifts, contradicting the refusal it quotes: %q", answer)
	}
	if !strings.Contains(answer, "no request to continue is open") {
		t.Errorf("the answer does not say what is actually true — that nothing is pending: %q", answer)
	}
	if !strings.Contains(answer, "after the window rolls") {
		t.Errorf("the answer does not tell the agent what ends this: %q", answer)
	}
}

// spentShare is a cost reader whose share is already gone.
type spentShare struct {
	reading agentquota.Reading
	asked   int
}

func (s *spentShare) CostShare(context.Context) agentquota.Reading {
	s.asked++
	return s.reading
}

// MCP-SESS-COST is soft, and SAYING SO is the whole control: the answer is
// served, and it carries the fact. A counter that were merely counted, with
// nothing ever showing it, would govern nothing at all.
func TestASpentBudgetShareWarnsOnTheAnswerAndWithholdsNothing(t *testing.T) {
	share := &spentShare{reading: agentquota.Reading{
		Counter: agentquota.Cost, Observed: 41_000, Limit: 40_000, Exceeded: true,
	}}
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}), WithCostShare(share))
	r.Register(&servingTool{spec: readToolSpec("search_records"), records: 1})
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(), Scopes: principal.NewScopeSet(principal.ScopeRead),
	})

	out, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("a spent budget share refused the call; the spec calls this quota soft: %v", err)
	}
	if !strings.Contains(string(out), warningCostShare) {
		t.Errorf("the answer does not carry the budget-share warning: %s", out)
	}
	if !strings.Contains(string(out), "41000") {
		t.Errorf("the warning does not say what was spent: %s", out)
	}
}

// And under the share, nothing is said. A warning present on every answer says
// as little as one present on none.
func TestAnAnswerUnderTheBudgetShareSaysNothingAboutIt(t *testing.T) {
	share := &spentShare{reading: agentquota.Reading{Counter: agentquota.Cost, Observed: 10, Limit: 40_000}}
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}), WithCostShare(share))
	r.Register(&servingTool{spec: readToolSpec("search_records"), records: 1})
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(), Scopes: principal.NewScopeSet(principal.ScopeRead),
	})

	out, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(out), warningCostShare) {
		t.Errorf("an answer well under the share carried the warning anyway: %s", out)
	}
	if share.asked != 1 {
		t.Errorf("the share was read %d times for one answer", share.asked)
	}
}
