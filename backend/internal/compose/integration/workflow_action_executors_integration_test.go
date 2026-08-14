// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Task 11a's composing executors, proven end to end over a real migrated
// Postgres: notify with no transport wired lands a VISIBLE 'skipped' run
// with a readable reason (§3.3, UAT.md:34) rather than a silent gap or a
// fabricated success; add_to_list actually writes a real list_member row
// through the collections module's own gated write path; and draft_email
// composes a draft, parks its run behind a real staged approval, and lands
// that draft durably on workflow_run.applied anyway — so the artifact a
// suspended firing produced is never lost with the suspension.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// notifyNoTransportProbe is a synthetic handler that exists only for this
// suite: no shipped starter carries a notify action, so nothing else
// exercises ApplyActions' notify case against a real database.
type notifyNoTransportProbe struct{}

func (notifyNoTransportProbe) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    "task11a_notify_no_transport_probe",
		Trigger: workflow.Trigger{EventType: "deal.stage_changed"},
		Tier:    mcp.TierAutoExecute,
	}
}

func (notifyNoTransportProbe) Match(context.Context, workflow.Event) (bool, error) { return true, nil }

func (notifyNoTransportProbe) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionNotify, Target: ev.Entity, Args: json.RawMessage(`{}`),
	}}}, nil
}

func (notifyNoTransportProbe) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	// The zero-value Executors carries a nil Notifier — this repo wires
	// none — so this proves ApplyActions answers ErrNoNotificationTransport
	// instead of a silent no-op or a fabricated success.
	applied, err := automation.ApplyActions(ctx, automation.Executors{}, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (notifyNoTransportProbe) IdempotencyKey(ev workflow.Event) string {
	return "task11a_notify_no_transport_probe:" + ev.ID.String()
}

func TestNotifyFiringWithNoTransportLandsAVisibleSkippedRun(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Notify Probe Deal", pipeline, open, nil)

	engine := compose.NewWorkflowEngine(e.DB())
	engine.RegisterSystemWorkflow(notifyNoTransportProbe{})

	ctx := context.Background()
	if err := engine.HandleEvent(ctx, kevents.Envelope{
		EventID: ids.NewV7(), Type: "deal.stage_changed",
		OccurredAt: time.Now().UTC(),
		Entity:     kevents.EntityRef{Type: "deal", ID: dealID},
	}); err != nil {
		t.Fatal(err)
	}

	var status string
	var reason *string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(
			context.Background(),
			`SELECT status, detail->>'reason' FROM workflow_run WHERE handler = 'task11a_notify_no_transport_probe'`,
		).Scan(&status, &reason)
	}); err != nil {
		t.Fatal(err)
	}

	if status != "skipped" {
		t.Fatalf("run status = %q, want skipped — a notify firing with no transport must be visible, never silent and never a fabricated 'applied'", status)
	}
	const wantReason = "no notification transport configured"
	if reason == nil || *reason != wantReason {
		t.Fatalf("run detail reason = %v, want %q", reason, wantReason)
	}
}

// testListsAdapter mirrors compose's own (unexported) listsAdapter: this
// suite lives outside package compose, so it needs its own copy of the
// same one-line mapping onto collections.Store.AddMember.
type testListsAdapter struct{ store *collections.Store }

func (a testListsAdapter) AddMember(ctx context.Context, listID ids.ListID, entityType string, entityID ids.UUID) error {
	_, err := a.store.AddMember(ctx, listID, entityType, entityID)
	return err
}

// addToListProbe is a synthetic handler that exists only for this suite:
// no shipped starter carries an add_to_list action, so nothing else
// exercises ApplyActions' add_to_list case against a real database.
type addToListProbe struct {
	listID ids.ListID
	lists  automation.Lists
}

func (addToListProbe) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    "task11a_add_to_list_probe",
		Trigger: workflow.Trigger{EventType: "deal.stage_changed"},
		Tier:    mcp.TierAutoExecute,
	}
}

func (addToListProbe) Match(context.Context, workflow.Event) (bool, error) { return true, nil }

func (p addToListProbe) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	args, err := json.Marshal(map[string]any{"list_id": p.listID})
	if err != nil {
		return workflow.Effect{}, err
	}
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionAddToList, Target: ev.Entity, Args: args,
	}}}, nil
}

