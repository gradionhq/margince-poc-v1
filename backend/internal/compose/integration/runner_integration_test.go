// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Surface-B runner end to end (architecture/07): a scheduled job
// executes the reason-act-observe loop on the offline fake brain against
// the REAL governed tool surface — same registry, same gate, same audit
// stream as every other agent surface. Covers: full run with an agent
// write and its provenance, trigger idempotency, the 🟡 suspend →
// human decision → resume handoff (both verdicts), and the loud
// no-passport failure.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type runnerEnv struct {
	*env
	pool  *pgxpool.Pool
	svc   *compose.RunnerService
	store *runner.Store
	brain *ai.FakeClient
	wsID  ids.UUID
	wsCtx context.Context

	passportID ids.UUID
}

func setupRunner(t *testing.T) *runnerEnv {
	t.Helper()
	e := setup(t)
	e.slug = "runner-e2e"

	if status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name": "Runner E2E", "admin_email": "runner@fable.test",
		"admin_display_name": "Runner Admin", "admin_password": "correct-horse-battery",
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("bootstrap → %d", status)
	}
	if status := e.call(t, "POST", "/v1/auth/login", anyMap{
		"email": "runner@fable.test", "password": "correct-horse-battery",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("login → %d", status)
	}

	var minted struct {
		PassportID string `json:"passport_id"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "overnight runner", "scopes": []string{"read", "write"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}
	passportID, err := ids.Parse(minted.PassportID)
	if err != nil {
		t.Fatal(err)
	}

	pool, err := database.NewPool(context.Background(), os.Getenv("MARGINCE_TEST_APP_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var wsRaw string
	if err := e.owner.QueryRow(context.Background(), `SELECT id FROM workspace WHERE slug = 'runner-e2e'`).Scan(&wsRaw); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	wsID, err := ids.Parse(wsRaw)
	if err != nil {
		t.Fatal(err)
	}

	brain := ai.NewFakeClient()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return &runnerEnv{
		env:        e,
		pool:       pool,
		svc:        compose.NewRunnerService(pool, brain, nil, logger),
		store:      runner.NewStore(pool),
		brain:      brain,
		wsID:       wsID,
		wsCtx:      principal.WithWorkspaceID(context.Background(), wsID),
		passportID: passportID,
	}
}

func (re *runnerEnv) enqueue(t *testing.T, spec, trigger string, passport *ids.UUID) {
	t.Helper()
	if err := re.store.EnqueueJob(re.wsCtx, spec, trigger, passport, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func (re *runnerEnv) runRow(t *testing.T, trigger string) (status string, trace []runner.Step, approvalID *string) {
	t.Helper()
	var traceJSON []byte
	err := database.WithWorkspaceTx(re.wsCtx, re.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT status, trace, approval_id::text FROM agent_run WHERE trigger_ref = $1`, trigger).
			Scan(&status, &traceJSON, &approvalID)
	})
	if err != nil {
		t.Fatalf("run row for %s: %v", trigger, err)
	}
	if err := json.Unmarshal(traceJSON, &trace); err != nil {
		t.Fatal(err)
	}
	return status, trace, approvalID
}

func TestRunnerFullLoopWritesAsGovernedAgent(t *testing.T) {
	re := setupRunner(t)
	trigger := "overnight_at_risk_sweep:e2e-full"

	// The model proposes one governed write, then finishes.
	re.brain.Script(
		`{"tool":"log_activity","args":{"kind":"note","subject":"At-risk: no touch in 21 days","body":"evidence: none found"}}`,
		`{"final":{"summary":"one at-risk deal flagged"}}`,
	)
	re.enqueue(t, "overnight_at_risk_sweep", trigger, &re.passportID)
	if err := re.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	status, trace, _ := re.runRow(t, trigger)
	if status != "completed" {
		t.Fatalf("run status = %s, want completed", status)
	}
	if len(trace) != 1 || trace[0].Tool != "log_activity" {
		t.Fatalf("trace = %+v", trace)
	}
	if strings.Contains(trace[0].Observation, "refused") || strings.Contains(trace[0].Observation, "failed") {
		t.Fatalf("the governed write did not land: %s", trace[0].Observation)
	}

	// The write carries the AGENT's provenance in the same audit stream
	// as every other surface — no runner back door.
	var actorType, actorID, capturedBy string
	err := database.WithWorkspaceTx(re.wsCtx, re.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT al.actor_type, al.actor_id, a.captured_by
			FROM audit_log al JOIN activity a ON a.id = al.entity_id
			WHERE al.action = 'create' AND al.entity_type = 'activity'
			ORDER BY al.occurred_at DESC LIMIT 1`).Scan(&actorType, &actorID, &capturedBy)
	})
	if err != nil {
		t.Fatal(err)
	}
	if actorType != "agent" || !strings.Contains(actorID, re.passportID.String()) {
		t.Fatalf("audit actor = %s %s, want the passport-bound agent", actorType, actorID)
	}
	if !strings.HasPrefix(capturedBy, "agent:") {
		t.Fatalf("captured_by = %q, want agent provenance", capturedBy)
	}

	// Idempotency: re-seeding and re-ticking the same occurrence starts
	// no second run.
	re.enqueue(t, "overnight_at_risk_sweep", trigger, &re.passportID)
	if err := re.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var runs int
	err = database.WithWorkspaceTx(re.wsCtx, re.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM agent_run WHERE trigger_ref = $1`, trigger).Scan(&runs)
	})
	if err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("duplicate trigger started %d runs, want 1", runs)
	}
}

