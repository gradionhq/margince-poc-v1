// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The bundle door itself: the routes, the session-only admission, the list
// filter, and the per-member body. The DECISION's own properties — N verdicts,
// N audit rows, an expired member reported rather than approved — are proven
// against the service in modules/approvals/bundle_integration_test.go; what can
// only be shown here is that a real client reaches all of it.

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// bundleDecisionBody is the wire shape both bundle routes answer with.
type bundleDecisionBody struct {
	BundleID string `json:"bundle_id"`
	Data     []struct {
		Approval struct {
			ID       string  `json:"id"`
			Kind     string  `json:"kind"`
			Status   string  `json:"status"`
			BundleID *string `json:"bundle_id"`
		} `json:"approval"`
		Outcome string `json:"outcome"`
	} `json:"data"`
}

// approvalListBody is the inbox page, read for the bundle id it now carries.
type approvalListBody struct {
	Data []struct {
		ID       string  `json:"id"`
		Kind     string  `json:"kind"`
		BundleID *string `json:"bundle_id"`
	} `json:"data"`
}

// siteReadPayload is what each kind of a site read's proposal carries. The
// payloads are real ones: this suite decides through the composed application,
// so every approved member runs its registered accept effect, and a placeholder
// body would report effect_failed for reasons that have nothing to do with
// bundling.
func siteReadPayload(kind, orgID, readID, person string) string {
	if kind == "deepread" {
		return `{"organization_id":"` + orgID + `","source_url":"https://acme.example",` +
			`"site_read_id":"` + readID + `","fields":[{"field":"industry","value":"Industrial valves",` +
			`"evidence_snippet":"Acme makes industrial valves.","source_url":"https://acme.example","confidence":0.9}],"facts":[]}`
	}
	return `{"organization_id":"` + orgID + `","site_read_id":"` + readID + `","natural_key":"` + person +
		`","name":"` + person + `","role":"CTO","published_email":"` + person + `@acme.example",` +
		`"evidence_snippet":"` + person + `, CTO","source_url":"https://acme.example/team"}`
}

// stageBundleRows puts one act's proposals in the inbox through the owner
// connection. The staging PATH is exercised where it lives; this suite needs
// only the rows an act leaves behind, and writing them directly keeps the
// arrange step from depending on a crawler.
func stageBundleRows(t *testing.T, e *apptest.AppEnv, orgID string, kinds ...string) ids.UUID {
	t.Helper()
	ctx := context.Background()
	bundle, readID := ids.NewV7(), ids.NewV7()
	var wsID, adminID string
	if err := e.Owner.QueryRow(ctx, `SELECT id FROM workspace WHERE slug = $1`, e.Slug).Scan(&wsID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	if err := e.Owner.QueryRow(ctx,
		`SELECT id FROM app_user WHERE workspace_id = $1 ORDER BY created_at LIMIT 1`, wsID).Scan(&adminID); err != nil {
		t.Fatalf("admin lookup: %v", err)
	}
	for i, kind := range kinds {
		payload := siteReadPayload(kind, orgID, readID.String(), fmt.Sprintf("person%d", i))
		if _, err := e.Owner.Exec(ctx, `
			INSERT INTO approval (workspace_id, kind, proposed_by, on_behalf_of, target_entity_type,
			                      target_entity_id, summary, proposed_change, diff_hash, expires_at, bundle_id)
			VALUES ($1, $2, 'agent:site-read', $3, 'organization', $4, $5, $6::jsonb, $7,
			        now() + interval '1 day', $8)`,
			wsID, kind, adminID, orgID, "Staged by the site read", payload,
			ids.NewV7().String(), bundle); err != nil {
			t.Fatalf("staging member %d (%s): %v", i, kind, err)
		}
	}
	return bundle
}

// One act, one question, and a body that still answers for every proposal in it.
// The second call is the part worth having: a client that retries — or two
// people clearing the same inbox — must not re-decide anything, and must be told
// so per member rather than by a bare 409 that says nothing about which.
func TestABundleIsListedAndDecidedThroughTheAPI(t *testing.T) {
	e := apptest.SetupApp(t)
	e.Slug = "bundle-door"
	apptest.BootstrapWorkspaceSession(t, e, "Bundle Door", "ada@bundle.test", "Ada Admin")

	var org struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/organizations", apptest.AnyMap{
		"display_name": "Acme GmbH", "source": "ui",
	}, nil, &org); status != http.StatusCreated {
		t.Fatalf("create org → %d", status)
	}
	bundle := stageBundleRows(t, e, org.ID, "deepread", "site_lead", "site_lead")

	var listed approvalListBody
	if status := e.Call(t, "GET", "/v1/approvals?bundle_id="+bundle.String(), nil, nil, &listed); status != http.StatusOK {
		t.Fatalf("list by bundle → %d", status)
	}
	if len(listed.Data) != 3 {
		t.Fatalf("the bundle filter returned %d rows, want the 3 the act staged", len(listed.Data))
	}
	for _, item := range listed.Data {
		if item.BundleID == nil || *item.BundleID != bundle.String() {
			t.Errorf("%s (%s) carries bundle_id %v, want %s", item.ID, item.Kind, item.BundleID, bundle)
		}
	}

	var decision bundleDecisionBody
	if status := e.Call(t, "POST", "/v1/approval-bundles/"+bundle.String()+"/approve",
		apptest.AnyMap{"reason": "the read looks right"}, nil, &decision); status != http.StatusOK {
		t.Fatalf("approve bundle → %d", status)
	}
	if decision.BundleID != bundle.String() || len(decision.Data) != 3 {
		t.Fatalf("decision = %+v, want the 3 members of %s", decision, bundle)
	}
	for _, member := range decision.Data {
		if member.Outcome != "decided" || member.Approval.Status != "approved" {
			t.Errorf("member %s → outcome %q status %q, want decided/approved",
				member.Approval.ID, member.Outcome, member.Approval.Status)
		}
	}

	var replay bundleDecisionBody
	if status := e.Call(t, "POST", "/v1/approval-bundles/"+bundle.String()+"/approve",
		nil, nil, &replay); status != http.StatusOK {
		t.Fatalf("re-approve bundle → %d, want 200 reporting per member", status)
	}
	if len(replay.Data) != 3 {
		t.Fatalf("the second call reported %d members, want the same 3 — a loop over an "+
			"empty list would pass this test having checked nothing", len(replay.Data))
	}
	for _, member := range replay.Data {
		if member.Outcome != "already_decided" {
			t.Errorf("member %s on the second call → %q, want already_decided",
				member.Approval.ID, member.Outcome)
		}
	}
}