func (p addToListProbe) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	applied, err := automation.ApplyActions(ctx, automation.Executors{Lists: p.lists}, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (addToListProbe) IdempotencyKey(ev workflow.Event) string {
	return "task11a_add_to_list_probe:" + ev.ID.String()
}

func TestAddToListFiringAddsARealListMember(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Add To List Probe Deal", pipeline, open, nil)

	listsStore := collections.NewStore(e.DB())
	// The harness AdminPerms grants the core record objects but not `list`,
	// so seed the probe list as a seeded user carrying an explicit list
	// grant — the automation firing below is what the test exercises, not
	// this setup write.
	listAuthor := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"admin"},
		Objects:  map[string]principal.ObjectGrant{"list": {Create: true, Read: true, Update: true}},
		RowScope: principal.RowScopeAll,
	})
	list, err := listsStore.CreateList(listAuthor, collections.CreateListInput{Name: "Task 11a Probe List", EntityType: "deal"})
	if err != nil {
		t.Fatalf("seeding the probe list: %v", err)
	}

	engine := compose.NewWorkflowEngine(e.DB())
	engine.RegisterSystemWorkflow(addToListProbe{listID: list.ID, lists: testListsAdapter{store: listsStore}})

	ctx := context.Background()
	if err := engine.HandleEvent(ctx, kevents.Envelope{
		EventID: ids.NewV7(), Type: "deal.stage_changed",
		OccurredAt: time.Now().UTC(),
		Entity:     kevents.EntityRef{Type: "deal", ID: dealID},
	}); err != nil {
		t.Fatal(err)
	}

	var memberCount int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(
			context.Background(),
			`SELECT count(*) FROM list_member WHERE list_id = $1 AND entity_type = 'deal' AND entity_id = $2`,
			list.ID, dealID,
		).Scan(&memberCount)
	}); err != nil {
		t.Fatal(err)
	}
	if memberCount != 1 {
		t.Fatalf("list_member rows for (list, deal) = %d, want exactly 1 — the add_to_list firing never reached collections' real write path", memberCount)
	}

	var status string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(
			context.Background(),
			`SELECT status FROM workflow_run WHERE handler = 'task11a_add_to_list_probe'`,
		).Scan(&status)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "applied" {
		t.Fatalf("run status = %q, want applied", status)
	}
}

// draftingComms is a deterministic Comms stand-in for this suite: it
// returns a fixed composed draft. The real activities-backed compute is
// exercised by compose's own comms suites; what THIS suite proves is the
// durable round-trip — a composed draft surviving onto workflow_run.applied
// through real Postgres — so a hermetic composer keeps the assertion on
// the persistence, not on the draft's wording. The seam still runs live:
// the probe calls DraftEmail, and the enrichment path is what lands the
// draft on the run record.
type draftingComms struct{ subject, body string }

func (c draftingComms) DraftEmail(context.Context, ids.UUID, string) (string, string, error) {
	return c.subject, c.body, nil
}

// A fixed deliverable address, for the same reason the draft itself is fixed:
// this suite proves the durable round-trip, not the participant walk that finds
// a counterparty. Returning a real one rather than "" matters — an empty
// address would let the assertions below pass over a draft nobody could send.
func (draftingComms) ReplyAddress(context.Context, ids.UUID) (string, error) {
	return draftProbeRecipient, nil
}

const draftProbeRecipient = "counterparty@example.com"

// stagingApprovals puts the REAL approvals service behind automation's staging
// seam rather than a recording double. What this suite is for is the round trip
// through Postgres, and a fake Stage would prove the run row while skipping the
// approval row the run is supposed to park behind.
type stagingApprovals struct{ svc *approvals.Service }

func (a stagingApprovals) Stage(ctx context.Context, in automation.StageRequest) (ids.ApprovalID, error) {
	return a.svc.Stage(ctx, approvals.StageInput{
		Kind:           in.Kind,
		ProposedChange: in.ProposedChange,
		DiffHash:       in.DiffHash,
		TargetType:     in.TargetType,
		TargetID:       in.TargetID,
		Summary:        in.Summary,
		JoinPending:    in.JoinPending,
	})
}

// draftEmailProbe is a synthetic handler that exists only for this suite:
// no shipped starter carries a draft_email action, so nothing else
// exercises ApplyActions' draft_email case against a real database.
type draftEmailProbe struct {
	comms     automation.Comms
	approvals automation.Approvals
}

func (draftEmailProbe) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    "task11a_draft_email_probe",
		Trigger: workflow.Trigger{EventType: "deal.stage_changed"},
		Tier:    mcp.TierAutoExecute,
	}
}

func (draftEmailProbe) Match(context.Context, workflow.Event) (bool, error) { return true, nil }

func (draftEmailProbe) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	return workflow.Effect{Actions: []workflow.Action{{
		Kind:   workflow.ActionDraftEmail,
		Target: ev.Entity,
		Args:   json.RawMessage(`{"intent":"nudge toward a decision","consent_purpose":"business_correspondence"}`),
	}}}, nil
}

