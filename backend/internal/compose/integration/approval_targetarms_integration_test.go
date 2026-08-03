// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// One confirm-first verb per target-visibility arm — a curator's list, a tag, a
// saved view, an offer template and a webhook subscription — each walking the
// whole loop over the passport surface a real agent uses: the agent's call stages
// instead of writing, the human's inbox LISTS the row and answers for it, the
// decision releases it, and the identical call then lands.
//
// All three reads are asserted, not just the decision. `decidable` backs the
// inbox list, the single Get and the Decide alike, and a staged row it rejects is
// invisible AND undecidable: the refusal tells the agent to go and get an approval
// no human can ever give, and the row sits pending until the TTL clears it.

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/webhooks"
)

func TestConfirmFirstArchivesAreDecidableForEveryTargetArm(t *testing.T) {
	// The webhook-subscription route seals a signing secret, so the suite boots
	// with the deployment key the create path needs.
	cipher, err := webhooks.NewCipher(bytes.Repeat([]byte{0x5a}, webhooks.WebhookKeyBytes))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	e := setupWithOptions(t, compose.WithWebhookSigningKey(cipher))
	e.bootstrapWorkspace(t)
	bearer := agentBearer(t, e, "target-arm agent")

	t.Run("list", func(t *testing.T) {
		id := createdID(t, e, "/v1/lists", anyMap{"name": "Q3 Targets", "entity_type": "person"})
		releaseStagedCall(t, e, bearer, "DELETE", "/v1/lists/"+id, nil, "list")
	})

	t.Run("tag", func(t *testing.T) {
		id := createdID(t, e, "/v1/tags", anyMap{"name": "Champion"})
		releaseStagedCall(t, e, bearer, "DELETE", "/v1/tags/"+id, nil, "tag")
	})

	t.Run("saved_view", func(t *testing.T) {
		id := createdID(t, e, "/v1/views", anyMap{
			"resource": "people", "name": "My people", "query": anyMap{"columns": []any{"full_name"}},
		})
		releaseStagedCall(t, e, bearer, "DELETE", "/v1/views/"+id, nil, "saved_view")
	})

	t.Run("offer_template", func(t *testing.T) {
		id := createdID(t, e, "/v1/offer-templates", anyMap{
			"name": "Standard DE", "layout": anyMap{"logo_url": "https://example.test/logo.png"},
		})
		releaseStagedCall(t, e, bearer, "DELETE", "/v1/offer-templates/"+id, nil, "offer_template")
	})

	t.Run("webhook_subscription", func(t *testing.T) {
		var created struct {
			Subscription struct {
				ID string `json:"id"`
			} `json:"subscription"`
		}
		if status := e.call(t, "POST", "/v1/webhook-subscriptions", anyMap{
			"target_url": "https://ok.example/hook", "event_types": []string{"deal.created"},
		}, nil, &created); status != http.StatusCreated {
			t.Fatalf("create subscription → %d", status)
		}
		releaseStagedCall(t, e, bearer, "PATCH", "/v1/webhook-subscriptions/"+created.Subscription.ID,
			anyMap{"state": "paused"}, "webhook_subscription")
	})
}

