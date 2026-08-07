// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The staff-mediated GDPR surface end-to-end: workspace bootstrap
// seeds the §3.4 default retention policies exactly, and fulfilling an
// erasure DSR EXECUTES the erasure — the status flip and the deletion
// cannot drift apart.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

func TestBootstrapSeedsDefaultRetentionPolicies(t *testing.T) {
	e := setupRelationships(t)

	rows, err := e.Owner.Query(context.Background(), `
		SELECT object_type, coalesce(category, ''), retain_days, action
		FROM retention_policy ORDER BY object_type, retain_days`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var object, category, action string
		var days int
		if err := rows.Scan(&object, &category, &days, &action); err != nil {
			t.Fatal(err)
		}
		got = append(got, object+"/"+category+"/"+action)
		_ = days
	}
	want := []string{
		"activity/transcript/erase", "activity//archive",
		"ai_call_payload/content/erase",
		"deal/lost/archive", "lead/unconverted/anonymize",
		"person/no_consent_no_deal/anonymize",
	}
	if len(got) != len(want) {
		t.Fatalf("seeded %d policies %v, want the §3.4 five + ai_call_payload/content/erase", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("policy %d = %s, want %s (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestFulfillingAnErasureDSRExecutesTheErasure(t *testing.T) {
	e := setupRelationships(t)

	var dsr struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/data-subject-requests", apptest.AnyMap{
		"kind": "erasure", "subject_ref": e.personID, "due_at": "2026-08-01T00:00:00Z",
	}, nil, &dsr); status != http.StatusCreated {
		t.Fatalf("create DSR → %d", status)
	}
	if status := e.Call(t, "PATCH", "/v1/data-subject-requests/"+dsr.ID, apptest.AnyMap{
		"status": "fulfilled", "resolution": "erased per Art. 17",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("fulfill DSR → %d", status)
	}

	var person struct {
		FullName string `json:"full_name"`
	}
	if status := e.Call(t, "GET", "/v1/people/"+e.personID, nil, nil, &person); status != http.StatusOK {
		t.Fatalf("read person → %d", status)
	}
	if person.FullName != "Erased Subject" {
		t.Fatalf("fulfilled erasure left the subject intact: %q", person.FullName)
	}
}
