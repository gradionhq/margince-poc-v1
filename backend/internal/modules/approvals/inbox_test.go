// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// The inbox query's pure half: that every filter the contract declares
// reaches the SQL, and that the target pair is refused when only half of it
// arrives. What the filtered read then SHOWS — decidability, the row-scope
// prune, the empty answer for an out-of-scope target — needs a database and
// lives in compose/integration/approval_targetfilter_integration_test.go.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
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
	}, nil)

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
	q, args := approvalPageQuery(ListInput{}, nil)
	if strings.Contains(q, "WHERE") {
		t.Errorf("an unfiltered read carries a WHERE clause:\n%s", q)
	}
	if len(args) != 0 {
		t.Errorf("an unfiltered read bound %v", args)
	}

	kind := "deepread"
	q, args = approvalPageQuery(ListInput{Kind: &kind}, after(row{ID: ids.From[ids.ApprovalKind](ids.NewV7())}))
	if !strings.Contains(q, "(created_at, id) < ($2, $3)") {
		t.Errorf("the cursor did not bind after the filter:\n%s", q)
	}
	if len(args) != 3 || args[0] != kind {
		t.Errorf("bound %v, want the filter first and the cursor after it", args)
	}
}

// approvalTable stands in for the ordered scan the query performs — the staged
// rows newest-first by (created_at, id). Only the DATABASE is stood in for:
// decodeStart, capPage and the token itself are the real ones, so what the walk
// below proves is the paging contract this module defines rather than a mock's
// idea of it.
type approvalTable []row

// newApprovalTable is count rows, newest first, one minute apart.
func newApprovalTable(count int) approvalTable {
	newest := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	all := make(approvalTable, 0, count)
	for i := range count {
		all = append(all, row{
			ID:        ids.From[ids.ApprovalKind](ids.NewV7()),
			CreatedAt: newest.Add(time.Duration(-i) * time.Minute),
		})
	}
	return all
}

// page answers one request: the rows after the caller's cursor, cut to the
// display limit by capPage.
func (all approvalTable) page(t *testing.T, in ListInput) ([]row, storekit.Page) {
	t.Helper()
	from, err := startOf(in.Cursor)
	if err != nil {
		t.Fatalf("the cursor a previous page handed back does not decode: %v", err)
	}
	var scanned []row
	for _, a := range all {
		if from == nil || a.CreatedAt.Before(from.createdAt) ||
			(a.CreatedAt.Equal(from.createdAt) && a.ID.String() < from.id.String()) {
			scanned = append(scanned, a)
		}
	}
	return capPage(scanned, in.Limit, nil)
}

// The whole point of next_cursor: a client told there is more can fetch it, and
// walking the pages sees every staged row exactly once. Before the cursor was
// threaded through, every request restarted at the newest row — has_more said
// "there is more" and nothing the client could send would reach it.
func TestPagingTheInboxSeesEveryRowExactlyOnce(t *testing.T) {
	const rows, limit = 5, 2
	all := newApprovalTable(rows)

	var seen []ids.ApprovalID
	in := ListInput{Limit: limit}
	for range rows + 1 {
		got, page := all.page(t, in)
		if len(got) > limit {
			t.Fatalf("a page returned %d rows, want at most the requested %d", len(got), limit)
		}
		seen = append(seen, idsOf(got)...)
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Errorf("a final page still handed back the cursor %q", page.NextCursor)
			}
			if len(got) == limit {
				t.Error("the last page was full; the walk did not end on a short page")
			}
			break
		}
		if page.NextCursor == "" {
			t.Fatal("has_more is true with no next_cursor — the client has no way to ask for the rest")
		}
		last := got[len(got)-1]
		if want := storekit.EncodeCursor(last.CreatedAt, last.ID.UUID); page.NextCursor != want {
			t.Fatalf("next_cursor is %q, want the token of the last returned row %q", page.NextCursor, want)
		}
		in.Cursor = page.NextCursor
	}

	want := idsOf(all)
	if len(seen) != len(want) {
		t.Fatalf("the walk returned %d rows over %d staged approvals — a boundary duplicated or dropped one",
			len(seen), len(want))
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("row %d of the walk is %s, want %s", i, seen[i], want[i])
		}
	}
}

// A page that is not full is the whole answer: no has_more, and no token, so a
// client stops rather than asking for a page that does not exist.
func TestAShortPageReportsNoMoreAndNoCursor(t *testing.T) {
	all := newApprovalTable(3)
	got, page := all.page(t, ListInput{Limit: 50})
	if len(got) != len(all) {
		t.Fatalf("returned %d rows, want all %d", len(got), len(all))
	}
	if page.HasMore {
		t.Error("has_more is true when every row was returned")
	}
	if page.NextCursor != "" {
		t.Errorf("next_cursor is %q on a complete answer", page.NextCursor)
	}
}

// A cursor is client input, so one that is not a page token is the caller's
// fault. It has to travel as storekit's malformed-cursor fault, which is what
// the transport turns into the same 422 every other list answers with — never
// a panic and never a 500.
func TestAMalformedCursorIsAClientFault(t *testing.T) {
	from, err := startOf("not-a-page-token!!")
	if from != nil {
		t.Errorf("a malformed cursor decoded to %+v", from)
	}
	var malformed *storekit.MalformedCursorError
	if !errors.As(err, &malformed) {
		t.Fatalf("decoding a malformed cursor gave %v, want storekit's malformed-cursor fault", err)
	}
}

// A token that DECODES but names no row is the same fault, and the more
// dangerous one: the decode is a JSON unmarshal, so `{}` parses happily into an
// empty cursor that reads as "everything before the beginning of time". The
// caller would be handed a successful, permanently empty page and told nothing,
// silently losing every row they had not yet seen.
func TestACursorThatNamesNoRowIsAClientFaultToo(t *testing.T) {
	tokens := map[string]string{
		"an empty object": base64.RawURLEncoding.EncodeToString([]byte(`{}`)),
		"a time with no id": base64.RawURLEncoding.EncodeToString(
			[]byte(`{"t":"2026-07-30T09:00:00Z"}`)),
		"an id with no time": base64.RawURLEncoding.EncodeToString(
			[]byte(`{"id":"019fac61-2543-7745-9f45-bc520b427079"}`)),
	}
	for name, token := range tokens {
		t.Run(name, func(t *testing.T) {
			from, err := startOf(token)
			if from != nil {
				t.Errorf("%s decoded to a resume point %+v", name, from)
			}
			var malformed *storekit.MalformedCursorError
			if !errors.As(err, &malformed) {
				t.Fatalf("%s gave %v, want storekit's malformed-cursor fault", name, err)
			}
		})
	}
}

// idsOf is the id order one slice of rows carries.
func idsOf(rows []row) []ids.ApprovalID {
	out := make([]ids.ApprovalID, 0, len(rows))
	for _, a := range rows {
		out = append(out, a.ID)
	}
	return out
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
	cursor := storekit.EncodeCursor(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), ids.NewV7())

	in, invalid := listInput(crmcontracts.ListApprovalsParams{
		Status: &status, Kind: &kind, Limit: &limit, Cursor: &cursor,
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
	if in.Cursor != cursor {
		t.Errorf("cursor = %q, want the token the client sent %q", in.Cursor, cursor)
	}

	// Neither half supplied is the unfiltered inbox, not a validation error.
	if in, invalid := listInput(crmcontracts.ListApprovalsParams{}); invalid != nil || in.targeted() {
		t.Errorf("an unfiltered request gave (%+v, %v), want the whole inbox", in, invalid)
	}
}
