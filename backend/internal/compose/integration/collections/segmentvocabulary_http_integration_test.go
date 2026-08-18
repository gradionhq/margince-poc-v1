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
// (compose/collectionswiring_test.go) checks the one constructor both
// surfaces are now built through, so neither can lose the seam alone;
// this drives the endpoints over the real composed server and proves
// that wiring actually PRODUCES an accepted, evaluated filter rather
// than merely passing a structural check.

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
	matchID, ok := matching["id"].(string)
	if !ok || matchID == "" {
		t.Fatalf("created matching person carries no id: %v", matching)
	}

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
	listID, ok := list["id"].(string)
	if !ok || listID == "" {
		t.Fatalf("created list carries no id: %v", list)
	}

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

// The vocabulary READ (LVS-EXT-8), over the composed server, against the
// endpoint that consumes the same vocabulary.
//
// Two things only an HTTP test can prove here. First, that the operation is
// actually served: every contract operation gets a generated 501 stub in
// compose, and a module handler wins only by being embedded one level
// shallower — a mechanism the build cannot fail on, because an unimplemented
// operation compiles perfectly and answers 501 at runtime.
//
// Second, the equivalence the extension is defined by: a field this operation
// lists must be one a filter may name. Asserting that against POST /v1/lists
// rather than against the engine's map closes the loop the store-level suite
// cannot — the two surfaces resolving the same vocabulary is exactly what the
// regression above proved is not automatic.
func TestTheFilterVocabularyOverHTTPListsWhatADynamicListAccepts(t *testing.T) {
	e := apptest.SetupAppWithOptions(t, compose.WithSchemaPool(integration.SchemaPool(t)))
	e.BootstrapWorkspace(t)

	var field apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/custom-fields", apptest.AnyMap{
		"object": "person", "label": "Vocabulary Probe", "type": "picklist", "source": "ui",
		"options": []string{"gold", "silver"},
	}, nil, &field); status != http.StatusCreated {
		t.Fatalf("create custom field: status=%d body=%v", status, field)
	}
	column, ok := field["column_name"].(string)
	if !ok || column == "" {
		t.Fatalf("created field carries no column_name: %v", field)
	}

	var vocab struct {
		Resource string `json:"resource"`
		Fields   []struct {
			Name      string   `json:"name"`
			Type      string   `json:"type"`
			Operators []string `json:"operators"`
			Custom    bool     `json:"custom"`
		} `json:"fields"`
	}
	status := e.Call(t, "GET", "/v1/segments/vocabulary?resource=person", nil, nil, &vocab)
	if status == http.StatusNotImplemented {
		t.Fatal("the operation answered 501: the generated stub is being served, so the module handler is not shadowing it")
	}
	if status != http.StatusOK {
		t.Fatalf("read the filter vocabulary: status=%d body=%v", status, vocab)
	}
	if vocab.Resource != "person" {
		t.Errorf("resource = %q, want the one asked for", vocab.Resource)
	}

	reported := map[string]bool{}
	for _, f := range vocab.Fields {
		reported[f.Name] = true
		switch f.Name {
		case column:
			if !f.Custom {
				t.Errorf("%s is a workspace-defined column and is not reported custom", column)
			}
			if f.Type != "picklist" {
				t.Errorf("%s type = %q, want the picklist the admin created", column, f.Type)
			}
		case "owner_id":
			if f.Custom {
				t.Error("owner_id is a core field and is reported custom")
			}
		}
		if len(f.Operators) == 0 {
			t.Errorf("%s reports no operators, so a builder could offer no clause on it", f.Name)
		}
	}
	if !reported[column] {
		t.Fatalf("the vocabulary omits %s, a column a filter may name", column)
	}
	if !reported["owner_id"] {
		t.Error("the vocabulary omits owner_id, a core field every person filter may name")
	}

	// The equivalence, forwards: a listed field is one a dynamic list accepts.
	var accepted apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/lists", apptest.AnyMap{
		"name": "Reported field is accepted", "entity_type": "person", "list_type": "dynamic",
		"definition": apptest.AnyMap{"field": column, "op": "eq", "value": "gold"},
	}, nil, &accepted); status != http.StatusCreated {
		t.Fatalf("the vocabulary listed %s but a dynamic list on it was refused: status=%d body=%v", column, status, accepted)
	}

	// And backwards: a field it does not list is one the same endpoint refuses,
	// so the omission is a real answer rather than an incomplete one.
	var refused apptest.AnyMap
	unlisted := column + "_not_in_the_catalog"
	if reported[unlisted] {
		t.Fatalf("%s was meant to be absent from the vocabulary", unlisted)
	}
	if status := e.Call(t, "POST", "/v1/lists", apptest.AnyMap{
		"name": "Unreported field is refused", "entity_type": "person", "list_type": "dynamic",
		"definition": apptest.AnyMap{"field": unlisted, "op": "eq", "value": "gold"},
	}, nil, &refused); status != http.StatusUnprocessableEntity {
		t.Fatalf("the vocabulary omits %s but a dynamic list on it was not refused 422: status=%d body=%v", unlisted, status, refused)
	}
}
