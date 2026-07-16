// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Task 11a's composing executors, proven end to end over a real migrated
// Postgres: notify with no transport wired lands a VISIBLE 'skipped' run
// with a readable reason (§3.3, UAT.md:34) rather than a silent gap or a
// fabricated success; add_to_list actually writes a real list_member row
// through the collections module's own gated write path; and draft_email
// lands the composed draft durably on workflow_run.applied, so a run
// reporting 'applied' has a real, findable draft — never a discarded one.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
		Tier:    mcp.TierGreen,
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

	engine := compose.NewWorkflowEngine(e.Pool)
	engine.RegisterSystemWorkflow(notifyNoTransportProbe{})

	ctx := context.Background()
	if err := engine.HandleEvent(ctx, kevents.Envelope{
		EventID: ids.NewV7(), Type: "deal.stage_changed", WorkspaceID: e.WS,
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
		Tier:    mcp.TierGreen,
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

	listsStore := collections.NewStore(e.Pool)
	list, err := listsStore.CreateList(e.Admin(), collections.CreateListInput{Name: "Task 11a Probe List", EntityType: "deal"})
	if err != nil {
		t.Fatalf("seeding the probe list: %v", err)
	}

	engine := compose.NewWorkflowEngine(e.Pool)
	engine.RegisterSystemWorkflow(addToListProbe{listID: list.ID, lists: testListsAdapter{store: listsStore}})

	ctx := context.Background()
	if err := engine.HandleEvent(ctx, kevents.Envelope{
		EventID: ids.NewV7(), Type: "deal.stage_changed", WorkspaceID: e.WS,
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

// draftEmailProbe is a synthetic handler that exists only for this suite:
// no shipped starter carries a draft_email action, so nothing else
// exercises ApplyActions' draft_email case against a real database.
type draftEmailProbe struct{ comms automation.Comms }

func (draftEmailProbe) Spec() workflow.Spec {
	return workflow.Spec{
		Name:    "task11a_draft_email_probe",
		Trigger: workflow.Trigger{EventType: "deal.stage_changed"},
		Tier:    mcp.TierGreen,
	}
}

func (draftEmailProbe) Match(context.Context, workflow.Event) (bool, error) { return true, nil }

func (draftEmailProbe) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionDraftEmail, Target: ev.Entity, Args: json.RawMessage(`{"intent":"nudge toward a decision"}`),
	}}}, nil
}

func (p draftEmailProbe) Apply(ctx context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	applied, err := automation.ApplyActions(ctx, automation.Executors{Comms: p.comms}, eff)
	return workflow.RunResult{Applied: applied}, err
}

func (draftEmailProbe) IdempotencyKey(ev workflow.Event) string {
	return "task11a_draft_email_probe:" + ev.ID.String()
}

func TestDraftEmailFiringLandsTheComposedDraftOnTheRunRecord(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Draft Email Probe Deal", pipeline, open, nil)

	engine := compose.NewWorkflowEngine(e.Pool)
	engine.RegisterSystemWorkflow(draftEmailProbe{comms: draftingComms{subject: "Re: next step", body: "Following up on our last conversation."}})

	ctx := context.Background()
	if err := engine.HandleEvent(ctx, kevents.Envelope{
		EventID: ids.NewV7(), Type: "deal.stage_changed", WorkspaceID: e.WS,
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
	if status != "applied" {
		t.Fatalf("run status = %q, want applied", status)
	}

	// The composed draft must be findable IN the run record — a draft_email
	// firing whose only effect is the text would be a fake success if the
	// run said 'applied' with nothing durable behind it.
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
}
