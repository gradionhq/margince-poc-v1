// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// POST /tasks is the one door that says a task is an activity of kind task:
// what it creates must be exactly what GET /activities lists as one.

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

func TestATaskCreatedThroughItsOwnDoorIsATaskOnTheTimeline(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	stages := apptest.DiscoverSeededPipeline(t, e)
	dealID := apptest.CreateOpenDeal(t, e, stages)

	var task apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/tasks", apptest.AnyMap{
		"subject": "Send the redline",
		"due_at":  "2026-09-01T09:00:00+02:00",
		"links":   []apptest.AnyMap{{"entity_type": "deal", "entity_id": dealID}},
		"source":  "ui",
	}, nil, &task); status != http.StatusCreated {
		t.Fatalf("create task = %d %v", status, task)
	}
	if task["kind"] != "task" || task["subject"] != "Send the redline" || task["due_at"] == nil {
		t.Fatalf("task = %v", task)
	}

	var listed apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/activities?kind=task&entity_type=deal&entity_id="+dealID, nil, nil, &listed); status != http.StatusOK {
		t.Fatalf("list = %d %v", status, listed)
	}
	rows, _ := listed["data"].([]any)
	if len(rows) != 1 {
		t.Fatalf("timeline tasks = %v, want the one created", rows)
	}
	if status := e.Call(t, "POST", "/v1/tasks", apptest.AnyMap{"subject": "", "source": "ui"}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("empty subject = %d, want 422", status)
	}
}
