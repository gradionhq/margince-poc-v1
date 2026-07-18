// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The dedupe review queue over the real wire (DH-EXT-1/2): a manual
// fuzzy org create leaves the pair in GET /dedupe/candidates, the
// evidence snapshot rides through verbatim, dismissal/undo/merge answer
// the DH-EXT-2 contract, and the refusals are typed 422/404/409 — the
// store's own behaviour is proved in the people package; this suite owns
// the transport.

import (
	"net/http"
	"testing"
)

type dedupeCandidateDTO struct {
	ID         string  `json:"id"`
	EntityType string  `json:"entity_type"`
	LeftID     string  `json:"left_id"`
	RightID    string  `json:"right_id"`
	Confidence float64 `json:"confidence"`
	Status     string  `json:"status"`
	Evidence   []struct {
		Field      string  `json:"field"`
		LeftValue  *string `json:"left_value"`
		RightValue *string `json:"right_value"`
		Signal     string  `json:"signal"`
	} `json:"evidence"`
}

type dedupeListDTO struct {
	Data []dedupeCandidateDTO `json:"data"`
	Page *struct {
		HasMore    bool    `json:"has_more"`
		NextCursor *string `json:"next_cursor"`
	} `json:"page"`
}

// seedOrgPairHTTP creates an incumbent org and a same-stem near-duplicate
// through the real create endpoint; the create records the queue pair.
func seedOrgPairHTTP(t *testing.T, e *env, stem, incumbentDomain, dupDomain string) (incumbentID string) {
	t.Helper()
	var org struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/organizations", anyMap{
		"display_name": stem + " GmbH",
		"domains":      []anyMap{{"domain": incumbentDomain, "is_primary": true}},
	}, nil, &org); status != http.StatusCreated {
		t.Fatalf("incumbent create → %d, want 201", status)
	}
	if status := e.call(t, "POST", "/v1/organizations", anyMap{
		"display_name": stem + " Inc",
		"domains":      []anyMap{{"domain": dupDomain, "is_primary": true}},
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("fuzzy create must still 201, got %d", status)
	}
	return org.ID
}

func TestDedupeQueueOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	incumbentID := seedOrgPairHTTP(t, e, "Umbrella Holdings", "umbrella.example", "umbrella-us.example")

	var queue dedupeListDTO
	if status := e.call(t, "GET", "/v1/dedupe/candidates", nil, nil, &queue); status != http.StatusOK {
		t.Fatalf("list → %d, want 200", status)
	}
	if len(queue.Data) != 1 {
		t.Fatalf("open queue holds %d rows, want the one recorded pair", len(queue.Data))
	}
	c := queue.Data[0]
	if c.EntityType != "organization" || c.Status != "open" {
		t.Fatalf("candidate = %s/%s, want organization/open", c.EntityType, c.Status)
	}
	if len(c.Evidence) == 0 || c.Evidence[0].Field != "display_name" {
		t.Fatalf("evidence %+v does not carry the display_name collision", c.Evidence)
	}

	var one dedupeCandidateDTO
	if status := e.call(t, "GET", "/v1/dedupe/candidates/"+c.ID, nil, nil, &one); status != http.StatusOK || one.ID != c.ID {
		t.Fatalf("get → %d (id %s), want 200 for %s", status, one.ID, c.ID)
	}
	if status := e.call(t, "GET", "/v1/dedupe/candidates/00000000-0000-7000-8000-000000000000", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("unknown id → %d, want 404", status)
	}
	if status := e.call(t, "GET", "/v1/dedupe/candidates?cursor=%21broken", nil, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("malformed cursor → %d, want 422", status)
	}

	// DH-EXT-2 refusals: an unknown verb and a merge without a winner.
	if status := e.call(t, "POST", "/v1/dedupe/candidates/"+c.ID+"/disposition",
		anyMap{"disposition": "merge"}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("merge without winner → %d, want 422", status)
	}

	// Dismiss, verify the flip, refuse the second decision, undo.
	var decided dedupeCandidateDTO
	if status := e.call(t, "POST", "/v1/dedupe/candidates/"+c.ID+"/disposition",
		anyMap{"disposition": "not_a_duplicate"}, nil, &decided); status != http.StatusOK || decided.Status != "not_a_duplicate" {
		t.Fatalf("dismiss → %d/%s, want 200/not_a_duplicate", status, decided.Status)
	}
	if status := e.call(t, "POST", "/v1/dedupe/candidates/"+c.ID+"/disposition",
		anyMap{"disposition": "not_a_duplicate"}, nil, nil); status != http.StatusConflict {
		t.Fatalf("second decision → %d, want 409", status)
	}
	var reopened dedupeCandidateDTO
	if status := e.call(t, "POST", "/v1/dedupe/candidates/"+c.ID+"/undo", nil, nil, &reopened); status != http.StatusOK || reopened.Status != "open" {
		t.Fatalf("undo → %d/%s, want 200/open", status, reopened.Status)
	}

	// Merge: the winner survives, and a merged pair cannot be undone.
	var merged dedupeCandidateDTO
	if status := e.call(t, "POST", "/v1/dedupe/candidates/"+c.ID+"/disposition",
		anyMap{"disposition": "merge", "winner_id": incumbentID}, nil, &merged); status != http.StatusOK || merged.Status != "merged" {
		t.Fatalf("merge → %d/%s, want 200/merged", status, merged.Status)
	}
	if status := e.call(t, "POST", "/v1/dedupe/candidates/"+c.ID+"/undo", nil, nil, nil); status != http.StatusConflict {
		t.Fatalf("undo on merged → %d, want 409 (PO-AC-M6: no merge reversal)", status)
	}

	// The decided pair filters by status; the open queue is empty again.
	var open dedupeListDTO
	if status := e.call(t, "GET", "/v1/dedupe/candidates?status=open", nil, nil, &open); status != http.StatusOK || len(open.Data) != 0 {
		t.Fatalf("open list after merge → %d with %d rows, want 200 with 0", status, len(open.Data))
	}
	var mergedList dedupeListDTO
	if status := e.call(t, "GET", "/v1/dedupe/candidates?status=merged&entity_type=organization", nil, nil, &mergedList); status != http.StatusOK || len(mergedList.Data) != 1 {
		t.Fatalf("merged list → %d with %d rows, want 200 with 1", status, len(mergedList.Data))
	}
}

func TestDedupeQueuePagesOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	seedOrgPairHTTP(t, e, "Vandelay Industries", "vandelay.example", "vandelay-us.example")
	seedOrgPairHTTP(t, e, "Initech Systems", "initech.example", "initech-us.example")

	var page1 dedupeListDTO
	if status := e.call(t, "GET", "/v1/dedupe/candidates?limit=1", nil, nil, &page1); status != http.StatusOK {
		t.Fatalf("page 1 → %d, want 200", status)
	}
	if len(page1.Data) != 1 || page1.Page == nil || !page1.Page.HasMore || page1.Page.NextCursor == nil {
		t.Fatalf("page 1 = %d rows, page %+v — want 1 row with a next cursor", len(page1.Data), page1.Page)
	}
	var page2 dedupeListDTO
	if status := e.call(t, "GET", "/v1/dedupe/candidates?limit=1&cursor="+*page1.Page.NextCursor, nil, nil, &page2); status != http.StatusOK {
		t.Fatalf("page 2 → %d, want 200", status)
	}
	if len(page2.Data) != 1 || page2.Data[0].ID == page1.Data[0].ID {
		t.Fatalf("page 2 re-served page 1's row")
	}
}
