// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The MCP-SESS-READS bound on the REST door, over the REAL HTTP stack with a
// real passport.
//
// The shared app harness composes agentquota.Unmetered() — it serves no Redis,
// and a meter that cannot reach its counter fails closed, which would refuse
// every agent read in suites testing something else. That is the right default
// for those suites and the wrong one for this property, so this file builds its
// own Server with a live bound. Without it nothing would prove the REST door
// enforces the bound at all.
//
// The window is spent by charging the meter directly: that IS a window already
// spent through the MCP door, which is the shape this bound has to answer —
// one credential presenting at a second door must meet the same counter.
//
import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// boundedApp is the app under a live read bound, plus the meter itself so a
// test can put the window into whatever state it is about.
func boundedApp(t *testing.T, slug string, limit int) (*apptest.AppEnv, *agentquota.Meter) {
	t.Helper()
	meter := agentquota.New(budgettest.Client(t), agentquota.Limits{Reads: limit}, time.Hour)
	e := apptest.SetupAppWithOptions(t, compose.WithAgentQuota(meter))
	e.Slug = slug
	apptest.BootstrapWorkspaceSession(t, e, "Read Bound", slug+"@fable.test", "Admin")
	return e, meter
}

// asPassport builds the context the meter counts one passport against — the
// same binding the gate resolves from a presented Bearer.
func asPassport(t *testing.T, e *apptest.AppEnv, passport ids.UUID) context.Context {
	t.Helper()
	var ws ids.UUID
	if err := e.Owner.QueryRow(t.Context(), `SELECT id FROM workspace LIMIT 1`).Scan(&ws); err != nil {
		t.Fatalf("reading the workspace id: %v", err)
	}
	ctx := principal.WithWorkspaceID(t.Context(), ws)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:" + passport.String(), PassportID: passport,
	})
}

// spendWindow charges the meter against one passport, as the MCP door would.
func spendWindow(t *testing.T, e *apptest.AppEnv, meter *agentquota.Meter, passport ids.UUID, records int) {
	t.Helper()
	if err := meter.Consume(asPassport(t, e, passport), agentquota.Reads, records); err != nil {
		t.Fatalf("spending the window: %v", err)
	}
}

// readsCharged is what the window has actually observed against one passport.
func readsCharged(t *testing.T, e *apptest.AppEnv, meter *agentquota.Meter, passport ids.UUID) int {
	t.Helper()
	return meter.Read(asPassport(t, e, passport), agentquota.Reads).Observed
}

// A passport that has spent its window is refused on the REST door too. Before
// this, contractAPI built its gate with no bound and the agent gate returned
// early on every non-mutating method, so /v1 sat outside the control entirely.
func TestASpentWindowRefusesTheSamePassportOnTheRestDoor(t *testing.T) {
	e, meter := boundedApp(t, "read-bound-rest", 100)
	bearer, passport := passportWithID(t, e, "reading agent", "read")
	seedPeople(t, e, 2)

	// Served under the bound first, so the refusal below is the bound firing
	// rather than the route being closed to agents for some other reason.
	if status := e.Call(t, "GET", "/v1/people", nil, bearer, nil); status != http.StatusOK {
		t.Fatalf("an agent read under the bound → %d, want 200", status)
	}

	spendWindow(t, e, meter, passport, 100)

	// The QUOTA response specifically: a 403 or a 500 would also fail an
	// is-not-200 check while meaning something entirely different.
	var problem struct {
		Code string `json:"code"`
	}
	status := e.Call(t, "GET", "/v1/people", nil, bearer, &problem)

	if status != http.StatusTooManyRequests {
		t.Errorf("a passport that spent its window → %d, want 429; /v1 is outside the bound", status)
	}
	if problem.Code != "rate_limited" {
		t.Errorf("the refusal carried code %q, want \"rate_limited\" — a caller branches on the code, not the prose", problem.Code)
	}
}

