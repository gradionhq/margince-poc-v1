// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package readmeter

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// agentCtx is an agent call carrying a Passport — the caller this bound is
// written for.
func agentCtx(t *testing.T) context.Context {
	t.Helper()
	ctx := principal.WithWorkspaceID(t.Context(), ids.New[ids.WorkspaceKind]().UUID)
	return principal.WithActor(ctx, principal.Principal{
		Type:       principal.PrincipalAgent,
		ID:         "agent:reader",
		PassportID: ids.New[ids.PassportKind]().UUID,
	})
}

// humanCtx is a human call. Humans are outside this control: their authority
// is RBAC at the store, and they answered for the action themselves.
func humanCtx(t *testing.T) context.Context {
	t.Helper()
	ctx := principal.WithWorkspaceID(t.Context(), ids.New[ids.WorkspaceKind]().UUID)
	return principal.WithActor(ctx, principal.Principal{
		Type:   principal.PrincipalHuman,
		ID:     "human:reader",
		UserID: ids.New[ids.UserKind]().UUID,
	})
}

// The sharpest property in the package: a meter that cannot reach its counter
// does not know whether the threshold has been passed, and a control that
// cannot answer must not answer "no". Written first because failing OPEN here
// would make the whole bound removable by stopping one process.
func TestAnUnreachableCounterRefusesTheAgentRatherThanAdmittingIt(t *testing.T) {
	meter := New(nil, DefaultLimit, DefaultWindow)

	reading := meter.Read(agentCtx(t))

	if !reading.Exceeded {
		t.Error("a meter with no counter admitted an agent read; the bound is removable by stopping Redis")
	}
	if reading.Limit != DefaultLimit {
		t.Errorf("the refusal reported limit %d, not the configured %d — a caller cannot act on a limit that is not the real one",
			reading.Limit, DefaultLimit)
	}
}

// The other side of the same branch, and the reason it is a branch at all: an
// unreachable counter and a caller this bound does not govern are both "no",
// and only one of them may be refused. Conflating them would deny the product
// to its own users on a bound written for agents.
func TestAHumanIsNotMeteredEvenWhenTheCounterIsUnreachable(t *testing.T) {
	meter := New(nil, DefaultLimit, DefaultWindow)

	reading := meter.Read(humanCtx(t))

	if reading.Exceeded {
		t.Error("a human session was refused by the agent read bound")
	}
	if reading.Observed != 0 {
		t.Errorf("a human accrued %d metered reads; humans are outside this control", reading.Observed)
	}
}

// A call with no actor at all — a background job, an internal read reaching the
// meter by mistake — is not an agent and so is not metered. Asserted rather
// than assumed because the alternative reading (treat "no actor" as
// fail-closed) would refuse every internal read path in the product.
func TestACallWithNoActorIsNotMetered(t *testing.T) {
	meter := New(nil, DefaultLimit, DefaultWindow)

	if meter.Read(t.Context()).Exceeded {
		t.Error("a call with no actor was refused by a bound that only governs agents")
	}
}

// Charging an unmetered caller is a no-op rather than an error: the charge
// points are shared by both doors and both kinds of caller, and a handler that
// had to ask "is this an agent?" before every charge would eventually forget.
func TestChargingAnUnmeteredCallerRecordsNothingAndDoesNotFail(t *testing.T) {
	meter := New(nil, DefaultLimit, DefaultWindow)

	if err := meter.Consume(humanCtx(t), 25); err != nil {
		t.Errorf("charging a human failed: %v", err)
	}
	if err := meter.Consume(agentCtx(t), 0); err != nil {
		t.Errorf("charging an empty page failed: %v", err)
	}
}

