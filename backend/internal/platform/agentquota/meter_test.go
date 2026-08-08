// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentquota

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// agentCtx is an agent call carrying a Passport — the caller these bounds are
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

// governedCounters is every counter that can refuse. Tests that assert a
// property of "the bound" walk it rather than naming one, so a counter added
// later inherits the property instead of quietly opting out of it.
var governedCounters = []Counter{Reads, Writes, Egress, Calls}

// The sharpest property in the package: a meter that cannot reach its counter
// does not know whether the threshold has been passed, and a control that
// cannot answer must not answer "no". Written first because failing OPEN here
// would make the whole bound removable by stopping one process — and it must
// hold for EVERY governed counter, not only the one that shipped first.
func TestAnUnreachableCounterRefusesTheAgentRatherThanAdmittingIt(t *testing.T) {
	meter := New(nil, Limits{}, DefaultWindow)
	ctx := agentCtx(t)

	for _, c := range governedCounters {
		reading := meter.Read(ctx, c)
		if !reading.Exceeded {
			t.Errorf("a meter with no counter admitted %s; the bound is removable by stopping Redis", c)
		}
		if reading.Limit != meter.Limit(c) {
			t.Errorf("the %s refusal reported limit %d, not the configured %d — a caller cannot act on a limit that is not the real one",
				c, reading.Limit, meter.Limit(c))
		}
		if reading.Counter != c {
			t.Errorf("a reading of %s named itself %s; a refusal that misnames its quota cannot be acted on", c, reading.Counter)
		}
	}
}

// The other side of the same branch, and the reason it is a branch at all: an
// unreachable counter and a caller these bounds do not govern are both "no",
// and only one of them may be refused. Conflating them would deny the product
// to its own users on bounds written for agents.
func TestAHumanIsNotMeteredEvenWhenTheCounterIsUnreachable(t *testing.T) {
	meter := New(nil, Limits{}, DefaultWindow)
	ctx := humanCtx(t)

	for _, c := range governedCounters {
		reading := meter.Read(ctx, c)
		if reading.Exceeded {
			t.Errorf("a human session was refused by the agent %s bound", c)
		}
		if reading.Observed != 0 {
			t.Errorf("a human accrued %d metered %s; humans are outside this control", reading.Observed, c)
		}
	}
}

// A call with no actor at all — a background job, an internal read reaching the
// meter by mistake — is not an agent and so is not metered. Asserted rather
// than assumed because the alternative reading (treat "no actor" as
// fail-closed) would refuse every internal path in the product.
func TestACallWithNoActorIsNotMetered(t *testing.T) {
	meter := New(nil, Limits{}, DefaultWindow)

	if meter.Read(t.Context(), Reads).Exceeded {
		t.Error("a call with no actor was refused by a bound that only governs agents")
	}
}

// Charging an unmetered caller is a no-op rather than an error: the charge
// points are shared by both doors and both kinds of caller, and a handler that
// had to ask "is this an agent?" before every charge would eventually forget.
func TestChargingAnUnmeteredCallerRecordsNothingAndDoesNotFail(t *testing.T) {
	meter := New(nil, Limits{}, DefaultWindow)

	if err := meter.Consume(humanCtx(t), Reads, 25); err != nil {
		t.Errorf("charging a human failed: %v", err)
	}
	if err := meter.Consume(agentCtx(t), Reads, 0); err != nil {
		t.Errorf("charging an empty page failed: %v", err)
	}
}

// An agent principal that carries no Passport is STILL metered, under its own
// principal id. Every agent this product mints carries one
// (identity.AgentIdentity.Principal), so this is not a live path — but keying
// on the Passport alone would make "an agent without one" a silent exemption
// from every bound, and the next principal shape that appears would inherit it.
func TestAnAgentWithNoPassportIsStillMetered(t *testing.T) {
	meter := New(nil, Limits{}, DefaultWindow)
	ctx := principal.WithWorkspaceID(t.Context(), ids.New[ids.WorkspaceKind]().UUID)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:passportless",
	})

	if !meter.Read(ctx, Reads).Exceeded {
		t.Error("an agent with no Passport escaped the read bound entirely")
	}
}

// The window bucket comes from the injected clock, so rollover is a property
// asserted by advancing time rather than by sleeping (T11). Two moments inside
// one window share a bucket; two moments either side of it do not.
func TestTheWindowBucketRollsOverWithTheInjectedClock(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	meter := NewWithClock(nil, Limits{}, time.Hour, clock)

	first := meter.Bucket()
	now = now.Add(59 * time.Minute)
	if meter.Bucket() != first {
		t.Error("two moments inside one window landed in different buckets")
	}
	now = now.Add(2 * time.Minute)
	if meter.Bucket() == first {
		t.Error("a moment past the window boundary stayed in the previous bucket")
	}
}

