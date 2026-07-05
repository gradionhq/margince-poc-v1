// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// remind_at (B-E16.1): a task-only reminder column. The field round-trips
// on task create/patch, and the widened activity_task_fields CHECK
// answers a typed 422 (never a 500) when a non-task kind carries it.

import (
	"net/http"
	"testing"
)

func TestRemindAtIsTaskOnlyAndRoundTrips(t *testing.T) {
	e := setup(t)
	e.slug = "reminders"
	bootstrapWorkspaceSession(t, e, "Reminders", "admin@reminders.test")

	// Round-trip on create: the reminder lands and reads back.
	var created struct {
		ID       string  `json:"id"`
		RemindAt *string `json:"remind_at"`
	}
	if status := e.call(t, "POST", "/v1/activities", anyMap{
		"kind": "task", "subject": "Call back", "remind_at": "2026-07-08T09:00:00Z",
	}, nil, &created); status != http.StatusCreated {
		t.Fatalf("create task with remind_at → %d", status)
	}
	if created.RemindAt == nil {
		t.Fatal("remind_at did not round-trip on create")
	}

	// Round-trip on patch: moving the reminder sticks.
	var patched struct {
		RemindAt *string `json:"remind_at"`
	}
	if status := e.call(t, "PATCH", "/v1/activities/"+created.ID, anyMap{
		"remind_at": "2026-07-09T10:30:00Z",
	}, nil, &patched); status != http.StatusOK {
		t.Fatalf("patch remind_at → %d", status)
	}
	if patched.RemindAt == nil || *patched.RemindAt == *created.RemindAt {
		t.Fatalf("patched remind_at = %v, want the moved reminder", patched.RemindAt)
	}

	// A non-task kind cannot carry a reminder: the widened
	// activity_task_fields CHECK answers a typed 422.
	var problem struct {
		Code string `json:"code"`
	}
	status := e.call(t, "POST", "/v1/activities", anyMap{
		"kind": "note", "body": "no reminders on notes", "remind_at": "2026-07-08T09:00:00Z",
	}, nil, &problem)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("note with remind_at → %d, want 422", status)
	}

	// Same rule on the update path: a reminder cannot be patched onto a
	// non-task kind after the fact.
	var note struct {
		ID string `json:"id"`
	}
	if s := e.call(t, "POST", "/v1/activities", anyMap{"kind": "note", "body": "plain"}, nil, &note); s != http.StatusCreated {
		t.Fatalf("create note → %d", s)
	}
	if s := e.call(t, "PATCH", "/v1/activities/"+note.ID, anyMap{"remind_at": "2026-07-08T09:00:00Z"}, nil, nil); s != http.StatusUnprocessableEntity {
		t.Fatalf("patch remind_at onto a note → %d, want 422", s)
	}
}