// A HUMAN session is never touched by it, however much any agent has read. A
// busy agent must not be able to lock its own operator out of the product it is
// acting inside.
func TestAHumanSessionIsUnaffectedByASpentAgentWindow(t *testing.T) {
	e, meter := boundedApp(t, "read-bound-human", 100)
	_, passport := passportWithID(t, e, "reading agent", "read")
	seedPeople(t, e, 2)

	spendWindow(t, e, meter, passport, 500)

	if status := e.Call(t, "GET", "/v1/people", nil, nil, nil); status != http.StatusOK {
		t.Errorf("a human read → %d after an agent spent its window; humans are outside this bound", status)
	}
}

// The door that REFUSES on a counter pays into it. The refusal above proves /v1
// consults MCP-SESS-READS; this proves it charges, which is what makes the
// refusal reachable by reading rather than only by having read elsewhere.
//
// Counted in RECORDS, not requests: a page of one and a page of two hundred are
// not the same read, and a per-request charge would price them alike.
func TestARestReadChargesTheRecordsItServed(t *testing.T) {
	e, meter := boundedApp(t, "read-bound-charge", 100)
	bearer, passport := passportWithID(t, e, "reading agent", "read")
	seedPeople(t, e, 3)

	if status := e.Call(t, "GET", "/v1/people", nil, bearer, nil); status != http.StatusOK {
		t.Fatalf("an agent read under the bound → %d, want 200", status)
	}

	if charged := readsCharged(t, e, meter, passport); charged != 3 {
		t.Errorf("a page of 3 records charged %d against MCP-SESS-READS, want 3; "+
			"a door that refuses on a counter nothing increments bounds nobody", charged)
	}
}

