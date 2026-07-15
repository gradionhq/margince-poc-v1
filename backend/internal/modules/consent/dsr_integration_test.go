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
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

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
// struct and never read, so every filter returned the whole queue.
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

	open, _, err := e.store.ListDSRs(e.ctx, nil, "", "open")
	if err != nil {
		t.Fatalf("listing open: %v", err)
	}
	if len(open) != 1 || open[0].ID != stayOpen.ID {
		t.Fatalf("status=open must return exactly the open request, got %d rows", len(open))
	}

	fulfilled, _, err := e.store.ListDSRs(e.ctx, nil, "", "fulfilled")
	if err != nil {
		t.Fatalf("listing fulfilled: %v", err)
	}
	if len(fulfilled) != 1 || fulfilled[0].ID != toClose.ID {
		t.Fatalf("status=fulfilled must return exactly the closed request, got %d rows", len(fulfilled))
	}

	all, _, err := e.store.ListDSRs(e.ctx, nil, "", "")
	if err != nil {
		t.Fatalf("listing all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("an empty status must not filter: want 2 rows, got %d", len(all))
	}
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

// recordingEraser satisfies consent.Eraser (handlers.go:30) and proves the
// refusal happens BEFORE any erase is attempted.
type recordingEraser struct{ calls int }

func (e *recordingEraser) ErasePerson(context.Context, ids.UUID, string) error {
	e.calls++
	return nil
}
