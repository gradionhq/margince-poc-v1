// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Relationship edges + the partner extension over HTTP: endpoint
// visibility gates reads and writes (an edge never out-sees its ends),
// one current-primary employer per person, optimistic concurrency on
// update, and partner promotion flipping the org's classification.

import (
	"net/http"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

type relEnv struct {
	*apptest.AppEnv
	personID string
	orgID    string
}

func setupRelationships(t *testing.T) *relEnv {
	t.Helper()
	e := apptest.SetupApp(t)
	e.Slug = "rel-e2e"
	apptest.BootstrapWorkspaceSession(t, e, "Rel E2E", "rel@fable.test", "Admin")
	var person, org struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/people", apptest.AnyMap{"full_name": "Edge Person"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	if status := e.Call(t, "POST", "/v1/organizations", apptest.AnyMap{"display_name": "Edge Org"}, nil, &org); status != http.StatusCreated {
		t.Fatalf("create org → %d", status)
	}
	return &relEnv{AppEnv: e, personID: person.ID, orgID: org.ID}
}

func TestRelationshipLifecycle(t *testing.T) {
	e := setupRelationships(t)

	var first struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}
	if status := e.Call(t, "POST", "/v1/relationships", apptest.AnyMap{
		"kind": "employment", "person_id": e.personID, "organization_id": e.orgID,
		"role": "cto", "is_current_primary": true, "source": "ui",
	}, nil, &first); status != http.StatusCreated {
		t.Fatalf("create employment → %d", status)
	}

	// A second primary employer demotes the first inside one tx.
	var org2 struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/organizations", apptest.AnyMap{"display_name": "Second Org"}, nil, &org2); status != http.StatusCreated {
		t.Fatalf("create org2 → %d", status)
	}
	if status := e.Call(t, "POST", "/v1/relationships", apptest.AnyMap{
		"kind": "employment", "person_id": e.personID, "organization_id": org2.ID,
		"is_current_primary": true, "source": "ui",
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("second employment → %d", status)
	}
	var listed struct {
		Data []struct {
			ID               string `json:"id"`
			IsCurrentPrimary bool   `json:"is_current_primary"`
		} `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/relationships?person_id="+e.personID+"&kind=employment", nil, nil, &listed); status != http.StatusOK || len(listed.Data) != 2 {
		t.Fatalf("list → %d %+v", status, listed)
	}
	primaries := 0
	for _, rel := range listed.Data {
		if rel.IsCurrentPrimary {
			primaries++
		}
	}
	if primaries != 1 {
		t.Fatalf("%d current-primary employers, the invariant is ≤1", primaries)
	}

	// Optimistic concurrency: a stale If-Match answers version_skew.
	var problem struct {
		Code string `json:"code"`
	}
	status := e.Call(t, "PATCH", "/v1/relationships/"+first.ID, apptest.AnyMap{"role": "ceo"},
		map[string]string{"If-Match": "999"}, &problem)
	if status != http.StatusConflict || problem.Code != "version_skew" {
		t.Fatalf("stale If-Match → %d %q, want 409 version_skew", status, problem.Code)
	}
	if status := e.Call(t, "PATCH", "/v1/relationships/"+first.ID, apptest.AnyMap{"role": "ceo"}, nil, nil); status != http.StatusOK {
		t.Fatalf("update → %d", status)
	}

	if status := e.Call(t, "DELETE", "/v1/relationships/"+first.ID, nil, nil, nil); status != http.StatusOK {
		t.Fatalf("archive → %d", status)
	}

	// A malformed endpoint shape is a 422, not a DB error.
	if status := e.Call(t, "POST", "/v1/relationships", apptest.AnyMap{
		"kind": "employment", "person_id": e.personID, "source": "ui",
	}, nil, nil); status != 422 {
		t.Fatalf("shape-violating edge → %d, want 422", status)
	}
	// An invisible endpoint reads as absent (H1).
	if status := e.Call(t, "POST", "/v1/relationships", apptest.AnyMap{
		"kind": "employment", "person_id": "00000000-0000-7000-8000-00000000dead",
		"organization_id": e.orgID, "source": "ui",
	}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("invisible endpoint → %d, want 404", status)
	}
}

// Ending an employment is a 🟡 act for an AGENT: it changes what the record says
// about a person's job, and it is hard to undo for whoever needed the edge. The
// human path above deletes it outright, because the confirm-first tier governs
// agent principals, not people in their own seat (ADR-0055 makes a passport's
// REST call governed exactly like its MCP twin).
//
// The pin is the second assertion, and it is the one worth spelling out: this
// REST path stages through the approvals engine, which resolves target_version
// itself inside the staging transaction for every target type its versionTables
// set knows — `relationship` is in that set, so the pin lands without the
// caller offering one. A type absent from it stages a NULL pin, and redemption's
// skew check short-circuits on NULL, so the approval would authorize the archive
// against whatever the edge had drifted to inside the TTL.
//
// (The MCP twin reaches staging differently — archive_record's StageInfo READS
// the target to summarize it — which is why the seam owes a relationship Read.
// That read is exercised by create_record's read-back in
// relationship_seam_integration_test.go.)
func TestArchivingAnEdgeStagesForAnAgentAndPinsItsVersion(t *testing.T) {
	e := setupRelationships(t)

	var edge struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}
	if status := e.Call(t, "POST", "/v1/relationships", apptest.AnyMap{
		"kind": "employment", "person_id": e.personID, "organization_id": e.orgID,
		"role": "cto", "source": "ui",
	}, nil, &edge); status != http.StatusCreated {
		t.Fatalf("create employment → %d", status)
	}

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.Call(t, "POST", "/v1/passports", apptest.AnyMap{
		"label": "edge agent", "scopes": []string{"read", "write"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	status := e.Call(t, "DELETE", "/v1/relationships/"+edge.ID, nil, bearer, &problem)
	if status != http.StatusForbidden || problem.Code != "approval_required" {
		t.Fatalf("agent archive → %d %q, want 403 approval_required", status, problem.Code)
	}
	approvalID := extractStagedApprovalID(t, problem.Detail)

	// The pin the STAGING read produced. Without it the approval would authorize
	// the archive against whatever the edge had drifted to inside the TTL — and a
	// nil pin is exactly what a target type the seam cannot read produces.
	var pin *int64
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT target_version FROM approval WHERE id = $1`, approvalID).Scan(&pin); err != nil {
		t.Fatal(err)
	}
	if pin == nil {
		t.Fatal("the staged archive carries no target_version — redemption's skew check short-circuits " +
			"on NULL, so the approval would authorize the archive whatever the edge became")
	}
	if *pin != edge.Version {
		t.Errorf("target_version = %d, want the edge's own %d", *pin, edge.Version)
	}

	assertDecidableInTheInbox(t, e.AppEnv, approvalID, "relationship")

	// The edge is untouched while the approval is pending.
	var parked struct {
		Data []struct {
			ID         string  `json:"id"`
			ArchivedAt *string `json:"archived_at"`
		} `json:"data"`
	}
	if got := e.Call(t, "GET", "/v1/relationships?person_id="+e.personID+"&kind=employment", nil, nil, &parked); got != http.StatusOK {
		t.Fatalf("list while parked → %d", got)
	}
	for _, rel := range parked.Data {
		if rel.ID == edge.ID && rel.ArchivedAt != nil {
			t.Errorf("the edge was archived while its approval was still pending")
		}
	}

	// And it can be REJECTED, which is the half a caller cannot fake: a decision
	// runs the same visibility predicate as the list.
	if got := e.Call(t, "POST", "/v1/approvals/"+approvalID+"/reject", apptest.AnyMap{
		"reason": "probe",
	}, nil, nil); got != http.StatusOK {
		t.Fatalf("deciding the staged archive → %d, want 200 — a row the inbox lists and cannot decide is "+
			"the same dead end one step later", got)
	}
}