func (p draftEmailProbe) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	applied, err := automation.ApplyActions(ctx,
		automation.Executors{Comms: p.comms, Approvals: p.approvals}, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (draftEmailProbe) IdempotencyKey(ev workflow.Event) string {
	return "task11a_draft_email_probe:" + ev.ID.String()
}

func TestDraftEmailFiringLandsTheComposedDraftOnTheRunRecord(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Draft Email Probe Deal", pipeline, open, nil)

	engine := compose.NewWorkflowEngine(e.DB())
	engine.RegisterSystemWorkflow(draftEmailProbe{
		comms:     draftingComms{subject: "Re: next step", body: "Following up on our last conversation."},
		approvals: stagingApprovals{svc: approvals.NewService(e.DB())},
	})

	ctx := context.Background()
	if err := engine.HandleEvent(ctx, kevents.Envelope{
		EventID: ids.NewV7(), Type: "deal.stage_changed",
		OccurredAt: time.Now().UTC(),
		Entity:     kevents.EntityRef{Type: "deal", ID: dealID},
	}); err != nil {
		t.Fatal(err)
	}

	var status string
	var appliedJSON []byte
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(
			context.Background(),
			`SELECT status, applied FROM workflow_run WHERE handler = 'task11a_draft_email_probe'`,
		).Scan(&status, &appliedJSON)
	}); err != nil {
		t.Fatal(err)
	}
	// Drafting composes and then holds its SEND for a human, so the run parks.
	if status != "requires_approval" {
		t.Fatalf("run status = %q, want requires_approval — the send waits for a human", status)
	}

	// The composed draft must be findable IN the run record even though the
	// firing suspended. This is the regression worth a real database: the
	// staged arm of recordApplyOutcome wrote only `detail` for its whole life,
	// so the moment draft_email began staging, the artifact it had just
	// produced would have been dropped — run history reporting that an
	// automation drafted a reply while holding nothing to show for it.
	// workflow.Action carries no json tags, so it serializes into
	// workflow_run.applied with its Go field names; Go matches those keys to
	// these exported fields case-insensitively without a tag.
	var appliedActions []struct {
		Kind string
		Args struct {
			Subject string `json:"draft_subject"`
			Body    string `json:"draft_body"`
		}
	}
	if err := json.Unmarshal(appliedJSON, &appliedActions); err != nil {
		t.Fatalf("decoding workflow_run.applied: %v", err)
	}
	if len(appliedActions) != 1 {
		t.Fatalf("workflow_run.applied has %d actions, want exactly 1", len(appliedActions))
	}
	got := appliedActions[0]
	if got.Kind != string(workflow.ActionDraftEmail) {
		t.Errorf("applied action Kind = %q, want draft_email", got.Kind)
	}
	if got.Args.Subject != "Re: next step" || got.Args.Body != "Following up on our last conversation." {
		t.Fatalf("workflow_run.applied draft = (subject=%q, body=%q), want the composed draft durably persisted", got.Args.Subject, got.Args.Body)
	}

	// And the other half: a real approval row is waiting, carrying everything
	// its release needs. The run parking is only half the contract — a parked
	// run with no inbox item behind it is a firing that stopped and told
	// nobody. Asserted against the row the real service wrote, not against the
	// request the engine handed it.
	var kind, proposed string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT kind, proposed_change::text FROM approval
			  WHERE target_entity_id = $1 AND status = 'pending'`, dealID).Scan(&kind, &proposed)
	}); err != nil {
		t.Fatalf("reading the staged approval: %v", err)
	}
	if kind != automation.HeldDraftKind {
		t.Errorf("staged kind = %q, want %q", kind, automation.HeldDraftKind)
	}
	var staged struct {
		AnchorActivityID string `json:"anchor_activity_id"`
		To               string `json:"to"`
		Subject          string `json:"subject"`
		Body             string `json:"body"`
		ConsentPurpose   string `json:"consent_purpose"`
	}
	if err := json.Unmarshal([]byte(proposed), &staged); err != nil {
		t.Fatalf("decoding the staged proposal: %v", err)
	}
	// Every field the release reads. It is handed the proposed change, the diff
	// hash and the approval id — never the approval's target — so anything
	// missing here is a message that cannot be sent at the moment somebody
	// approves it, which is the worst place to find out.
	if staged.To != draftProbeRecipient {
		t.Errorf("staged to = %q, want %q", staged.To, draftProbeRecipient)
	}
	if staged.ConsentPurpose != "business_correspondence" {
		t.Errorf("staged consent_purpose = %q, want the declared purpose", staged.ConsentPurpose)
	}
	if staged.AnchorActivityID != dealID.String() {
		t.Errorf("staged anchor = %q, want the fired target %q", staged.AnchorActivityID, dealID)
	}
	if staged.Subject != "Re: next step" || staged.Body != "Following up on our last conversation." {
		t.Errorf("staged message = (%q, %q), want the composed draft", staged.Subject, staged.Body)
	}
}
