// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The next-best-action read over the real stores: the facts it folds are the
// ones the deal's timeline and open promises actually hold.

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

func TestTheNextBestActionFollowsTheDealsOwnTimeline(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	stages := apptest.DiscoverSeededPipeline(t, e)
	dealID := apptest.CreateOpenDeal(t, e, stages)

	var nba apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/deals/"+dealID+"/next-best-action", nil, nil, &nba); status != http.StatusOK {
		t.Fatalf("nba = %d %v", status, nba)
	}
	if nba["action"] != "create_task" {
		t.Fatalf("an empty deal should ask for a first step, got %v", nba)
	}
	args, _ := nba["arguments"].(map[string]any)
	if args["subject"] != "Agree the next step on Acme rollout" {
		t.Fatalf("arguments = %v", args)
	}

	// Performing it through the verb it names: the task exists, so the answer
	// becomes none and names it.
	if status := e.Call(t, "POST", "/v1/tasks", args, nil, nil); status != http.StatusCreated {
		t.Fatalf("create task from the recommendation = %d", status)
	}
	var after apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/deals/"+dealID+"/next-best-action", nil, nil, &after); status != http.StatusOK {
		t.Fatalf("nba after task = %d %v", status, after)
	}
	if after["action"] != "none" {
		t.Fatalf("after creating the task, got %v", after)
	}

	// An unanswered inbound mail outranks the open task.
	var mail apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/activities", apptest.AnyMap{
		"kind": "email", "direction": "inbound", "subject": "Re: rollout", "body": "Can you send the DPA?",
		"links":  []apptest.AnyMap{{"entity_type": "deal", "entity_id": dealID}},
		"source": "ui",
	}, nil, &mail); status != http.StatusCreated {
		t.Fatalf("log mail = %d %v", status, mail)
	}
	var reply apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/deals/"+dealID+"/next-best-action", nil, nil, &reply); status != http.StatusOK {
		t.Fatalf("nba after mail = %d %v", status, reply)
	}
	replyArgs, _ := reply["arguments"].(map[string]any)
	if reply["action"] != "draft_email" || replyArgs["activity_id"] != mail["id"] {
		t.Fatalf("after an inbound mail, got %v", reply)
	}
}
