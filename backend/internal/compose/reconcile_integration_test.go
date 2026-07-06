// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Overnight follow-up reconciliation over real migrated Postgres
// (features/07 §8a, B-E06.2a): the nightly pass turns a captured
// interaction with no next step into a STAGED follow-up proposal —
// never a silent write. After a run, the deal is untouched and the
// follow-up sits in the morning approval inbox; a human confirm creates
// it exactly once, a reject creates nothing, and a rep who cannot see
// the deal cannot see — or decide — its proposal.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// reconcileEnv wraps integration.Env with the default pipeline, the follow-up
// reconciler, and the approvals service carrying its confirm effect.
type reconcileEnv struct {
	*integration.Env
	owner      *pgx.Conn
	pipeline   ids.UUID
	open       ids.UUID
	reconciler *deals.FollowUpReconciler
	svc        *approvals.Service
}

// reconcilePerms is a team-scoped rep who may create activities and read
// deals — exactly what confirming a follow-up needs, and no more, so the
// row-scope test bites.
var reconcilePerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"activity": {Create: true, Read: true},
		"deal":     {Read: true, Update: true},
		"pipeline": {Read: true},
	},
	RowScope: principal.RowScopeTeam,
}

func setupReconcile(t *testing.T) *reconcileEnv {
	t.Helper()
	e := &reconcileEnv{Env: integration.Setup(t), owner: integration.OwnerConn(t)}
	e.pipeline, e.open, _ = integration.DealFixture(t, e.Env)
	quiet := slog.New(slog.NewTextHandler(os.Stderr, nil))
	e.svc = approvals.NewService(e.Pool)
	e.svc.WithEffect(deals.FollowUpReconcileKind, followUpConfirmEffect(e.svc, e.Activities))
	e.reconciler = deals.NewFollowUpReconciler(e.Pool, followUpStager{svc: e.svc}, quiet)
	return e
}

// seedInteraction plants a captured call/mail/meeting on the deal,
// occurredHoursAgo before now — the "real touch" side of the discrepancy.
func (e *reconcileEnv) seedInteraction(t *testing.T, dealID ids.UUID, kind, subject string, occurredHoursAgo int) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	ctx := context.Background()
	if _, err := e.owner.Exec(ctx,
		`INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		 VALUES ($1, $2, $3, $4, now() - make_interval(hours => $5), 'manual', 'human:x')`,
		id, e.WS, kind, subject, occurredHoursAgo); err != nil {
		t.Fatalf("seed %s activity: %v", kind, err)
	}
	e.linkActivity(t, id, dealID)
	return id
}

// seedTask plants a task on the deal — the "next step already planned"
// side that suppresses the proposal when it is still open.
func (e *reconcileEnv) seedTask(t *testing.T, dealID ids.UUID, done bool) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, due_at, is_done, done_at, source, captured_by)
		 VALUES ($1, $2, 'task', 'Existing next step', now(), now() + interval '2 days', $3,
		         CASE WHEN $3 THEN now() ELSE NULL END, 'manual', 'human:x')`,
		id, e.WS, done); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	e.linkActivity(t, id, dealID)
	return id
}

func (e *reconcileEnv) linkActivity(t *testing.T, activityID, dealID ids.UUID) {
	t.Helper()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO activity_link (workspace_id, activity_id, entity_type, deal_id) VALUES ($1, $2, 'deal', $3)`,
		e.WS, activityID, dealID); err != nil {
		t.Fatalf("link activity to deal: %v", err)
	}
}

