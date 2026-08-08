// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The MCP-SESS-READS bound on the REST door, over the REAL HTTP stack with a
// real passport.
//
// The shared app harness composes readmeter.Unmetered() — it serves no Redis,
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
	"net/http"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
	"github.com/gradionhq/margince/backend/internal/platform/readmeter"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// boundedApp is the app under a live read bound, plus the meter itself so a
// test can put the window into whatever state it is about.
func boundedApp(t *testing.T, slug string, limit int) (*apptest.AppEnv, *readmeter.Meter) {
	t.Helper()
	meter := readmeter.New(budgettest.Client(t), limit, time.Hour)
	e := apptest.SetupAppWithOptions(t, compose.WithReadMeter(meter))
	e.Slug = slug
	apptest.BootstrapWorkspaceSession(t, e, "Read Bound", slug+"@fable.test", "Admin")
	return e, meter
}

// spendWindow charges the meter against one passport, as the MCP door would.
func spendWindow(t *testing.T, e *apptest.AppEnv, meter *readmeter.Meter, passport ids.UUID, records int) {
	t.Helper()
	var ws ids.UUID
	if err := e.Owner.QueryRow(t.Context(), `SELECT id FROM workspace LIMIT 1`).Scan(&ws); err != nil {
		t.Fatalf("reading the workspace id: %v", err)
	}
	ctx := principal.WithWorkspaceID(t.Context(), ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:" + passport.String(), PassportID: passport,
	})
	if err := meter.Consume(ctx, records); err != nil {
		t.Fatalf("spending the window: %v", err)
	}
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