// A bundle nobody staged reads as absent rather than as an empty success. An
// empty 200 would tell a caller the id is real and holds nothing, which is the
// existence oracle the single-approval read already refuses to be.
func TestAnUnknownBundleIsNotFoundThroughTheAPI(t *testing.T) {
	e := apptest.SetupApp(t)
	e.Slug = "bundle-absent"
	apptest.BootstrapWorkspaceSession(t, e, "Bundle Absent", "ada@absent.test", "Ada Admin")

	if status := e.Call(t, "POST", "/v1/approval-bundles/"+ids.NewV7().String()+"/reject",
		nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("reject an unstaged bundle → %d, want 404", status)
	}
}

// Deciding is human work whatever it is spelled over. A passport reaches the
// same origin and the same routes, so the bundle door has to refuse it exactly
// as the single-approval door does — otherwise an agent that staged a bundle
// could release the whole thing by asking for all of it at once.
func TestABundleDecisionRefusesAPassport(t *testing.T) {
	e := apptest.SetupApp(t)
	e.Slug = "bundle-passport"
	apptest.BootstrapWorkspaceSession(t, e, "Bundle Passport", "ada@passport.test", "Ada Admin")

	var org struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/organizations", apptest.AnyMap{
		"display_name": "Acme GmbH", "source": "ui",
	}, nil, &org); status != http.StatusCreated {
		t.Fatalf("create org → %d", status)
	}
	bundle := stageBundleRows(t, e, org.ID, "site_lead")
	agent := passportBearer(t, e, "bundle agent", "read", "write")

	status := e.Call(t, "POST", "/v1/approval-bundles/"+bundle.String()+"/approve", nil, agent, nil)
	if status != http.StatusForbidden && status != http.StatusUnauthorized {
		t.Fatalf("a passport approving a bundle → %d, want it refused", status)
	}
	var stored string
	if err := e.Owner.QueryRow(context.Background(),
		`SELECT status FROM approval WHERE bundle_id = $1`, bundle).Scan(&stored); err != nil {
		t.Fatalf("reading the member back: %v", err)
	}
	if stored != "pending" {
		t.Errorf("the member is %s after a refused agent decision, want pending", stored)
	}
}
