// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The §2.4 ladder over the REAL stack: a live Redis counter, a real approval
// row, a real decision by a real human, and the same passport reading again.
//
// The unit suites prove each half against a stub. What only this lane can prove
// is that the halves meet — that the window the gate refused on is the window the
// approval widened, through the actual approvals HTTP surface, with the same
// meter pointer at both ends. Every wiring fault this change could have has the
// same symptom: an approval that reads as granted and an agent that stays
// refused.

import (
	"net/http"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ladderApp is the app under live counters at thresholds a test can actually
// cross, plus the meter so a test can put a window into the state it is about.
func ladderApp(t *testing.T, slug string, limits agentquota.Limits) (*apptest.AppEnv, *agentquota.Meter) {
	t.Helper()
	meter := agentquota.New(budgettest.Client(t), limits, time.Hour)
	// The CONNECTOR composition, because half the ladder only exists behind
	// /mcp: the REST door refuses on the quota but has no tool to name, so the
	// step-up it would stage has no question in it. The hosted transport is
	// where a refusal becomes something a human can answer.
	e := apptest.SetupAppWithOriginOptions(t, func(origin string) []compose.Option {
		return []compose.Option{
			compose.WithMCPConnector(), compose.WithMCPResource(origin + "/mcp"),
			compose.WithAgentQuota(meter),
		}
	})
	e.Slug = slug
	apptest.BootstrapWorkspaceSession(t, e, "Quota Ladder", slug+"@fable.test", "Admin")
	return e, meter
}

// spendCounter charges one counter against one passport, as the surface would.
func spendCounter(t *testing.T, e *apptest.AppEnv, meter *agentquota.Meter, passport ids.UUID, c agentquota.Counter, n int) {
	t.Helper()
	var ws ids.UUID
	if err := e.Owner.QueryRow(t.Context(), `SELECT id FROM workspace LIMIT 1`).Scan(&ws); err != nil {
		t.Fatalf("reading the workspace id: %v", err)
	}
	ctx := principal.WithWorkspaceID(t.Context(), ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:" + passport.String(), PassportID: passport,
	})
	if err := meter.Consume(ctx, c, n); err != nil {
		t.Fatalf("spending the %s window: %v", c, err)
	}
}

// pendingStepUp reads the staged step-up out of the database. It goes to the
// table rather than the inbox API because the inbox is scoped to the DECIDING
// human, and half of what this file proves is which human that is.
func pendingStepUp(t *testing.T, e *apptest.AppEnv) (id ids.ApprovalID, passport ids.UUID, summary string) {
	t.Helper()
	err := e.Owner.QueryRow(t.Context(), `
		SELECT id, passport_id, coalesce(summary, '') FROM approval
		 WHERE kind = $1 AND status = 'pending'`, approvals.KindQuotaRelease).Scan(&id, &passport, &summary)
	if err != nil {
		t.Fatalf("reading the staged step-up: %v", err)
	}
	return id, passport, summary
}

// callTool makes one tools/call over the mounted MCP surface, under the
// presented passport. It ignores the answer: every caller here is about what
// the call left BEHIND — a staged question, or the absence of one — and a tool
// refusal is the expected outcome in all of them.
func callTool(t *testing.T, e *apptest.AppEnv, bearer map[string]string, tool string, args apptest.AnyMap) {
	t.Helper()
	status := e.Call(t, "POST", "/mcp", apptest.AnyMap{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": apptest.AnyMap{"name": tool, "arguments": args},
	}, bearer, nil)
	if status != http.StatusOK {
		t.Fatalf("tools/call %s → %d, want 200 with the refusal inside the result", tool, status)
	}
}