func (e *reconcileEnv) pendingFollowUps(t *testing.T, dealID ids.UUID) int {
	t.Helper()
	var n int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM approval WHERE kind = 'deal_follow_up' AND target_entity_id = $1 AND status = 'pending'`,
		dealID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *reconcileEnv) followUpApproval(t *testing.T, dealID ids.UUID) (ids.UUID, deals.FollowUpProposal) {
	t.Helper()
	var id ids.UUID
	var raw []byte
	if err := e.owner.QueryRow(context.Background(),
		`SELECT id, proposed_change FROM approval WHERE kind = 'deal_follow_up' AND target_entity_id = $1 AND status = 'pending'`,
		dealID).Scan(&id, &raw); err != nil {
		t.Fatalf("no staged follow-up to decide: %v", err)
	}
	proposal, err := deals.UnmarshalFollowUpProposal(raw)
	if err != nil {
		t.Fatalf("staged proposal does not round-trip: %v", err)
	}
	return id, proposal
}

// dealTasks reports the follow-up tasks the confirm effect created on the
// deal, and the provenance of the first — used to prove exactly-once and
// the agent:overnight attribution.
func (e *reconcileEnv) dealTasks(t *testing.T, dealID ids.UUID) (count int, capturedBy, sourceSystem string, due *time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := e.owner.QueryRow(ctx, `
		SELECT count(*) FROM activity a
		JOIN activity_link l ON l.activity_id = a.id AND l.deal_id = $1
		WHERE a.kind = 'task' AND a.source = 'overnight-reconcile'`, dealID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		return 0, "", "", nil
	}
	if err := e.owner.QueryRow(ctx, `
		SELECT a.captured_by, a.source_system, a.due_at FROM activity a
		JOIN activity_link l ON l.activity_id = a.id AND l.deal_id = $1
		WHERE a.kind = 'task' AND a.source = 'overnight-reconcile'
		ORDER BY a.occurred_at DESC LIMIT 1`, dealID).Scan(&capturedBy, &sourceSystem, &due); err != nil {
		t.Fatal(err)
	}
	return count, capturedBy, sourceSystem, due
}

func (e *reconcileEnv) dealVersion(t *testing.T, dealID ids.UUID) int64 {
	t.Helper()
	var v int64
	if err := e.owner.QueryRow(context.Background(), `SELECT version FROM deal WHERE id = $1`, dealID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// --- staging: a real touch with no next step becomes a proposal, not a write ---

func TestFollowUpReconcileStagesProposalAndCommitsNothing(t *testing.T) {
	e := setupReconcile(t)
	deal := e.SeedDeal(t, "Touched, no next step", e.pipeline, e.open, &e.Rep1)
	call := e.seedInteraction(t, deal, "call", "Discovery call", 1)
	before := e.dealVersion(t, deal)

	if err := e.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := e.pendingFollowUps(t, deal); got != 1 {
		t.Fatalf("pending follow-up proposals = %d, want 1", got)
	}
	// None committed: the deal is untouched and no task exists yet.
	if after := e.dealVersion(t, deal); after != before {
		t.Errorf("deal version moved %d → %d; the pass must stage, not write", before, after)
	}
	if got, _, _, _ := e.dealTasks(t, deal); got != 0 {
		t.Errorf("tasks created pre-approval = %d, want 0 (staged, not committed)", got)
	}

	// The proposal is grounded in the real interaction and dated ahead.
	_, proposal := e.followUpApproval(t, deal)
	if proposal.EvidenceActivityID != call {
		t.Errorf("evidence activity = %s, want the seeded call %s", proposal.EvidenceActivityID, call)
	}
	if proposal.EvidenceKind != "call" {
		t.Errorf("evidence kind = %q, want call", proposal.EvidenceKind)
	}
	wantDue := today().AddDate(0, 0, 3).Format(time.DateOnly)
	if proposal.DueDate != wantDue {
		t.Errorf("proposed due date = %q, want %q (today + follow-up lead)", proposal.DueDate, wantDue)
	}
}

// --- suppression: an existing next step, no real touch, and dedupe ---

func TestFollowUpReconcileSuppressesWhenNoDiscrepancy(t *testing.T) {
	e := setupReconcile(t)

	// A recent call but an OPEN task already queued: the rep has a next
	// step — do not nag.
	planned := e.SeedDeal(t, "Has next step", e.pipeline, e.open, &e.Rep1)
	e.seedInteraction(t, planned, "meeting", "Kickoff", 2)
	e.seedTask(t, planned, false)

	// A deal with only a note (not a call/mail/meeting): no real touch.
	noteOnly := e.SeedDeal(t, "Note only", e.pipeline, e.open, &e.Rep1)
	e.seedInteraction(t, noteOnly, "email", "Old thread", 24*10) // outside the 48h window

	if err := e.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.pendingFollowUps(t, planned); got != 0 {
		t.Errorf("deal with an open next step staged %d proposals, want 0", got)
	}
	if got := e.pendingFollowUps(t, noteOnly); got != 0 {
		t.Errorf("deal with no recent interaction staged %d proposals, want 0", got)
	}
}

func TestFollowUpReconcileDoesNotStackAcrossPasses(t *testing.T) {
	e := setupReconcile(t)
	deal := e.SeedDeal(t, "Reconciled twice", e.pipeline, e.open, &e.Rep1)
	e.seedInteraction(t, deal, "call", "Call", 1)

	for pass := 0; pass < 2; pass++ {
		if err := e.reconciler.Reconcile(context.Background()); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}
	if got := e.pendingFollowUps(t, deal); got != 1 {
		t.Errorf("after two passes, pending proposals = %d, want still 1 (no duplicate)", got)
	}
}

// --- confirm / reject: the human decision is the only write ---

func TestFollowUpConfirmCreatesTheTaskExactlyOnce(t *testing.T) {
	e := setupReconcile(t)
	deal := e.SeedDeal(t, "Confirm me", e.pipeline, e.open, &e.Rep1)
	e.seedInteraction(t, deal, "call", "Discovery", 1)
	if err := e.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	approvalID, proposal := e.followUpApproval(t, deal)

	human := e.As(e.Rep1, []ids.UUID{e.Team1}, reconcilePerms)
	if _, err := e.svc.Decide(human, approvalID, true, nil); err != nil {
		t.Fatalf("approve + effect: %v", err)
	}

	count, capturedBy, sourceSystem, due := e.dealTasks(t, deal)
	if count != 1 {
		t.Fatalf("follow-up tasks created = %d, want exactly 1", count)
	}
	if capturedBy != "agent:overnight" {
		t.Errorf("captured_by = %q, want agent:overnight (the agent's suggestion, on behalf of the human)", capturedBy)
	}
	if sourceSystem != "overnight-reconcile" {
		t.Errorf("source_system = %q, want overnight-reconcile", sourceSystem)
	}
	if due == nil || due.Format(time.DateOnly) != proposal.DueDate {
		t.Errorf("task due = %v, want the proposed %s", due, proposal.DueDate)
	}

	// Exactly-once: the proposal is no longer pending, so a re-driven
	// decision is refused and no second task is created.
	if _, err := e.svc.Decide(human, approvalID, true, nil); err == nil {
		t.Error("a second decision on an approved proposal succeeded; want it refused")
	}
	if again, _, _, _ := e.dealTasks(t, deal); again != 1 {
		t.Errorf("tasks after a replayed decision = %d, want still 1", again)
	}
}

func TestFollowUpRejectWritesNothing(t *testing.T) {
	e := setupReconcile(t)
	deal := e.SeedDeal(t, "Reject me", e.pipeline, e.open, &e.Rep1)
	e.seedInteraction(t, deal, "meeting", "Sync", 1)
	if err := e.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	approvalID, _ := e.followUpApproval(t, deal)

	human := e.As(e.Rep1, []ids.UUID{e.Team1}, reconcilePerms)
	if _, err := e.svc.Decide(human, approvalID, false, nil); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if got, _, _, _ := e.dealTasks(t, deal); got != 0 {
		t.Errorf("a rejected follow-up created %d tasks, want 0", got)
	}
	var status string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT status FROM approval WHERE id = $1`, approvalID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "rejected" {
		t.Errorf("approval status = %q, want rejected", status)
	}
}

