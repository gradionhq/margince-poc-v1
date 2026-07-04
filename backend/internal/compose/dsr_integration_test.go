//go:build integration

package compose_test

// Data-subject requests (Art. 15/16/17): the compliance queue is
// human-only at the transport, the status machine is closed (a closed
// request never reopens), and closing demands the answer.

import (
	"net/http"
	"testing"
	"time"
)

func TestDataSubjectRequestLifecycle(t *testing.T) {
	c := setupConsent(t)

	due := time.Now().AddDate(0, 0, 30).UTC().Format(time.RFC3339)
	var dsr struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if status := c.call(t, "POST", "/v1/data-subject-requests", anyMap{
		"kind": "erasure", "subject_ref": c.personID, "due_at": due,
	}, nil, &dsr); status != http.StatusCreated || dsr.Status != "open" {
		t.Fatalf("create DSR → %d %+v", status, dsr)
	}

	// Closing without a resolution refuses.
	if status := c.call(t, "PATCH", "/v1/data-subject-requests/"+dsr.ID, anyMap{
		"status": "fulfilled",
	}, nil, nil); status != 422 {
		t.Fatalf("resolution-less close → %d, want 422", status)
	}
	if status := c.call(t, "PATCH", "/v1/data-subject-requests/"+dsr.ID, anyMap{
		"status": "in_progress",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("start → %d", status)
	}
	if status := c.call(t, "PATCH", "/v1/data-subject-requests/"+dsr.ID, anyMap{
		"status": "fulfilled", "resolution": "erased person + activities per retention policy",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("fulfill → %d", status)
	}
	// A closed request never reopens.
	if status := c.call(t, "PATCH", "/v1/data-subject-requests/"+dsr.ID, anyMap{
		"status": "open",
	}, nil, nil); status != 422 {
		t.Fatalf("reopen → %d, want 422", status)
	}

	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if status := c.call(t, "GET", "/v1/data-subject-requests", nil, nil, &list); status != http.StatusOK || len(list.Data) != 1 {
		t.Fatalf("list → %d %+v", status, list)
	}

	// Agent bearers are rejected at the transport (x-agent-access).
	var minted struct {
		Token string `json:"token"`
	}
	if status := c.call(t, "POST", "/v1/passports", anyMap{
		"label": "dsr probe", "scopes": []string{"read", "write"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("mint passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}
	if status := c.call(t, "POST", "/v1/data-subject-requests", anyMap{
		"kind": "access", "subject_ref": "x", "due_at": due,
	}, bearer, nil); status != http.StatusForbidden {
		t.Fatalf("agent DSR create → %d, want 403 (human-only)", status)
	}
}
