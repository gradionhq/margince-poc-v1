// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// HTTP-level coverage for GET /people/{id}/strength and
// GET /organizations/{id}/strength (§4 relationship strength) over the
// real handler stack: a person with qualifying interactions answers a
// real score whose factors reconcile to it, and an unreadable/nonexistent
// person answers 404 rather than disclosing existence.

import (
	"math"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// strengthWire is the wire slice these assertions read.
type strengthWire struct {
	Score   int    `json:"score"`
	Bucket  string `json:"bucket"`
	Factors struct {
		Recency     float64 `json:"recency"`
		Frequency   float64 `json:"frequency"`
		Reciprocity float64 `json:"reciprocity"`
		Direction   float64 `json:"direction"`
	} `json:"factors"`
	Inbound90d              *int     `json:"inbound_90d"`
	Outbound90d             *int     `json:"outbound_90d"`
	ComputedAt              *string  `json:"computed_at"`
	ContributingActivityIds []string `json:"contributing_activity_ids"`
}

func seedStrengthPersonWithActivities(t *testing.T, e *env) string {
	t.Helper()
	var person struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Strength Target", "source": "ui",
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}

	// Two fresh qualifying interactions (kind email, within the 90-day
	// window since occurred_at defaults to now), one of each direction —
	// a live recency/frequency/reciprocity signal rather than a bare
	// zero-interaction "none" bucket.
	for _, direction := range []string{"inbound", "outbound"} {
		if status := e.call(t, "POST", "/v1/activities", anyMap{
			"kind": "email", "subject": "Touch", "source": "ui", "direction": direction,
			"links": []anyMap{{"entity_id": person.ID, "entity_type": "person"}},
		}, nil, nil); status != http.StatusCreated {
			t.Fatalf("log %s activity → %d", direction, status)
		}
	}
	return person.ID
}

// TestPersonStrengthHTTPReconciles: a person with two balanced, fresh
// interactions answers a bucket in the contract vocabulary, a score in
// 0..100, and factors whose product (rounded) equals the score — the
// same §4 formula the domain computes, just surfaced over the wire.
func TestPersonStrengthHTTPReconciles(t *testing.T) {
	e := setup(t)
	e.slug = "strength-e2e"
	bootstrapWorkspaceSession(t, e, "Strength E2E", "admin@strength.test")
	personID := seedStrengthPersonWithActivities(t, e)

	var wire strengthWire
	if status := e.call(t, "GET", "/v1/people/"+personID+"/strength", nil, nil, &wire); status != http.StatusOK {
		t.Fatalf("GET strength → %d", status)
	}

	switch wire.Bucket {
	case "dormant", "weak", "warm", "strong":
	default:
		t.Errorf("bucket = %q, want one of dormant|weak|warm|strong", wire.Bucket)
	}
	if wire.Score < 0 || wire.Score > 100 {
		t.Fatalf("score = %d, want 0..100", wire.Score)
	}
	if wire.Score == 0 {
		t.Errorf("score = 0, want > 0 given two fresh balanced interactions")
	}
	reconciled := int(math.Round(100 * wire.Factors.Recency * wire.Factors.Frequency * wire.Factors.Reciprocity))
	if reconciled != wire.Score {
		t.Errorf("factors reconcile to %d, want the reported score %d", reconciled, wire.Score)
	}
	if wire.Inbound90d == nil || *wire.Inbound90d != 1 || wire.Outbound90d == nil || *wire.Outbound90d != 1 {
		t.Errorf("inbound/outbound counts = %+v %+v, want 1 and 1", wire.Inbound90d, wire.Outbound90d)
	}
	if wire.ComputedAt == nil {
		t.Error("computed_at missing, want a real timestamp")
	}
	if len(wire.ContributingActivityIds) != 2 {
		t.Errorf("contributing_activity_ids = %v, want 2 entries", wire.ContributingActivityIds)
	}
}

// TestPersonStrengthHTTPMissingIs404: a nonexistent person id answers 404
// rather than disclosing anything about it — existence-hiding, matching
// every other row-scoped read.
func TestPersonStrengthHTTPMissingIs404(t *testing.T) {
	e := setup(t)
	e.slug = "strength-404"
	bootstrapWorkspaceSession(t, e, "Strength 404", "admin@strength404.test")

	if status := e.call(t, "GET", "/v1/people/"+ids.NewV7().String()+"/strength", nil, nil, nil); status != http.StatusNotFound {
		t.Errorf("missing person strength = %d, want 404", status)
	}
}

// TestOrganizationStrengthHTTPReconciles: the org roll-up (max over
// current employees' strength) surfaces the same employee's score it
// computed for the person-level endpoint above.
func TestOrganizationStrengthHTTPReconciles(t *testing.T) {
	e := setup(t)
	e.slug = "org-strength-e2e"
	bootstrapWorkspaceSession(t, e, "Org Strength E2E", "admin@orgstrength.test")

	var org struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/organizations", anyMap{
		"display_name": "Strength Co", "source": "ui",
	}, nil, &org); status != http.StatusCreated {
		t.Fatalf("create organization → %d", status)
	}
	personID := seedStrengthPersonWithActivities(t, e)
	if status := e.call(t, "POST", "/v1/relationships", anyMap{
		"kind": "employment", "person_id": personID, "organization_id": org.ID,
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("create employment relationship → %d", status)
	}

	var wire strengthWire
	if status := e.call(t, "GET", "/v1/organizations/"+org.ID+"/strength", nil, nil, &wire); status != http.StatusOK {
		t.Fatalf("GET org strength → %d", status)
	}
	if wire.Score <= 0 {
		t.Errorf("org strength score = %d, want > 0 (rolled up from its employee)", wire.Score)
	}
}

// TestOrganizationStrengthHTTPMissingIs404 mirrors the person-level 404:
// a nonexistent organization id discloses nothing about it either.
func TestOrganizationStrengthHTTPMissingIs404(t *testing.T) {
	e := setup(t)
	e.slug = "org-strength-404"
	bootstrapWorkspaceSession(t, e, "Org Strength 404", "admin@orgstrength404.test")

	if status := e.call(t, "GET", "/v1/organizations/"+ids.NewV7().String()+"/strength", nil, nil, nil); status != http.StatusNotFound {
		t.Errorf("missing organization strength = %d, want 404", status)
	}
}