// BYO-STEP-1, whole: a read past the threshold is refused, the question reaches
// the human who lent the passport, their approval widens THAT window, and the
// same passport reads again — through the same door, with nothing else changed.
func TestAReadPastItsThresholdIsReleasedByTheHumanWhoLentThePassport(t *testing.T) {
	e, meter := ladderApp(t, "ladder-read", agentquota.Limits{Reads: 100})
	bearer, passport := passportWithID(t, e, "reading agent", "read")
	seedPeople(t, e, 2)
	spendCounter(t, e, meter, passport, agentquota.Reads, 120)

	if status := e.Call(t, "GET", "/v1/people", nil, bearer, nil); status != http.StatusTooManyRequests {
		t.Fatalf("a read past its threshold → %d, want 429", status)
	}

	// The REST door refuses without staging (it has no tool to name), so the
	// question is put through the MCP door — which is the door the ladder is
	// written for.
	callTool(t, e, bearer, "search_records", apptest.AnyMap{"query": "Metered"})

	id, staged, summary := pendingStepUp(t, e)
	if staged != passport {
		t.Fatalf("the step-up was staged against passport %s, want the one that was refused (%s)", staged, passport)
	}
	if summary == "" {
		t.Error("the step-up carries no sentence for a human to answer")
	}

	// The human decides through the real surface — the same POST the inbox
	// makes — and the release is applied as a consequence of that decision.
	if status := e.Call(t, "POST", "/v1/approvals/"+id.String()+"/approve", nil, nil, nil); status != http.StatusOK {
		t.Fatalf("approving the step-up → %d", status)
	}

	if status := e.Call(t, "GET", "/v1/people", nil, bearer, nil); status != http.StatusOK {
		t.Errorf("the agent is still refused after its human approved the step-up → %d; "+
			"the window the gate reads and the window the approval widened are not the same window", status)
	}
}

// The other half of the same property, and the one that would fail silently:
// approving does not hand out an UNBOUNDED continuation. One release is one
// more allowance, so the agent that spends it is refused again and its human is
// asked again.
func TestOneReleaseIsOneMoreAllowanceAndNotAStandingPermission(t *testing.T) {
	e, meter := ladderApp(t, "ladder-once", agentquota.Limits{Reads: 100})
	bearer, passport := passportWithID(t, e, "reading agent", "read")
	seedPeople(t, e, 2)
	spendCounter(t, e, meter, passport, agentquota.Reads, 120)
	callTool(t, e, bearer, "search_records", apptest.AnyMap{"query": "Metered"})
	id, _, _ := pendingStepUp(t, e)
	if status := e.Call(t, "POST", "/v1/approvals/"+id.String()+"/approve", nil, nil, nil); status != http.StatusOK {
		t.Fatalf("approving the step-up → %d", status)
	}

	// The released allowance is spent too.
	spendCounter(t, e, meter, passport, agentquota.Reads, 100)

	if status := e.Call(t, "GET", "/v1/people", nil, bearer, nil); status != http.StatusTooManyRequests {
		t.Errorf("a second crossing after one release → %d, want 429: approving once granted a standing permission", status)
	}
}

// BYO-STEP-3: a spent EGRESS ceiling reaches no inbox. Approving it is not a
// thing the meter will do, so staging it would put a question in front of a
// human whose answer changes nothing — and leave the agent waiting for it.
func TestASpentEgressCeilingAsksNobodyAnything(t *testing.T) {
	e, meter := ladderApp(t, "ladder-egress", agentquota.Limits{Egress: 1})
	bearer, passport := passportWithID(t, e, "sending agent", "read", "send")
	spendCounter(t, e, meter, passport, agentquota.Egress, 5)

	callTool(t, e, bearer, "send_email", apptest.AnyMap{
		"to": "someone@example.com", "subject": "s", "body": "b",
	})

	var staged int
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT count(*) FROM approval WHERE kind = $1`, approvals.KindQuotaRelease).Scan(&staged); err != nil {
		t.Fatalf("counting staged step-ups: %v", err)
	}
	if staged != 0 {
		t.Errorf("a hard stop reached a human's inbox %d times; no approval lifts it", staged)
	}
}