// An agent principal that carries no Passport is STILL metered, under its own
// principal id. Every agent this product mints carries one
// (identity.AgentIdentity.Principal), so this is not a live path — but keying
// on the Passport alone would make "an agent without one" a silent exemption
// from the bound, and the next principal shape that appears would inherit it.
func TestAnAgentWithNoPassportIsStillMetered(t *testing.T) {
	meter := New(nil, DefaultLimit, DefaultWindow)
	ctx := principal.WithWorkspaceID(t.Context(), ids.New[ids.WorkspaceKind]().UUID)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:passportless",
	})

	if !meter.Read(ctx).Exceeded {
		t.Error("an agent with no Passport escaped the read bound entirely")
	}
}

// A zero or negative configured limit would make every agent read refuse (or,
// worse under a different sign convention, admit unboundedly). It falls back
// to the spec's default rather than trusting a misconfiguration.
func TestAnUnusableConfiguredLimitFallsBackToTheSpecDefault(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if got := New(nil, limit, DefaultWindow).Limit(); got != DefaultLimit {
			t.Errorf("a configured limit of %d resolved to %d, not the %d default", limit, got, DefaultLimit)
		}
	}
	if got := New(nil, 50, DefaultWindow).Limit(); got != 50 {
		t.Errorf("a configured limit of 50 resolved to %d", got)
	}
}

// The window bucket comes from the injected clock, so rollover is a property
// asserted by advancing time rather than by sleeping (T11). Two moments inside
// one window share a bucket; two moments either side of it do not.
func TestTheWindowBucketRollsOverWithTheInjectedClock(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	meter := NewWithClock(nil, DefaultLimit, time.Hour, clock)

	first := meter.bucket()
	now = now.Add(59 * time.Minute)
	if meter.bucket() != first {
		t.Error("two moments inside one window landed in different buckets")
	}
	now = now.Add(2 * time.Minute)
	if meter.bucket() == first {
		t.Error("a moment past the window boundary stayed in the previous bucket")
	}
}

// The counter's expiry must outlive its window and not by so much that it
// survives into a later one. An expiry shorter than the window under-counts
// (the agent's reads vanish mid-window); a much longer one over-counts.
func TestTheCounterOutlivesItsWindowByTheSkewSlackOnly(t *testing.T) {
	meter := New(nil, DefaultLimit, 24*time.Hour)

	ttl := time.Duration(meter.ttlSeconds()) * time.Second

	if ttl <= 24*time.Hour {
		t.Errorf("a counter with a %s expiry dies inside its own 24h window", ttl)
	}
	if ttl > 25*time.Hour {
		t.Errorf("a counter with a %s expiry survives into the window after its own", ttl)
	}
}

// Two Passports in one workspace, and one Passport across two workspaces, each
// get their own counter. Sharing either would let one agent's reading refuse
// another's.
func TestEachPassportAndWorkspaceCountsSeparately(t *testing.T) {
	meter := New(nil, DefaultLimit, DefaultWindow)
	ws, other := ids.New[ids.WorkspaceKind]().UUID, ids.New[ids.WorkspaceKind]().UUID
	passport, second := ids.New[ids.PassportKind]().String(), ids.New[ids.PassportKind]().String()

	if meter.countKey(ws, passport, 1) == meter.countKey(ws, second, 1) {
		t.Error("two Passports in one workspace share a read counter")
	}
	if meter.countKey(ws, passport, 1) == meter.countKey(other, passport, 1) {
		t.Error("one Passport shares a read counter across two workspaces")
	}
}

// The rebind is what keeps the two halves of the bound on ONE counter: compose
// builds a fail-closed meter, hands the SAME pointer to the gate that refuses
// and the registry that charges, and cmd rebinds it once Redis is known. If the
// rebind did not reach a holder, that holder would keep enforcing against a
// meter nothing pays into.
func TestRebindingReachesEveryHolderOfTheSharedPointer(t *testing.T) {
	shared := New(nil, DefaultLimit, DefaultWindow)
	held := shared // the gate's copy of the pointer, taken before the rebind

	shared.RebindFrom(New(nil, 50, time.Hour))

	if held.Limit() != 50 {
		t.Errorf("a holder that took the pointer before the rebind still reads limit %d, not the rebound 50", held.Limit())
	}
}