// The counter's expiry must outlive its window and not by so much that it
// survives into a later one. An expiry shorter than the window under-counts
// (the agent's charges vanish mid-window); a much longer one over-counts.
func TestTheCounterOutlivesItsWindowByTheSkewSlackOnly(t *testing.T) {
	meter := New(nil, Limits{}, 24*time.Hour)

	ttl := time.Duration(meter.ttlSeconds()) * time.Second

	if ttl <= 24*time.Hour {
		t.Errorf("a counter with a %s expiry dies inside its own 24h window", ttl)
	}
	if ttl > 25*time.Hour {
		t.Errorf("a counter with a %s expiry survives into the window after its own", ttl)
	}
}

// Two Passports in one workspace, one Passport across two workspaces, and one
// Passport across two counters each get their own key. Sharing any of the three
// would let one agent's activity refuse another's — or let a heavy reader
// exhaust its own send allowance.
func TestEachPassportWorkspaceAndCounterCountsSeparately(t *testing.T) {
	meter := New(nil, Limits{}, DefaultWindow)
	ws, other := ids.New[ids.WorkspaceKind]().UUID, ids.New[ids.WorkspaceKind]().UUID
	passport, second := ids.New[ids.PassportKind]().String(), ids.New[ids.PassportKind]().String()

	if meter.countKey(ws, passport, Reads, 1) == meter.countKey(ws, second, Reads, 1) {
		t.Error("two Passports in one workspace share a counter")
	}
	if meter.countKey(ws, passport, Reads, 1) == meter.countKey(other, passport, Reads, 1) {
		t.Error("one Passport shares a counter across two workspaces")
	}
	if meter.countKey(ws, passport, Reads, 1) == meter.countKey(ws, passport, Writes, 1) {
		t.Error("one Passport's reads and writes share a counter")
	}
	if meter.countKey(ws, passport, Reads, 1) == meter.releaseKey(ws, passport, Reads, 1) {
		t.Error("the charge and the release a human granted share a key; one would erase the other")
	}
}

// The key prefix is load-bearing history, not a name. Renaming it orphans every
// live read window at deploy, and an orphaned counter reads as zero — one free
// full allowance per connected Passport, on the exact release meant to tighten
// the bound. Pinned so the rename is a deliberate act with a failing test in
// front of it.
func TestTheCounterKeyPrefixIsPinnedToWhatIsAlreadyDeployed(t *testing.T) {
	meter := New(nil, Limits{}, DefaultWindow)
	ws := ids.New[ids.WorkspaceKind]().UUID
	key := meter.countKey(ws, "passport", Reads, 7)

	if want := "msr:" + ws.String() + ":passport:reads:7"; key != want {
		t.Errorf("counter key is %q, want %q — changing this resets every live window to zero", key, want)
	}
}

// The rebind is what keeps the two halves of a bound on ONE counter: compose
// builds a fail-closed meter, hands the SAME pointer to the gate that refuses
// and the registry that charges, and cmd rebinds it once Redis is known. If the
// rebind did not reach a holder, that holder would keep enforcing against a
// meter nothing pays into.
func TestRebindingReachesEveryHolderOfTheSharedPointer(t *testing.T) {
	shared := New(nil, Limits{}, DefaultWindow)
	held := shared // the gate's copy of the pointer, taken before the rebind

	shared.RebindFrom(New(nil, Limits{Reads: 50}, time.Hour))

	if held.Limit(Reads) != 50 {
		t.Errorf("a holder that took the pointer before the rebind still reads limit %d, not the rebound 50", held.Limit(Reads))
	}
}

// A non-positive window is a misconfiguration, not an instruction to divide by
// zero when the bucket is computed. It falls back to the spec's default for the
// same reason a limit does.
func TestAnUnusableConfiguredWindowFallsBackToTheSpecDefault(t *testing.T) {
	meter := NewWithClock(nil, Limits{}, 0, func() time.Time {
		return time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	})

	if meter.window != DefaultWindow {
		t.Errorf("a zero window resolved to %s, not the %s default", meter.window, DefaultWindow)
	}
	if meter.Bucket() <= 0 {
		t.Error("the window fallback did not produce a usable bucket")
	}
}

