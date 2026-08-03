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
	"testing"
)

type relEnv struct {
	*env
	personID string
	orgID    string
}

func setupRelationships(t *testing.T) *relEnv {
	t.Helper()
	e := setup(t)
	e.slug = "rel-e2e"
	bootstrapWorkspaceSession(t, e, "Rel E2E", "rel@fable.test", "Admin")
	var person, org struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{"full_name": "Edge Person"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	if status := e.call(t, "POST", "/v1/organizations", anyMap{"display_name": "Edge Org"}, nil, &org); status != http.StatusCreated {
		t.Fatalf("create org → %d", status)
	}
	return &relEnv{env: e, personID: person.ID, orgID: org.ID}
}

func TestRelationshipLifecycle(t *testing.T) {
	e := setupRelationships(t)

	var first struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}
	if status := e.call(t, "POST", "/v1/relationships", anyMap{
		"kind": "employment", "person_id": e.personID, "organization_id": e.orgID,
		"role": "cto", "is_current_primary": true, "source": "ui",
	}, nil, &first); status != http.StatusCreated {
		t.Fatalf("create employment → %d", status)
	}

	// A second primary employer demotes the first inside one tx.
	var org2 struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/organizations", anyMap{"display_name": "Second Org"}, nil, &org2); status != http.StatusCreated {
		t.Fatalf("create org2 → %d", status)
	}
	if status := e.call(t, "POST", "/v1/relationships", anyMap{
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
	if status := e.call(t, "GET", "/v1/relationships?person_id="+e.personID+"&kind=employment", nil, nil, &listed); status != http.StatusOK || len(listed.Data) != 2 {
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
	status := e.call(t, "PATCH", "/v1/relationships/"+first.ID, anyMap{"role": "ceo"},
		map[string]string{"If-Match": "999"}, &problem)
	if status != http.StatusConflict || problem.Code != "version_skew" {
		t.Fatalf("stale If-Match → %d %q, want 409 version_skew", status, problem.Code)
	}
	if status := e.call(t, "PATCH", "/v1/relationships/"+first.ID, anyMap{"role": "ceo"}, nil, nil); status != http.StatusOK {
		t.Fatalf("update → %d", status)
	}

	if status := e.call(t, "DELETE", "/v1/relationships/"+first.ID, nil, nil, nil); status != http.StatusOK {
		t.Fatalf("archive → %d", status)
	}

	// A malformed endpoint shape is a 422, not a DB error.
	if status := e.call(t, "POST", "/v1/relationships", anyMap{
		"kind": "employment", "person_id": e.personID, "source": "ui",
	}, nil, nil); status != 422 {
		t.Fatalf("shape-violating edge → %d, want 422", status)
	}
	// An invisible endpoint reads as absent (H1).
	if status := e.call(t, "POST", "/v1/relationships", anyMap{
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
	if status := e.call(t, "POST", "/v1/relationships", anyMap{
		"kind": "employment", "person_id": e.personID, "organization_id": e.orgID,
		"role": "cto", "source": "ui",
	}, nil, &edge); status != http.StatusCreated {
		t.Fatalf("create employment → %d", status)
	}

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "edge agent", "scopes": []string{"read", "write"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	status := e.call(t, "DELETE", "/v1/relationships/"+edge.ID, nil, bearer, &problem)
	if status != http.StatusForbidden || problem.Code != "approval_required" {
		t.Fatalf("agent archive → %d %q, want 403 approval_required", status, problem.Code)
	}
	approvalID := extractStagedApprovalID(t, problem.Detail)

	// The pin the STAGING read produced. Without it the approval would authorize
	// the archive against whatever the edge had drifted to inside the TTL — and a
	// nil pin is exactly what a target type the seam cannot read produces.
	var pin *int64
	if err := e.owner.QueryRow(t.Context(),
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

	// The edge is untouched while the approval is pending.
	var listed struct {
		Data []struct {
			ID         string  `json:"id"`
			ArchivedAt *string `json:"archived_at"`
		} `json:"data"`
	}
	if got := e.call(t, "GET", "/v1/relationships?person_id="+e.personID+"&kind=employment", nil, nil, &listed); got != http.StatusOK {
		t.Fatalf("list while parked → %d", got)
	}
	for _, rel := range listed.Data {
		if rel.ID == edge.ID && rel.ArchivedAt != nil {
			t.Errorf("the edge was archived while its approval was still pending")
		}
	}
}

func TestPartnerPromotionLifecycle(t *testing.T) {
	e := setupRelationships(t)

	if status := e.call(t, "GET", "/v1/organizations/"+e.orgID+"/partner", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("non-partner org → %d, want 404", status)
	}
	var partner struct {
		CertStatus string `json:"cert_status"`
	}
	if status := e.call(t, "PUT", "/v1/organizations/"+e.orgID+"/partner", anyMap{
		"partner_role": "consulting", "cert_status": "certified",
		"gate_metrics": anyMap{"certified_staff": 3, "retention_rate": 90},
	}, nil, &partner); status != http.StatusOK || partner.CertStatus != "certified" {
		t.Fatalf("upsert partner → %d %+v", status, partner)
	}
	// Promotion flips the org's classification.
	var org struct {
		Classification string `json:"classification"`
	}
	if status := e.call(t, "GET", "/v1/organizations/"+e.orgID, nil, nil, &org); status != http.StatusOK || org.Classification != "partner" {
		t.Fatalf("org after promotion → %d classification=%q", status, org.Classification)
	}
	var partners struct {
		Data []struct {
			OrganizationID string `json:"organization_id"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/partners?cert_status=certified", nil, nil, &partners); status != http.StatusOK || len(partners.Data) != 1 {
		t.Fatalf("list partners → %d %+v", status, partners)
	}
	if status := e.call(t, "GET", "/v1/partners?cert_status=suspended", nil, nil, &partners); status != http.StatusOK {
		t.Fatalf("filtered list → %d", status)
	}
}
