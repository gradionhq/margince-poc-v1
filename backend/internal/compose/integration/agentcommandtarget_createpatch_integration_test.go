// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The create and patch families' half of the governance seam
// (modules/agents/command.go), proved the same way
// agentcommandtarget_integration_test.go proves it for archive: the staged
// approval ROW, not merely the ErrRequiresApproval sentinel a refusal with
// nowhere to land answers just as readily.

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/webhooks"
)

// A confirm-first CREATE stages the record TYPE with NO target id — the row
// does not exist yet, so there is nothing for an approval to pin. createProject
// is the contract's per-record-type floor (#982) tightening a verb that is
// auto-execute for every other type it serves.
func TestARestCreateStagesItsRecordTypeWithNoTargetID(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	bearer := agentBearer(t, e, "create-staging agent")

	orgID := createdID(t, e, "/v1/organizations", apptest.AnyMap{"display_name": "Tier floor anchor"})

	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if status := e.Call(t, "POST", "/v1/projects", apptest.AnyMap{
		"name": "Unapproved", "organization_id": orgID,
	}, bearer, &problem); status != http.StatusForbidden || problem.Code != "approval_required" {
		t.Fatalf("agent project create → %d %q, want 403 approval_required", status, problem.Code)
	}
	approvalID := ExtractStagedApprovalID(t, problem.Detail)

	var targetType string
	var targetID *string
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT coalesce(target_entity_type, ''), target_entity_id FROM approval WHERE id = $1`,
		approvalID).Scan(&targetType, &targetID); err != nil {
		t.Fatalf("reading the staged approval: %v", err)
	}
	if targetType != "project" {
		t.Errorf("staged target_entity_type = %q, want \"project\"", targetType)
	}
	if targetID != nil {
		t.Errorf("staged target_entity_id = %v, want NULL — a create names no existing row an approval "+
			"could pin", *targetID)
	}

	var n int
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT count(*) FROM project WHERE name = 'Unapproved'`).Scan(&n); err != nil {
		t.Fatalf("counting projects: %v", err)
	}
	if n != 0 {
		t.Error("a project named Unapproved exists — the agent performed unattended the write this " +
			"confirm-first tier should have staged")
	}
}

// A confirm-first PATCH stages the record TYPE and the ID the route named —
// the row a human's decision reads and the redemption pins. webhook_subscription
// is outside create_record/update_record's own tool schema and outside the
// record seam (datasource.EntityTypes()), so this also proves
// patchResolver.Guards' servedByTheRecordSeam short-circuit stands down
// gracefully over the full stack rather than faulting on a read the seam
// cannot answer.
func TestARestPatchOutsideTheToolSchemaStagesTheRowWithID(t *testing.T) {
	cipher, err := webhooks.NewCipher(bytes.Repeat([]byte{0x5a}, webhooks.WebhookKeyBytes))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	e := apptest.SetupAppWithOptions(t, compose.WithWebhookSigningKey(cipher))
	e.BootstrapWorkspace(t)
	bearer := agentBearer(t, e, "patch-staging agent")

	var created struct {
		Subscription struct {
			ID string `json:"id"`
		} `json:"subscription"`
	}
	if status := e.Call(t, "POST", "/v1/webhook-subscriptions", apptest.AnyMap{
		"target_url": "https://ok.example/hook", "event_types": []string{"deal.created"},
	}, nil, &created); status != http.StatusCreated {
		t.Fatalf("create subscription → %d", status)
	}

	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if status := e.Call(t, "PATCH", "/v1/webhook-subscriptions/"+created.Subscription.ID,
		apptest.AnyMap{"state": "paused"}, bearer, &problem); status != http.StatusForbidden ||
		problem.Code != "approval_required" {
		t.Fatalf("agent subscription patch → %d %q, want 403 approval_required", status, problem.Code)
	}
	approvalID := ExtractStagedApprovalID(t, problem.Detail)

	var targetType string
	var targetID *string
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT coalesce(target_entity_type, ''), target_entity_id FROM approval WHERE id = $1`,
		approvalID).Scan(&targetType, &targetID); err != nil {
		t.Fatalf("reading the staged approval: %v", err)
	}
	if targetType != "webhook_subscription" {
		t.Errorf("staged target_entity_type = %q, want \"webhook_subscription\"", targetType)
	}
	if targetID == nil {
		t.Fatal("the staged approval names no target id — a decision about which subscription was never captured")
	}
	if *targetID != created.Subscription.ID {
		t.Errorf("staged target_entity_id = %s, want %s", *targetID, created.Subscription.ID)
	}

	// The staged row is only half the proof: a door that staged the approval AND
	// applied the patch answers everything above unchanged.
	var state string
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT state FROM webhook_subscription WHERE id = $1`, created.Subscription.ID).Scan(&state); err != nil {
		t.Fatalf("reading the subscription back: %v", err)
	}
	if state != "active" {
		t.Errorf("the subscription is %q — the agent performed unattended the write this confirm-first "+
			"tier should have staged", state)
	}
}