// Unmetered and "cannot reach the counter" must never be the same thing. One is
// a composition that declared no bound; the other is a bound that could not
// answer, and only the second may refuse. Collapsing them would make every
// Redis outage look like a deliberate opt-out.
func TestAnUnmeteredCompositionAdmitsWhereAnUnreachableOneRefuses(t *testing.T) {
	declared, unreachable := Unmetered(), New(nil, Limits{}, DefaultWindow)
	ctx := agentCtx(t)

	for _, c := range governedCounters {
		if declared.Read(ctx, c).Exceeded {
			t.Errorf("a composition that declared no bound refused %s", c)
		}
		if !unreachable.Read(ctx, c).Exceeded {
			t.Errorf("a bound that cannot reach its counter admitted %s", c)
		}
	}
	if err := declared.Consume(ctx, Reads, 5000); err != nil {
		t.Errorf("charging an unmetered composition failed: %v", err)
	}
}

// The rebind carries the unbounded flag too, so a Server that starts unmetered
// and is later rebound to a real meter actually becomes bounded — and one
// rebound to Unmetered does not keep enforcing a counter nothing pays into.
func TestRebindingCarriesWhetherTheCompositionIsBounded(t *testing.T) {
	shared := New(nil, Limits{}, DefaultWindow)

	shared.RebindFrom(Unmetered())

	if shared.Read(agentCtx(t), Reads).Exceeded {
		t.Error("rebinding to an unmetered composition left the meter refusing")
	}
}

// A window is a caller-supplied duration and nothing stops it being sub-second.
// Bucketing in whole seconds truncated that to zero and divided by it, so the
// first read of a 500ms window panicked rather than answering.
func TestASubSecondWindowBucketsRatherThanDividingByZero(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(nil, Limits{}, 500*time.Millisecond, func() time.Time { return now })

	first := meter.Bucket()
	now = now.Add(600 * time.Millisecond)

	if meter.Bucket() == first {
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
	if meter.Read(t.Context(), Reads).Exceeded {
		t.Fatal("an unmetered composition refused a call")
	}

	bounded := New(nil, Limits{}, DefaultWindow)
	ctx := principal.WithActor(t.Context(), principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:no-workspace",
		PassportID: ids.New[ids.PassportKind]().UUID,
	})

	if !bounded.Read(ctx, Reads).Exceeded {
		t.Error("an agent with no workspace bound escaped the read bound entirely")
	}
}

// And the other side of that branch stays intact: a human with no workspace is
// still outside the control, not refused by it.
func TestAHumanWithNoWorkspaceIsStillUnmetered(t *testing.T) {
	meter := New(nil, Limits{}, DefaultWindow)
	ctx := principal.WithActor(t.Context(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:rep",
	})

	if meter.Read(ctx, Reads).Exceeded {
		t.Error("a human with no workspace was refused by the agent read bound")
	}
}

// Every reading names the window it is a reading OF, because a release has to
// land in the window the human was shown. A reading with no bucket would send an
// approval answered an hour later into whatever window happened to be current.
func TestEveryReadingNamesItsOwnWindow(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(nil, Limits{}, time.Hour, func() time.Time { return now })
	ctx := agentCtx(t)

	first := meter.Read(ctx, Reads).Bucket
	if first != meter.Bucket() {
		t.Errorf("a reading named window %d while the meter is in %d", first, meter.Bucket())
	}
	now = now.Add(2 * time.Hour)
	if meter.Read(ctx, Reads).Bucket == first {
		t.Error("a reading taken two windows later named the earlier window")
	}
	if meter.Read(humanCtx(t), Reads).Bucket == 0 {
		t.Error("an unmetered reading named no window; the field must be answerable on every path")
	}
}

// A counter value Redis holds but this meter did not write — a string, a stray
// SET by an operator — is an ERROR and therefore a refusal, never a zero.
// Reading an unparseable value as "nothing spent" is how one corrupted key
// becomes an unbounded allowance.
func TestACounterValueThisMeterDidNotWriteIsAnErrorNotAZero(t *testing.T) {
	for _, v := range []any{"not-a-number", 42, []string{"1"}} {
		if _, err := asCount(v); err == nil {
			t.Errorf("counter value %#v parsed as a count instead of failing", v)
		}
	}
	if n, err := asCount(nil); err != nil || n != 0 {
		t.Errorf("an absent counter read as (%d, %v), want (0, nil) — nothing charged this window", n, err)
	}
	if n, err := asCount("17"); err != nil || n != 17 {
		t.Errorf("a written counter read as (%d, %v), want (17, nil)", n, err)
	}
}