// A non-positive window is a misconfiguration, not an instruction to divide by
// zero when the bucket is computed. It falls back to the spec's default for the
// same reason the limit does.
func TestAnUnusableConfiguredWindowFallsBackToTheSpecDefault(t *testing.T) {
	meter := NewWithClock(nil, DefaultLimit, 0, func() time.Time {
		return time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	})

	if meter.window != DefaultWindow {
		t.Errorf("a zero window resolved to %s, not the %s default", meter.window, DefaultWindow)
	}
	if meter.bucket() <= 0 {
		t.Error("the window fallback did not produce a usable bucket")
	}
}

// Unmetered and "cannot reach the counter" must never be the same thing. One is
// a composition that declared no bound; the other is a bound that could not
// answer, and only the second may refuse. Collapsing them would make every
// Redis outage look like a deliberate opt-out.
func TestAnUnmeteredCompositionAdmitsWhereAnUnreachableOneRefuses(t *testing.T) {
	declared, unreachable := Unmetered(), New(nil, DefaultLimit, DefaultWindow)
	ctx := agentCtx(t)

	if declared.Read(ctx).Exceeded {
		t.Error("a composition that declared no read bound refused an agent read")
	}
	if !unreachable.Read(ctx).Exceeded {
		t.Error("a bound that cannot reach its counter admitted an agent read")
	}
	if err := declared.Consume(ctx, 5000); err != nil {
		t.Errorf("charging an unmetered composition failed: %v", err)
	}
}

// The rebind carries the unbounded flag too, so a Server that starts unmetered
// and is later rebound to a real meter actually becomes bounded — and one
// rebound to Unmetered does not keep enforcing a counter nothing pays into.
func TestRebindingCarriesWhetherTheCompositionIsBounded(t *testing.T) {
	shared := New(nil, DefaultLimit, DefaultWindow)

	shared.RebindFrom(Unmetered())

	if shared.Read(agentCtx(t)).Exceeded {
		t.Error("rebinding to an unmetered composition left the meter refusing")
	}
}

// A window is a caller-supplied duration and nothing stops it being sub-second.
// Bucketing in whole seconds truncated that to zero and divided by it, so the
// first read of a 500ms window panicked rather than answering.
func TestASubSecondWindowBucketsRatherThanDividingByZero(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(nil, DefaultLimit, 500*time.Millisecond, func() time.Time { return now })

	first := meter.bucket()
	now = now.Add(600 * time.Millisecond)

	if meter.bucket() == first {
		t.Error("a 500ms window did not roll after 600ms")
	}
	if meter.ttlSeconds() < 1 {
		t.Errorf("a sub-second window asked Redis for a %ds expiry, which never expires", meter.ttlSeconds())
	}
}

// An agent whose counter cannot be NAMED is refused, not admitted. A call with
// no workspace bound is the live shape of this: the gate's MCP path rejects it
// before the quota, but the REST path has no such check, so a meter that
// admitted it would hand out a free pass on a wiring fault.
func TestAnAgentWithNoWorkspaceIsRefusedRatherThanUnmetered(t *testing.T) {
	meter := Unmetered() // even an unmetered composition must not be the reason
	if meter.Read(t.Context()).Exceeded {
		t.Fatal("an unmetered composition refused a call")
	}

	bounded := New(nil, DefaultLimit, DefaultWindow)
	ctx := principal.WithActor(t.Context(), principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:no-workspace",
		PassportID: ids.New[ids.PassportKind]().UUID,
	})

	if !bounded.Read(ctx).Exceeded {
		t.Error("an agent with no workspace bound escaped the read bound entirely")
	}
}

// And the other side of that branch stays intact: a human with no workspace is
// still outside the control, not refused by it.
func TestAHumanWithNoWorkspaceIsStillUnmetered(t *testing.T) {
	meter := New(nil, DefaultLimit, DefaultWindow)
	ctx := principal.WithActor(t.Context(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:rep",
	})

	if meter.Read(ctx).Exceeded {
		t.Error("a human with no workspace was refused by the agent read bound")
	}
}
