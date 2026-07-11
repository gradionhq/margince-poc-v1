// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The HTTP half of the sort/filter vocabulary coverage (the store-level
// semantics live in customfields_vocab_integration_test.go): the sort
// and cf_* filter query parameters travel the real compose stack, a
// cf_-sorted page comes back ordered, and the vocabulary refusals reach
// the wire as the contract's 422 codes.

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// listPeopleNames GETs /v1/people with the given query string and
// returns the page's full_name column in order.
func listPeopleNames(t *testing.T, e *env, query string) []string {
	t.Helper()
	var list struct {
		Data []anyMap `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/people"+query, nil, nil, &list); status != http.StatusOK {
		t.Fatalf("GET /v1/people%s status = %d", query, status)
	}
	names := make([]string, len(list.Data))
	for i, row := range list.Data {
		name, ok := row["full_name"].(string)
		if !ok {
			t.Fatalf("row %d carries no full_name: %v", i, row)
		}
		names[i] = name
	}
	return names
}

// assert422Code GETs the path and asserts the validation problem's one
// field error carries the expected machine code.
func assert422Code(t *testing.T, e *env, path, wantCode string) {
	t.Helper()
	var problem customFieldProblem
	status := e.call(t, "GET", path, nil, nil, &problem)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("GET %s status = %d, want 422 (%+v)", path, status, problem)
	}
	if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Code != wantCode {
		t.Fatalf("GET %s details = %+v, want one %s entry", path, problem.Details, wantCode)
	}
}

func TestCustomFieldVocabHTTP(t *testing.T) {
	e := schemaWiredEnv(t)

	status, tier, problem := createCustomField(t, e, anyMap{
		"object": "person", "label": "Tier", "type": "text", "source": "ui",
	})
	if status != http.StatusCreated {
		t.Fatalf("create person field status = %d: %+v", status, problem)
	}
	col := tier.ColumnName

	createWithCF(t, e, "/v1/people", anyMap{"full_name": "Person B", "source": "ui", col: "beta"})
	createWithCF(t, e, "/v1/people", anyMap{"full_name": "Person A", "source": "ui", col: "alpha"})
	createWithCF(t, e, "/v1/people", anyMap{"full_name": "Person N", "source": "ui"})

	t.Run("cf_ sort orders the page, NULL last", func(t *testing.T) {
		got := listPeopleNames(t, e, "?sort="+col)
		want := []string{"Person A", "Person B", "Person N"}
		for i := range want {
			if i >= len(got) || got[i] != want[i] {
				t.Fatalf("sorted names = %v, want %v", got, want)
			}
		}
	})

	t.Run("cf_ filter narrows to the equality match", func(t *testing.T) {
		got := listPeopleNames(t, e, "?"+col+"=alpha")
		if len(got) != 1 || got[0] != "Person A" {
			t.Fatalf("filtered names = %v, want [Person A]", got)
		}
	})

	t.Run("unknown cf_ sort answers 422 sort_field_not_allowed", func(t *testing.T) {
		assert422Code(t, e, "/v1/people?sort=cf_never_defined", "sort_field_not_allowed")
	})

	t.Run("unknown cf_ filter answers 422 filter_field_not_allowed", func(t *testing.T) {
		assert422Code(t, e, "/v1/people?cf_never_defined=x", "filter_field_not_allowed")
	})

	t.Run("multi-field sort answers 422 sort_unsupported", func(t *testing.T) {
		assert422Code(t, e, "/v1/people?sort=-created_at,full_name", "sort_unsupported")
	})

	status, score, problem := createCustomField(t, e, anyMap{
		"object": "person", "label": "Score", "type": "number", "source": "ui",
	})
	if status != http.StatusCreated {
		t.Fatalf("create person number field status = %d: %+v", status, problem)
	}

	t.Run("crafted cursor sort key answers 422 malformed_cursor, never 500", func(t *testing.T) {
		badKey := "abc"
		crafted := craftCursor(t, storekit.Cursor{
			CreatedAt: time.Now().UTC(), ID: ids.NewV7(),
			SortField: score.ColumnName, SortKey: &badKey,
		})
		assert422Code(t, e,
			"/v1/people?sort="+score.ColumnName+"&cursor="+url.QueryEscape(crafted),
			"malformed_cursor")
	})

	t.Run("cursor reused under another sort answers 422 cursor_param_mismatch", func(t *testing.T) {
		var list struct {
			Page struct {
				NextCursor string `json:"next_cursor"`
				HasMore    bool   `json:"has_more"`
			} `json:"page"`
		}
		path := "/v1/people?sort=" + score.ColumnName + "&limit=1"
		if status := e.call(t, "GET", path, nil, nil, &list); status != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, status)
		}
		if !list.Page.HasMore || list.Page.NextCursor == "" {
			t.Fatalf("expected a sorted next-page cursor, got %+v", list.Page)
		}
		assert422Code(t, e, "/v1/people?cursor="+url.QueryEscape(list.Page.NextCursor), "cursor_param_mismatch")
	})
}
