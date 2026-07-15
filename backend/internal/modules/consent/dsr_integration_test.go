// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package consent

// The DSR case queue over a real migrated Postgres. Two gates:
// BE-1 — the ?status= filter the contract publishes actually narrows the
// queue (it was parsed and dropped, so every filter returned everything).
// BE-2 — fulfilling an erasure whose subject_ref names no person fails
// loudly instead of certifying a deletion that never ran.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type dsrEnv struct {
	owner    *pgx.Conn
	pool     *pgxpool.Pool
	store    *Store
	ctx      context.Context
	ws, user ids.UUID
}

func setupDSR(t *testing.T) *dsrEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})

	e := &dsrEnv{owner: owner, ws: ids.NewV7(), user: ids.NewV7()}
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'DSR', $2, 'EUR')`,
		e.ws, "dsr-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Officer')`,
		e.user, e.ws, "dpo-"+e.user.String()+"@dsr.test"); err != nil {
		t.Fatal(err)
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.pool = pool
	e.store = NewStore(pool)

	opCtx := principal.WithWorkspaceID(context.Background(), e.ws)
	opCtx = principal.WithCorrelationID(opCtx, ids.NewV7())
	e.ctx = principal.WithActor(opCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.user.String(), UserID: e.user,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"person": {Create: true, Read: true, Update: true, Delete: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	return e
}

func (e *dsrEnv) mustCreate(t *testing.T, kind, subjectRef string) dsrRow {
	t.Helper()
	row, err := e.store.CreateDSR(e.ctx, CreateDSRInput{
		Kind: kind, SubjectRef: subjectRef, DueAt: time.Now().Add(720 * time.Hour),
	})
	if err != nil {
		t.Fatalf("creating %s DSR: %v", kind, err)
	}
	return row
}

// BE-1: ?status= is declared in crm.yaml and was parsed into the params
// struct and never read, so every filter returned the whole queue. The
// bug was in the handler's plumbing of params.Status into the store call,
// so the regression test must drive the handler, not call the store with
// the status pre-supplied — a store-only test still passes if the handler
// drops the parameter on the floor.
func TestListDSRsNarrowsByStatus(t *testing.T) {
	e := setupDSR(t)
	stayOpen := e.mustCreate(t, "access", "open@dsr.test")
	toClose := e.mustCreate(t, "access", "closed@dsr.test")

	answer := "handled by hand"
	if _, err := e.store.UpdateDSR(e.ctx, toClose.ID, UpdateDSRInput{
		Status: strptr("fulfilled"), Resolution: &answer,
	}); err != nil {
		t.Fatalf("closing the second request: %v", err)
	}

	h := NewHandlers(e.pool)
	r := httptest.NewRequest(http.MethodGet, "/v1/data-subject-requests", nil).WithContext(e.ctx)

	openStatus := crmcontracts.ListDataSubjectRequestsParamsStatusOpen
	open := listDSRsOverHTTP(t, h, r, crmcontracts.ListDataSubjectRequestsParams{Status: &openStatus})
	if len(open) != 1 || open[0].Id != openapi_types.UUID(stayOpen.ID) {
		t.Fatalf("status=open must return exactly the open request, got %d rows", len(open))
	}

	fulfilledStatus := crmcontracts.ListDataSubjectRequestsParamsStatusFulfilled
	fulfilled := listDSRsOverHTTP(t, h, r, crmcontracts.ListDataSubjectRequestsParams{Status: &fulfilledStatus})
	if len(fulfilled) != 1 || fulfilled[0].Id != openapi_types.UUID(toClose.ID) {
		t.Fatalf("status=fulfilled must return exactly the closed request, got %d rows", len(fulfilled))
	}

	all := listDSRsOverHTTP(t, h, r, crmcontracts.ListDataSubjectRequestsParams{})
	if len(all) != 2 {
		t.Fatalf("an empty status must not filter: want 2 rows, got %d", len(all))
	}
}

// TestListDataSubjectRequestsRejectsAnUnknownStatus covers the Status.Valid()
// guard: a value outside the closed queue-state vocabulary is a client
// error, not a filter that happens to match nothing.
func TestListDataSubjectRequestsRejectsAnUnknownStatus(t *testing.T) {
	e := setupDSR(t)
	h := NewHandlers(e.pool)
	r := httptest.NewRequest(http.MethodGet, "/v1/data-subject-requests", nil).WithContext(e.ctx)
	bogus := crmcontracts.ListDataSubjectRequestsParamsStatus("bogus")
	w := httptest.NewRecorder()

	h.ListDataSubjectRequests(w, r, crmcontracts.ListDataSubjectRequestsParams{Status: &bogus})

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a status outside the enum must be refused 422, got %d: %s", w.Code, w.Body)
	}
}

// listDSRsOverHTTP drives the handler (not the store) so a regression like
// BE-1 — the handler parsing params.Status and never passing it to the
// store — fails this test rather than passing silently.
func listDSRsOverHTTP(t *testing.T, h Handlers, r *http.Request, params crmcontracts.ListDataSubjectRequestsParams) []crmcontracts.DataSubjectRequest {
	t.Helper()
	w := httptest.NewRecorder()
	h.ListDataSubjectRequests(w, r, params)
	if w.Code != http.StatusOK {
		t.Fatalf("listing over HTTP: got %d: %s", w.Code, w.Body)
	}
	var body struct {
		Data []crmcontracts.DataSubjectRequest `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response body: %v", err)
	}
	return body.Data
}

func strptr(s string) *string { return &s }

func TestFulfillErasureHTTPRefusesAnUnresolvableSubject(t *testing.T) {
	e := setupDSR(t)
	req := e.mustCreate(t, "erasure", "anna.weber@brandt-automotive.de")

	// NewHandlers builds its own store from the pool; WithEraser wires the
	// erase seam compose injects in production.
	eraser := &recordingEraser{}
	h := NewHandlers(e.pool).WithEraser(eraser)
	body := `{"status":"fulfilled","resolution":"verified by phone"}`
	r := httptest.NewRequest(http.MethodPatch, "/v1/data-subject-requests/"+req.ID.String(),
		strings.NewReader(body)).WithContext(e.ctx)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.UpdateDataSubjectRequest(w, r, openapi_types.UUID(req.ID))

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an erasure whose subject_ref names no person must be refused 422, got %d: %s", w.Code, w.Body)
	}
	// Four distinct paths through this handler return 422; assert the one
	// this scenario must hit, not just "a" 422.
	if field := validationField(t, w.Body.Bytes()); field != "subject_ref" {
		t.Fatalf("want the subject_ref field to fail, got %q: %s", field, w.Body)
	}
	after, err := e.store.GetDSR(e.ctx, req.ID)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if after.Status != "open" {
		t.Fatalf("a refused fulfilment must not move the request: status=%q", after.Status)
	}
	if eraser.calls != 0 {
		t.Fatalf("nothing may be erased for an unresolvable subject, got %d call(s)", eraser.calls)
	}
}

// TestFulfillErasureHTTPRefusesAMissingResolution proves the erase side
// effect never runs when the same request that triggers it would be
// refused by UpdateDSR's own "closing needs an answer" guard — a freshly
// created DSR has no resolution, so a bare {"status":"fulfilled"} must be
// rejected before ErasePerson is ever called, not after.
func TestFulfillErasureHTTPRefusesAMissingResolution(t *testing.T) {
	e := setupDSR(t)
	req := e.mustCreate(t, "erasure", ids.NewV7().String())

	eraser := &recordingEraser{}
	h := NewHandlers(e.pool).WithEraser(eraser)
	body := `{"status":"fulfilled"}`
	r := httptest.NewRequest(http.MethodPatch, "/v1/data-subject-requests/"+req.ID.String(),
		strings.NewReader(body)).WithContext(e.ctx)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.UpdateDataSubjectRequest(w, r, openapi_types.UUID(req.ID))

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("fulfilling with no resolution must be refused 422, got %d: %s", w.Code, w.Body)
	}
	if eraser.calls != 0 {
		t.Fatalf("the person must not be erased before the resolution guard is checked, got %d call(s)", eraser.calls)
	}
	after, err := e.store.GetDSR(e.ctx, req.ID)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if after.Status != "open" {
		t.Fatalf("a refused fulfilment must not move the request: status=%q", after.Status)
	}
}

// TestFulfillErasureHTTPRefusesAnAlreadyRejectedRequest proves a stale
// fulfil against a request another officer already rejected is refused
// before the erase runs — the illegal transition (rejected → fulfilled)
// must be caught ahead of ErasePerson, not discovered afterward by
// UpdateDSR once the person is already gone.
func TestFulfillErasureHTTPRefusesAnAlreadyRejectedRequest(t *testing.T) {
	e := setupDSR(t)
	req := e.mustCreate(t, "erasure", ids.NewV7().String())
	answer := "the subject withdrew the request"
	if _, err := e.store.UpdateDSR(e.ctx, req.ID, UpdateDSRInput{
		Status: strptr("rejected"), Resolution: &answer,
	}); err != nil {
		t.Fatalf("rejecting the request: %v", err)
	}

	eraser := &recordingEraser{}
	h := NewHandlers(e.pool).WithEraser(eraser)
	body := `{"status":"fulfilled","resolution":"verified by phone"}`
	r := httptest.NewRequest(http.MethodPatch, "/v1/data-subject-requests/"+req.ID.String(),
		strings.NewReader(body)).WithContext(e.ctx)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.UpdateDataSubjectRequest(w, r, openapi_types.UUID(req.ID))

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("fulfilling an already-rejected request must be refused 422, got %d: %s", w.Code, w.Body)
	}
	if eraser.calls != 0 {
		t.Fatalf("a rejected request's subject must not be erased, got %d call(s)", eraser.calls)
	}
	after, err := e.store.GetDSR(e.ctx, req.ID)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if after.Status != "rejected" {
		t.Fatalf("a refused fulfilment must not move the request off rejected: status=%q", after.Status)
	}
}

// validationField decodes an httperr.Validation problem+json body and
// returns the failing field, so a test can assert WHICH guard fired
// instead of merely that the status code was 422.
func validationField(t *testing.T, body []byte) string {
	t.Helper()
	var problem struct {
		Details struct {
			Errors []struct {
				Field string `json:"field"`
			} `json:"errors"`
		} `json:"details"`
	}
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decoding problem body: %v", err)
	}
	if len(problem.Details.Errors) != 1 {
		t.Fatalf("want exactly one field error, got %d: %s", len(problem.Details.Errors), body)
	}
	return problem.Details.Errors[0].Field
}

// recordingEraser satisfies consent.Eraser (handlers.go:30) and proves the
// refusal happens BEFORE any erase is attempted.
type recordingEraser struct{ calls int }

func (e *recordingEraser) ErasePerson(context.Context, ids.UUID, string) error {
	e.calls++
	return nil
}
