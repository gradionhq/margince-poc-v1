// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// The inbox query's pure half: that every filter the contract declares
// reaches the SQL, and that the target pair is refused when only half of it
// arrives. What the filtered read then SHOWS — decidability, the row-scope
// prune, the empty answer for an out-of-scope target — needs a database and
// lives in compose/integration/approval_targetfilter_integration_test.go.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A declared filter the query never renders is a promise the server does not
// keep: the client narrows its request, the server answers the whole inbox,
// and nothing anywhere says so. Each parameter is checked as a bound argument
// rather than as literal text, because a filter spliced into the SQL would
// pass a substring assertion and be an injection.
func TestEveryDeclaredFilterReachesTheQuery(t *testing.T) {
	kind := "site_lead"
	status := "pending"
	targetType := "organization"
	targetID := ids.NewV7()

	q, args := approvalPageQuery(ListInput{
		Status: &status, Kind: &kind, TargetType: &targetType, TargetID: &targetID,
	}, nil, nil, PendingScanCap)

	for _, want := range []string{"status = $1", "kind = $2", "target_entity_type = $3", "target_entity_id = $4"} {
		if !strings.Contains(q, want) {
			t.Errorf("query is missing %q:\n%s", want, q)
		}
	}
	wantArgs := []any{status, kind, targetType, targetID}
	if len(args) != len(wantArgs) {
		t.Fatalf("bound %d arguments, want %d: %v", len(args), len(wantArgs), args)
	}
	for i, want := range wantArgs {
		if args[i] != want {
			t.Errorf("argument $%d is %v, want %v", i+1, args[i], want)
		}
	}
}

// An unfiltered read must not carry an empty WHERE, and the keyset cursor has
// to number its own binds after the filters — a builder that numbered them
// first would page one filtered inbox with another's arguments.
func TestTheCursorBindsAfterTheFilters(t *testing.T) {
	q, args := approvalPageQuery(ListInput{}, nil, nil, inboxBatch)
	if strings.Contains(q, "WHERE") {
		t.Errorf("an unfiltered read carries a WHERE clause:\n%s", q)
	}
	if len(args) != 0 {
		t.Errorf("an unfiltered read bound %v", args)
	}

	kind := "deepread"
	after := row{ID: ids.From[ids.ApprovalKind](ids.NewV7())}
	q, args = approvalPageQuery(ListInput{Kind: &kind}, &after.CreatedAt, &after.ID, inboxBatch)
	if !strings.Contains(q, "(created_at, id) < ($2, $3)") {
		t.Errorf("the cursor did not bind after the filter:\n%s", q)
	}
	if len(args) != 3 || args[0] != kind {
		t.Errorf("bound %v, want the filter first and the cursor after it", args)
	}
}

// Half a target reference filters nothing a client could have meant: a type
// alone matches every record of that type, an id alone every type carrying
// that id. Refusing it is what keeps "I asked about this company" from
// silently answering with the whole workspace's inbox.
func TestListApprovalsRefusesHalfATargetReference(t *testing.T) {
	targetType := "organization"
	targetID := openapi_types.UUID(ids.NewV7())

	for name, params := range map[string]crmcontracts.ListApprovalsParams{
		"type without id": {TargetEntityType: &targetType},
		"id without type": {TargetEntityId: &targetID},
	} {
		t.Run(name, func(t *testing.T) {
			// The service is never reached, so a nil one is the honest fixture:
			// a handler that called it would panic rather than pass.
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1/approvals", nil)
			NewHandlers(nil).ListApprovals(rec, req, params)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body %s", rec.Code, rec.Body.String())
			}
			var problem struct {
				Code    string `json:"code"`
				Details struct {
					Errors []struct {
						Field string `json:"field"`
						Code  string `json:"code"`
					} `json:"errors"`
				} `json:"details"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decoding the problem body: %v", err)
			}
			if problem.Code != "validation_error" {
				t.Errorf("code = %q, want validation_error", problem.Code)
			}
			if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Code != "requires_pair" {
				t.Errorf("errors = %+v, want one requires_pair finding naming the field",
					problem.Details.Errors)
			}
		})
	}
}

// The whole pair binds through to the query, and so does the kind the contract
// has declared all along.
func TestTheWholeTargetPairAndKindBind(t *testing.T) {
	targetType := "organization"
	targetID := ids.NewV7()
	kind := "site_lead"
	status := crmcontracts.ListApprovalsParamsStatusPending
	limit := 25

	in, invalid := listInput(crmcontracts.ListApprovalsParams{
		Status: &status, Kind: &kind, Limit: &limit,
		TargetEntityType: &targetType, TargetEntityId: (*openapi_types.UUID)(&targetID),
	})
	if invalid != nil {
		t.Fatalf("a complete pair was refused: %v", invalid)
	}
	if !in.targeted() {
		t.Error("a complete pair did not read as a target-scoped request")
	}
	if in.TargetID == nil || *in.TargetID != targetID {
		t.Errorf("target id = %v, want %v", in.TargetID, targetID)
	}
	if in.Kind == nil || *in.Kind != kind {
		t.Errorf("kind = %v, want %q", in.Kind, kind)
	}
	if in.Status == nil || *in.Status != statusPending {
		t.Errorf("status = %v, want %q", in.Status, statusPending)
	}
	if in.Limit != limit {
		t.Errorf("limit = %d, want %d", in.Limit, limit)
	}

	// Neither half supplied is the unfiltered inbox, not a validation error.
	if in, invalid := listInput(crmcontracts.ListApprovalsParams{}); invalid != nil || in.targeted() {
		t.Errorf("an unfiltered request gave (%+v, %v), want the whole inbox", in, invalid)
	}
}