// The regression this rules out: createCustomField creates a record type
// (custom_field) createResolver.Guards has no opinion on — create_record's
// own Handle cannot write it, but this operation's own module (customfields)
// does, entirely independent of that verb. An earlier version of this seam
// had createResolver.Guards refuse it outright over REST (the "does
// create_record itself serve this type" question, correct for the TOOL door,
// wrongly asked of every door); this proves it now STAGES like any other
// confirm-first create, not that it merely fails to 500.
func TestARestCreateCustomFieldStagesRatherThanRefuses(t *testing.T) {
	e := apptest.SetupAppWithOptions(t, compose.WithSchemaPool(SchemaPool(t)))
	e.BootstrapWorkspace(t)
	bearer := agentBearer(t, e, "custom-field-create agent")

	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if status := e.Call(t, "POST", "/v1/custom-fields", apptest.AnyMap{
		"object": "deal", "label": "Champion Score", "type": "text", "source": "ui",
	}, bearer, &problem); status != http.StatusForbidden || problem.Code != "approval_required" {
		t.Fatalf("agent custom field create → %d %q, want 403 approval_required — a hard refusal here is "+
			"the exact regression this test rules out", status, problem.Code)
	}
	approvalID := ExtractStagedApprovalID(t, problem.Detail)

	var targetType string
	var targetID *string
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT coalesce(target_entity_type, ''), target_entity_id FROM approval WHERE id = $1`,
		approvalID).Scan(&targetType, &targetID); err != nil {
		t.Fatalf("reading the staged approval: %v", err)
	}
	if targetType != "custom_field" {
		t.Errorf("staged target_entity_type = %q, want \"custom_field\"", targetType)
	}
	if targetID != nil {
		t.Errorf("staged target_entity_id = %v, want NULL — a create names no existing row", *targetID)
	}

	var n int
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT count(*) FROM custom_field WHERE label = 'Champion Score'`).Scan(&n); err != nil {
		t.Fatalf("counting custom fields: %v", err)
	}
	if n != 0 {
		t.Error("a custom field labelled Champion Score exists — the agent performed unattended the write " +
			"this confirm-first tier should have staged")
	}
}

// The same regression, on createWebhookSubscription — the other create
// route outside createResolver's served vocabulary.
func TestARestCreateWebhookSubscriptionStagesRatherThanRefuses(t *testing.T) {
	cipher, err := webhooks.NewCipher(bytes.Repeat([]byte{0x5a}, webhooks.WebhookKeyBytes))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	e := apptest.SetupAppWithOptions(t, compose.WithWebhookSigningKey(cipher))
	e.BootstrapWorkspace(t)
	bearer := agentBearer(t, e, "webhook-subscription-create agent")

	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if status := e.Call(t, "POST", "/v1/webhook-subscriptions", apptest.AnyMap{
		"target_url": "https://ok.example/hook", "event_types": []string{"deal.created"},
	}, bearer, &problem); status != http.StatusForbidden || problem.Code != "approval_required" {
		t.Fatalf("agent webhook subscription create → %d %q, want 403 approval_required — a hard refusal "+
			"here is the exact regression this test rules out", status, problem.Code)
	}
	approvalID := ExtractStagedApprovalID(t, problem.Detail)

	var targetType string
	var targetID *string
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT coalesce(target_entity_type, ''), target_entity_id FROM approval WHERE id = $1`,
		approvalID).Scan(&targetType, &targetID); err != nil {
		t.Fatalf("reading the staged approval: %v", err)
	}
	if targetType != "webhook_subscription" {
		t.Errorf("staged target_entity_type = %q, want \"webhook_subscription\"", targetType)
	}
	if targetID != nil {
		t.Errorf("staged target_entity_id = %v, want NULL — a create names no existing row", *targetID)
	}

	// Nothing else in this test creates a subscription, so the staged create is
	// the only one that could have written a row.
	var n int
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT count(*) FROM webhook_subscription`).Scan(&n); err != nil {
		t.Fatalf("counting webhook subscriptions: %v", err)
	}
	if n != 0 {
		t.Errorf("%d webhook subscriptions exist — the agent performed unattended the write this "+
			"confirm-first tier should have staged", n)
	}
}