// A single record read charges one, so the counter measures what was handed
// over rather than how the caller happened to ask for it.
func TestARestSingleRecordReadChargesOne(t *testing.T) {
	e, meter := boundedApp(t, "read-bound-single", 100)
	bearer, passport := passportWithID(t, e, "reading agent", "read")

	var created struct {
		ID ids.UUID `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/people", apptest.AnyMap{
		"full_name": "Single Read",
	}, nil, &created); status != http.StatusCreated {
		t.Fatalf("seeding the person → %d", status)
	}

	if status := e.Call(t, "GET", "/v1/people/"+created.ID.String(), nil, bearer, nil); status != http.StatusOK {
		t.Fatalf("an agent single read → %d, want 200", status)
	}

	if charged := readsCharged(t, e, meter, passport); charged != 1 {
		t.Errorf("one record charged %d against MCP-SESS-READS, want 1", charged)
	}
}

// THE DERIVED OBLIGATION, and the reason this file grew: every counter the
// admission gate can REFUSE on has a charge point on the SAME door.
//
// platform/auth refuses on Calls and on agentquota.CounterFor(spec) — which is
// Reads, Writes or Egress — so the REST door owes all four. Stated as one rule
// it catches a half at a time: the mutating half was closed by C2 and the read
// half sat open for a release, because nothing asserted the pair.
//
// Egress is not driven here and is not missing: CounterFor picks between Writes
// and Egress at the SAME charge point (ChargeEffect), so the Writes row proves
// the call site and CounterFor's own tests prove the choice. Reaching Egress
// through this door additionally needs an approval staged and redeemed, since
// every egress tool with a REST twin is confirm-first.
// Each counter is measured across the ONE request that must charge it, not over
// the suite. A total taken at the end cannot say which door paid: a mutation
// answers with the row it changed, so its read-back charges Reads too, and a
// read door charging nothing at all still reads as covered. That is not a
// hypothetical — this test passed against the unmetered read door until it
// measured per request.
func TestEveryCounterTheRestDoorRefusesOnIsChargedOnIt(t *testing.T) {
	e, meter := boundedApp(t, "read-bound-census", 1000)
	bearer, passport := passportWithID(t, e, "counting agent", "read", "write")
	seedPeople(t, e, 2)
	ctx := asPassport(t, e, passport)

	advance := func(during func()) map[agentquota.Counter]int {
		counters := []agentquota.Counter{agentquota.Reads, agentquota.Writes, agentquota.Calls}
		was := map[agentquota.Counter]int{}
		for _, c := range counters {
			was[c] = meter.Read(ctx, c).Observed
		}
		during()
		moved := map[agentquota.Counter]int{}
		for _, c := range counters {
			moved[c] = meter.Read(ctx, c).Observed - was[c]
		}
		return moved
	}

	onRead := advance(func() {
		if status := e.Call(t, "GET", "/v1/people", nil, bearer, nil); status != http.StatusOK {
			t.Fatalf("the agent read → %d, want 200", status)
		}
	})
	onWrite := advance(func() {
		if status := e.Call(t, "POST", "/v1/people", apptest.AnyMap{
			"full_name": "Charged By The Gate",
		}, bearer, nil); status != http.StatusCreated {
			t.Fatalf("the agent write → %d, want 201", status)
		}
	})

	for _, owed := range []struct {
		counter agentquota.Counter
		moved   int
		door    string
	}{
		{agentquota.Reads, onRead[agentquota.Reads], "a GET hands over records"},
		{agentquota.Writes, onWrite[agentquota.Writes], "a POST mutates"},
		{agentquota.Calls, onWrite[agentquota.Calls], "every admitted call sits under the ceiling"},
	} {
		if !owed.counter.Governed() {
			continue
		}
		if owed.moved == 0 {
			t.Errorf("the REST door refuses on %s and its own request charged it 0 — %s, so a credential using only this door never approaches it",
				owed.counter, owed.door)
		}
	}
}

// The bound closes on REST reads ALONE. This is the property #623 names: an
// agent that reads only over /v1 must approach the same ceiling as one reading
// over /mcp, rather than being bounded by its MCP half alone.
func TestRestReadsAloneCanSpendTheWindow(t *testing.T) {
	e, _ := boundedApp(t, "read-bound-selfspend", 4)
	bearer, _ := passportWithID(t, e, "reading agent", "read")
	seedPeople(t, e, 3)

	// 3 records served takes the window to 3 of 4 — admitted, not yet exceeded.
	if status := e.Call(t, "GET", "/v1/people", nil, bearer, nil); status != http.StatusOK {
		t.Fatalf("the first agent read → %d, want 200", status)
	}
	// Admitted on entry (3 < 4) and serves 3 more, taking it to 6 of 4.
	if status := e.Call(t, "GET", "/v1/people", nil, bearer, nil); status != http.StatusOK {
		t.Fatalf("the second agent read → %d, want 200", status)
	}

	if status := e.Call(t, "GET", "/v1/people", nil, bearer, nil); status != http.StatusTooManyRequests {
		t.Errorf("the third agent read → %d, want 429; reading over /v1 never spends the window it is refused on", status)
	}
}

// passportWithID mints a passport and returns both the header an agent presents
// and the id the meter counts against.
func passportWithID(t *testing.T, e *apptest.AppEnv, label string, scopes ...string) (map[string]string, ids.UUID) {
	t.Helper()
	var minted struct {
		ID    ids.UUID `json:"passport_id"`
		Token string   `json:"token"`
	}
	if status := e.Call(t, "POST", "/v1/passports", apptest.AnyMap{
		"label": label, "scopes": scopes,
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport %q → %d", label, status)
	}
	if minted.Token == "" || minted.ID.IsZero() {
		t.Fatalf("passport %q minted without a token or an id", label)
	}
	return map[string]string{"Authorization": "Bearer " + minted.Token}, minted.ID
}

// seedPeople gives the list something to return, so the admitted read is a real
// one rather than an empty page.
func seedPeople(t *testing.T, e *apptest.AppEnv, n int) {
	t.Helper()
	for i := range n {
		if status := e.Call(t, "POST", "/v1/people", apptest.AnyMap{
			"full_name": "Metered Person " + string(rune('A'+i)),
		}, nil, nil); status != http.StatusCreated {
			t.Fatalf("seeding person %d → %d", i, status)
		}
	}
}