// --- row scope: a proposal never leaks a deal the decider cannot see ---

func TestFollowUpProposalRespectsRowScope(t *testing.T) {
	e := setupReconcile(t)
	deal := e.SeedDeal(t, "Rep1's deal", e.pipeline, e.open, &e.Rep1)
	e.seedInteraction(t, deal, "call", "Private call", 1)
	if err := e.reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	approvalID, _ := e.followUpApproval(t, deal)

	// rep3 sits in team2; rep1's deal is invisible to them, so the staged
	// proposal reads as absent — no decide oracle for a leaked UUID.
	outsider := e.As(e.Rep3, []ids.UUID{e.Team2}, reconcilePerms)
	if _, err := e.svc.Decide(outsider, approvalID, true, nil); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("outsider decide → %v, want ErrNotFound (row-scope existence hiding)", err)
	}
	if got, _, _, _ := e.dealTasks(t, deal); got != 0 {
		t.Errorf("an undecidable proposal still created %d tasks, want 0", got)
	}

	// The owner can see and confirm it.
	owner := e.As(e.Rep1, []ids.UUID{e.Team1}, reconcilePerms)
	if _, err := e.svc.Decide(owner, approvalID, true, nil); err != nil {
		t.Fatalf("owner decide: %v", err)
	}
	if got, _, _, _ := e.dealTasks(t, deal); got != 1 {
		t.Errorf("owner confirm created %d tasks, want 1", got)
	}
}
