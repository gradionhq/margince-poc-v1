// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package collections

// The HTTP half of the segment-vocabulary proof. The store-level suite
// (segmentvocabulary_integration_test.go) proves the engine and the
// merge; this one proves compose's OWN wiring — the exact regression a
// review found: a cf_* filter that filtered export accepted was refused
// 422 by POST /v1/lists, because collections' catalog was wired into
// only one of its two handler stores. A reflection test
// (compose/collectionswiring_test.go) already checks that both entry
// points call WithFieldCatalog; this drives them over the real composed
// server, through the object compose itself constructs in
// newCollectionsHandlers, and proves that wiring actually PRODUCES an
// accepted, evaluated filter rather than merely passing a structural
// check.

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

// TestADynamicListOverHTTPAcceptsAndEvaluatesACustomFieldFilter is the
// point of the task: the same two endpoints a review found disagreeing
// (POST /v1/lists refusing 422 what GET-via-export accepted) must now
// both accept a cf_* predicate AND evaluate it to the right membership,
// through the server compose actually assembles — no hand-built store.
func TestADynamicListOverHTTPAcceptsAndEvaluatesACustomFieldFilter(t *testing.T) {
	e := apptest.SetupAppWithOptions(t, compose.WithSchemaPool(integration.SchemaPool(t)))
	e.BootstrapWorkspace(t)

	var field apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/custom-fields", apptest.AnyMap{
		"object": "person", "label": "Loyalty Tier HTTP", "type": "text", "source": "ui",
	}, nil, &field); status != http.StatusCreated {
		t.Fatalf("create custom field: status=%d body=%v", status, field)
	}
	column, ok := field["column_name"].(string)
	if !ok || column == "" {
		t.Fatalf("created field carries no column_name: %v", field)
	}

	var matching apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/people", apptest.AnyMap{
		"full_name": "Match", "source": "ui",
	}, nil, &matching); status != http.StatusCreated {
		t.Fatalf("create matching person: status=%d body=%v", status, matching)
	}
	matchID, _ := matching["id"].(string)

	var other apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/people", apptest.AnyMap{
		"full_name": "Other", "source": "ui",
	}, nil, &other); status != http.StatusCreated {
		t.Fatalf("create non-matching person: status=%d body=%v", status, other)
	}

	// Set through the update path, exactly like the store-level scenario:
	// a value a customer fills in after the fact must filter the same way.
	var updated apptest.AnyMap
	if status := e.Call(t, "PATCH", "/v1/people/"+matchID, apptest.AnyMap{column: "gold"}, nil, &updated); status != http.StatusOK {
		t.Fatalf("set the custom field through the update path: status=%d body=%v", status, updated)
	}

	// The regression's own repro: this exact call used to answer 422 at
	// the endpoint filtered export already accepted the same predicate on.
	var list apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/lists", apptest.AnyMap{
		"name": "Gold tier", "entity_type": "person", "list_type": "dynamic",
		"definition": apptest.AnyMap{"field": column, "op": "eq", "value": "gold"},
	}, nil, &list); status != http.StatusCreated {
		t.Fatalf("a dynamic list on a custom field was refused over HTTP: status=%d body=%v", status, list)
	}
	listID, _ := list["id"].(string)

	var members struct {
		Data []apptest.AnyMap `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/lists/"+listID+"/members", nil, nil, &members); status != http.StatusOK {
		t.Fatalf("list members: status=%d", status)
	}
	if len(members.Data) != 1 || members.Data[0]["entity_id"] != matchID {
		t.Fatalf("members = %v, want exactly [%s]", members.Data, matchID)
	}
}