func TestPartnerPromotionLifecycle(t *testing.T) {
	e := setupRelationships(t)

	if status := e.Call(t, "GET", "/v1/organizations/"+e.orgID+"/partner", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("non-partner org → %d, want 404", status)
	}
	var partner struct {
		CertStatus string `json:"cert_status"`
	}
	if status := e.Call(t, "PUT", "/v1/organizations/"+e.orgID+"/partner", apptest.AnyMap{
		"partner_role": "consulting", "cert_status": "certified",
		"gate_metrics": apptest.AnyMap{"certified_staff": 3, "retention_rate": 90},
	}, nil, &partner); status != http.StatusOK || partner.CertStatus != "certified" {
		t.Fatalf("upsert partner → %d %+v", status, partner)
	}
	// Promotion writes the partner relationship type — the half of the
	// invariant that lives on the organization (ADR-0079 amending ADR-0032).
	var org struct {
		RelationshipTypes []string `json:"relationship_types"`
	}
	if status := e.Call(t, "GET", "/v1/organizations/"+e.orgID, nil, nil, &org); status != http.StatusOK {
		t.Fatalf("org after promotion → %d", status)
	}
	if !slices.Contains(org.RelationshipTypes, "partner") {
		t.Fatalf("org after promotion carries %v, want partner among them", org.RelationshipTypes)
	}
	var partners struct {
		Data []struct {
			OrganizationID string `json:"organization_id"`
		} `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/partners?cert_status=certified", nil, nil, &partners); status != http.StatusOK || len(partners.Data) != 1 {
		t.Fatalf("list partners → %d %+v", status, partners)
	}
	if status := e.Call(t, "GET", "/v1/partners?cert_status=suspended", nil, nil, &partners); status != http.StatusOK {
		t.Fatalf("filtered list → %d", status)
	}
}

// assertDecidableInTheInbox is the assertion that makes a 403 approval_required
// mean something: a staged row a human cannot SEE is one they can never release
// or reject, and `decidable` backs the list, the single read and the decision
// alike — so an approval whose target type the inbox has no visibility rule for
// is a zombie, and the fan-out that would have told anyone is dropped too.
func assertDecidableInTheInbox(t *testing.T, e *apptest.AppEnv, approvalID, wantTargetType string) {
	t.Helper()
	var inbox struct {
		Data []struct {
			ID         string `json:"id"`
			TargetType string `json:"target_entity_type"`
		} `json:"data"`
	}
	if got := e.Call(t, "GET", "/v1/approvals?status=pending", nil, nil, &inbox); got != http.StatusOK {
		t.Fatalf("list approvals → %d", got)
	}
	for _, a := range inbox.Data {
		if a.ID != approvalID {
			continue
		}
		if a.TargetType != wantTargetType {
			t.Errorf("the staged row's target_entity_type is %q, want %q", a.TargetType, wantTargetType)
		}
		if got := e.Call(t, "GET", "/v1/approvals/"+approvalID, nil, nil, nil); got != http.StatusOK {
			t.Fatalf("GET the staged approval → %d, want 200", got)
		}
		return
	}
	t.Fatalf("the staged approval is not in the pending inbox (%d rows) — nobody can act on it", len(inbox.Data))
}

// An approval stays decidable after its target edge is archived out from under it.
//
// EnsureRelationshipVisible deliberately does NOT filter archived rows, unlike
// Store.GetRelationship, which does. That asymmetry is the whole reason a human can
// still REJECT a staged archive of an edge somebody removed in the meantime — and
// nothing gated it, so the next author adding `AND r.archived_at IS NULL` for
// symmetry with the read verb would take the guarantee away with every test green.
//
// This is the case that makes the deviation load-bearing rather than a comment.
func TestAnApprovalStaysDecidableAfterItsEdgeIsArchived(t *testing.T) {
	e := setupRelationships(t)

	var edge struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/relationships", apptest.AnyMap{
		"kind": "employment", "person_id": e.personID, "organization_id": e.orgID,
		"role": "cfo", "source": "ui",
	}, nil, &edge); status != http.StatusCreated {
		t.Fatalf("create employment → %d", status)
	}

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.Call(t, "POST", "/v1/passports", apptest.AnyMap{
		"label": "archived edge agent", "scopes": []string{"read", "write"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}

	var problem struct {
		Detail string `json:"detail"`
	}
	if status := e.Call(t, "DELETE", "/v1/relationships/"+edge.ID, nil,
		map[string]string{"Authorization": "Bearer " + minted.Token}, &problem); status != http.StatusForbidden {
		t.Fatalf("agent archive → %d, want 403 approval_required", status)
	}
	approvalID := extractStagedApprovalID(t, problem.Detail)

	// The human archives the edge themselves, which is exactly the race the
	// approval was staged into: the target is gone before anyone decided.
	if status := e.Call(t, "DELETE", "/v1/relationships/"+edge.ID, nil, nil, nil); status != http.StatusOK {
		t.Fatalf("human archive → %d", status)
	}

	// The staged row must STILL be visible and rejectable. If it were not, the
	// approval would sit pending until its TTL with no way to clear it.
	assertDecidableInTheInbox(t, e.AppEnv, approvalID, "relationship")
	if got := e.Call(t, "POST", "/v1/approvals/"+approvalID+"/reject", apptest.AnyMap{
		"reason": "already archived",
	}, nil, nil); got != http.StatusOK {
		t.Errorf("rejecting an approval whose edge was archived → %d, want 200 — a human must be able to "+
			"clear authority whose target is gone", got)
	}
}
