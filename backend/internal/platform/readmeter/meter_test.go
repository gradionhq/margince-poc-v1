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

// agentCtx is a call carrying a Passport — the only kind of caller this bound
// is written for.
func agentCtx(t *testing.T) context.Context {
	t.Helper()
	ctx := principal.WithWorkspaceID(t.Context(), ids.New[ids.WorkspaceKind]().UUID)
	return principal.WithActor(ctx, principal.Principal{
		Type:       principal.PrincipalAgent,
		ID:         "agent:reader",
		PassportID: ids.New[ids.PassportKind]().UUID,
	})
}

// humanCtx is a call with no Passport. Humans are outside this control: their
// authority is RBAC at the store.
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
// unreachable counter and a caller who is not metered both make resolve say
// no, and only one of them may be refused. Conflating them would deny the
// product to its own users on a bound written for agents.
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

// A call with no actor at all — a background job, an unauthenticated path
// reaching the meter by mistake — carries no Passport and so is not metered.
// It is asserted rather than assumed because the alternative reading (treat
// "no actor" as fail-closed) would break every internal read path.
func TestACallWithNoActorIsNotMetered(t *testing.T) {
	meter := New(nil, DefaultLimit, DefaultWindow)

	if meter.Read(t.Context()).Exceeded {
		t.Error("a call with no actor was refused by a bound that only governs Passports")
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

// Granting headroom to a caller the meter does not govern is a wiring fault,
// not a silent no-op: a step-up approval that quietly grants nothing would
// leave the agent refused forever with a human believing they had released it.
func TestGrantingHeadroomToAnUngovernedCallerIsAnError(t *testing.T) {
	meter := New(nil, DefaultLimit, DefaultWindow)

	if err := meter.Grant(humanCtx(t), 500); err == nil {
		t.Error("granting step-up headroom to a human reported success")
	}
	if err := meter.Grant(agentCtx(t), 0); err == nil {
		t.Error("a zero-record step-up grant reported success; it releases nothing")
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

// A window's grant is keyed to that window, not to the Passport at large, so
// releasing one window's reading cannot silently release the next one's.
func TestTheGrantIsScopedToTheWindowItWasGivenIn(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(nil, DefaultLimit, time.Hour, func() time.Time { return now })
	ws, passport := ids.New[ids.WorkspaceKind]().UUID, ids.New[ids.PassportKind]().UUID.String()

	first := meter.grantKey(ws, passport, meter.bucket())
	now = now.Add(time.Hour)
	second := meter.grantKey(ws, passport, meter.bucket())

	if first == second {
		t.Error("one window's step-up grant is stored under the next window's key, so it never expires")
	}
}

// The count and the grant are separate keys on purpose: a step-up must not
// erase the evidence of how much the agent has already been handed, because
// "how many records did it see" has to stay answerable after the human said
// continue.
func TestTheGrantDoesNotShareTheCountersKey(t *testing.T) {
	meter := New(nil, DefaultLimit, DefaultWindow)
	ws, passport := ids.New[ids.WorkspaceKind]().UUID, ids.New[ids.PassportKind]().UUID.String()

	if meter.countKey(ws, passport, 1) == meter.grantKey(ws, passport, 1) {
		t.Error("a step-up grant increments the same key as the read count, erasing the audit answer")
	}
}

// Two Passports in one workspace, and one Passport across two workspaces, each
// get their own counter. Sharing either would let one agent's reading refuse
// another's.
func TestEachPassportAndWorkspaceCountsSeparately(t *testing.T) {
	meter := New(nil, DefaultLimit, DefaultWindow)
	ws, other := ids.New[ids.WorkspaceKind]().UUID, ids.New[ids.WorkspaceKind]().UUID
	passport, second := ids.New[ids.PassportKind]().UUID.String(), ids.New[ids.PassportKind]().UUID.String()

	if meter.countKey(ws, passport, 1) == meter.countKey(ws, second, 1) {
		t.Error("two Passports in one workspace share a read counter")
	}
	if meter.countKey(ws, passport, 1) == meter.countKey(other, passport, 1) {
		t.Error("one Passport shares a read counter across two workspaces")
	}
}