func TestRunnerYellowSuspendApproveResume(t *testing.T) {
	re := setupRunner(t)

	var person struct {
		ID string `json:"id"`
	}
	if status := re.call(t, "POST", "/v1/people", anyMap{"full_name": "Stale Duplicate"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}

	trigger := "overnight_at_risk_sweep:e2e-yellow"
	re.brain.Script(
		fmt.Sprintf(`{"tool":"archive_record","args":{"record_type":"person","id":"%s"}}`, person.ID),
		`{"final":{"summary":"archive executed after approval"}}`,
	)
	re.enqueue(t, "overnight_at_risk_sweep", trigger, &re.passportID)
	if err := re.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	status, _, approvalID := re.runRow(t, trigger)
	if status != "awaiting_approval" || approvalID == nil {
		t.Fatalf("run = %s approval=%v, want a parked run", status, approvalID)
	}
	// The target is untouched while parked.
	var parked struct {
		ArchivedAt *string `json:"archived_at"`
	}
	if got := re.call(t, "GET", "/v1/people/"+person.ID, nil, nil, &parked); got != http.StatusOK || parked.ArchivedAt != nil {
		t.Fatalf("target mutated while approval pending: GET → %d archived_at=%v", got, parked.ArchivedAt)
	}

	// A human approves in the same inbox every surface stages into.
	if got := re.call(t, "POST", "/v1/approvals/"+*approvalID+"/approve", anyMap{}, nil, nil); got != http.StatusOK {
		t.Fatalf("approve → %d", got)
	}

	// The decision event resumes the run (the bus delivery itself is the
	// bus lane's suite; this drives the consumer handler directly).
	if err := re.svc.HandleEvent(context.Background(), decidedEnvelope(re.wsID, *approvalID, "approved")); err != nil {
		t.Fatal(err)
	}

	status, trace, _ := re.runRow(t, trigger)
	if status != "completed" {
		t.Fatalf("resumed run status = %s, want completed", status)
	}
	var after struct {
		ArchivedAt *string `json:"archived_at"`
	}
	if got := re.call(t, "GET", "/v1/people/"+person.ID, nil, nil, &after); got != http.StatusOK || after.ArchivedAt == nil {
		t.Fatalf("approved archive did not land: GET → %d archived_at=%v; trace: %+v", got, after.ArchivedAt, trace)
	}
	// The trace is one continuous record across the approval gap.
	if len(trace) < 2 {
		t.Fatalf("trace lost the suspension boundary: %+v", trace)
	}
}

func TestRunnerYellowRejectionReplansWithoutEffect(t *testing.T) {
	re := setupRunner(t)

	var person struct {
		ID string `json:"id"`
	}
	if status := re.call(t, "POST", "/v1/people", anyMap{"full_name": "Keep Me"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}

	trigger := "overnight_at_risk_sweep:e2e-reject"
	re.brain.Script(
		fmt.Sprintf(`{"tool":"archive_record","args":{"record_type":"person","id":"%s"}}`, person.ID),
		`{"final":{"summary":"left the record alone after rejection"}}`,
	)
	re.enqueue(t, "overnight_at_risk_sweep", trigger, &re.passportID)
	if err := re.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, approvalID := re.runRow(t, trigger)
	if approvalID == nil {
		t.Fatal("no staged approval")
	}
	if got := re.call(t, "POST", "/v1/approvals/"+*approvalID+"/reject", anyMap{"reason": "not a duplicate"}, nil, nil); got != http.StatusOK {
		t.Fatalf("reject → %d", got)
	}
	if err := re.svc.HandleEvent(context.Background(), decidedEnvelope(re.wsID, *approvalID, "rejected")); err != nil {
		t.Fatal(err)
	}

	status, _, _ := re.runRow(t, trigger)
	if status != "completed" {
		t.Fatalf("rejected resume status = %s, want completed", status)
	}
	var after struct {
		ArchivedAt *string `json:"archived_at"`
	}
	if got := re.call(t, "GET", "/v1/people/"+person.ID, nil, nil, &after); got != http.StatusOK || after.ArchivedAt != nil {
		t.Fatalf("rejected action executed anyway: GET → %d archived_at=%v", got, after.ArchivedAt)
	}
}

func TestRunnerJobWithoutPassportFailsLoudly(t *testing.T) {
	re := setupRunner(t)
	trigger := "morning_brief:e2e-no-passport"
	re.enqueue(t, "morning_brief", trigger, nil)
	if err := re.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status, lastError string
	err := database.WithWorkspaceTx(re.wsCtx, re.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT status, last_error FROM runner_job WHERE trigger_ref = $1`, trigger).Scan(&status, &lastError)
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != "failed" || !strings.Contains(lastError, "no passport bound") {
		t.Fatalf("passport-less job = %s %q, want a loud failure", status, lastError)
	}
	// And no run started with ambient authority.
	var runs int
	err = database.WithWorkspaceTx(re.wsCtx, re.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM agent_run WHERE trigger_ref = $1`, trigger).Scan(&runs)
	})
	if err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("a run started without a passport: %d", runs)
	}
}

func decidedEnvelope(wsID ids.UUID, approvalID, verdict string) kevents.Envelope {
	id, _ := ids.Parse(approvalID)
	payload, _ := json.Marshal(map[string]string{"verdict": verdict})
	return kevents.Envelope{
		EventID:     ids.NewV7(),
		Type:        "approval.decided",
		WorkspaceID: wsID,
		Entity:      kevents.EntityRef{Type: "approval", ID: id},
		Payload:     payload,
	}
}