// A decision is a judgment about the row as the human was shown it, so a row
// that moved underneath the approval revokes it. The staging reads the target's
// version itself (approvals.versionTables), redemption re-checks it, and the
// released call carries it as its own If-Match — three steps that all
// short-circuit on a NULL pin, which is what a target type absent from that set
// stages. Asserted on the subscription patch because it is the confirm-first
// route whose staged change is a field patch rather than an archive: an admin
// re-pointing the endpoint between the staging and the approval is exactly the
// drift the human never agreed to.
func TestAStagedPatchIsRefusedWhenItsTargetMovedBeforeTheApproval(t *testing.T) {
	cipher, err := webhooks.NewCipher(bytes.Repeat([]byte{0x5a}, webhooks.WebhookKeyBytes))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	e := setupWithOptions(t, compose.WithWebhookSigningKey(cipher))
	e.bootstrapWorkspace(t)
	bearer := agentBearer(t, e, "skew agent")

	var created struct {
		Subscription struct {
			ID      string `json:"id"`
			Version int64  `json:"version"`
		} `json:"subscription"`
	}
	if status := e.call(t, "POST", "/v1/webhook-subscriptions", anyMap{
		"target_url": "https://ok.example/hook", "event_types": []string{"deal.created"},
	}, nil, &created); status != http.StatusCreated {
		t.Fatalf("create subscription → %d", status)
	}
	path := "/v1/webhook-subscriptions/" + created.Subscription.ID
	staged := anyMap{"state": "paused"}

	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if status := e.call(t, "PATCH", path, staged, bearer, &problem); status != http.StatusForbidden ||
		problem.Code != "approval_required" {
		t.Fatalf("agent patch → %d %q, want 403 approval_required", status, problem.Code)
	}
	approvalID := extractStagedApprovalID(t, problem.Detail)

	// The admin's own edit, on a different field so the guarded patch really
	// bumps the version rather than finding nothing to change.
	var edited struct {
		Subscription struct {
			Version int64 `json:"version"`
		} `json:"subscription"`
	}
	if status := e.call(t, "PATCH", path, anyMap{"event_types": []string{"deal.updated"}}, nil, &edited); status != http.StatusOK {
		t.Fatalf("admin re-point → %d", status)
	}
	if edited.Subscription.Version == created.Subscription.Version {
		t.Fatalf("the admin's edit left version at %d — the fixture proves nothing about a pin it never moved",
			edited.Subscription.Version)
	}

	if status := e.call(t, "POST", "/v1/approvals/"+approvalID+"/approve", anyMap{}, nil, nil); status != http.StatusOK {
		t.Fatalf("human approve → %d", status)
	}
	withToken := map[string]string{"X-Approval-Token": approvalID}
	for k, v := range bearer {
		withToken[k] = v
	}
	problem = struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}{}
	if status := e.call(t, "PATCH", path, staged, withToken, &problem); status != http.StatusConflict ||
		problem.Code != "version_skew" {
		t.Fatalf("release over a moved row → %d %q, want 409 version_skew — the approval authorized the row "+
			"the human saw, not whatever it became", status, problem.Code)
	}
}

// agentBearer mints a passport and returns the Authorization header a governed
// agent call carries. A passport is the credential the tool surface and REST
// alike admit an agent under (ADR-0055), so it is what a confirm-first refusal
// has to be provoked with.
func agentBearer(t *testing.T, e *env, label string) map[string]string {
	t.Helper()
	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": label, "scopes": []string{"read", "write"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}
	return map[string]string{"Authorization": "Bearer " + minted.Token}
}

// createdID creates one record as the bootstrap admin over their session and
// returns its id — the human-owned row a later agent call stages against.
func createdID(t *testing.T, e *env, path string, body anyMap) string {
	t.Helper()
	var created struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", path, body, nil, &created); status != http.StatusCreated {
		t.Fatalf("POST %s → %d", path, status)
	}
	if created.ID == "" {
		t.Fatalf("POST %s returned no id", path)
	}
	return created.ID
}

// releaseStagedCall asserts the full confirm-first loop for one route: the agent
// is refused with a staged approval, the human can see and decide that approval,
// and the identical call then executes under the approval token.
//
// The identical body is sent twice because the diff_hash binding is what makes an
// approval authorize THIS call and no other.
func releaseStagedCall(t *testing.T, e *env, bearer map[string]string, method, path string, body anyMap, wantTargetType string) {
	t.Helper()
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if status := e.call(t, method, path, body, bearer, &problem); status != http.StatusForbidden ||
		problem.Code != "approval_required" {
		t.Fatalf("agent %s %s → %d %q, want 403 approval_required", method, path, status, problem.Code)
	}
	approvalID := extractStagedApprovalID(t, problem.Detail)

	assertDecidableInTheInbox(t, e, approvalID, wantTargetType)

	if status := e.call(t, "POST", "/v1/approvals/"+approvalID+"/approve", anyMap{}, nil, nil); status != http.StatusOK {
		t.Fatalf("human approve → %d, want 200 — a row the inbox lists and cannot decide is the same dead "+
			"end one step later", status)
	}
	withToken := map[string]string{"X-Approval-Token": approvalID}
	for k, v := range bearer {
		withToken[k] = v
	}
	// Every route here answers 200 on release: an archive returns the archived
	// row and the subscription patch the updated one.
	if status := e.call(t, method, path, body, withToken, nil); status != http.StatusOK {
		t.Fatalf("approved retry → %d, want 200 — the decision must release the staged call", status)
	}
}
