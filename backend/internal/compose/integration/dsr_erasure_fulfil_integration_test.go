// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Fulfilling an erasure DSR against the REAL privacy.Eraser — exactly the
// composition compose/server.go wires in production (consent.Handlers.WithEraser
// over privacy.NewEraser) — rather than the recordingEraser fake the rest of
// modules/consent's own DSR suite drives. ErasePerson anonymizes a person row IN
// PLACE and never deletes it, so both cases here turn on a fact only the real
// eraser can produce: a genuine ErrNotFound for a subject_ref naming nobody, and a
// genuine nil on a harmless repeat scrub of an already-erased row. A fake can only
// return what a test tells it to, so it cannot stand in for either.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// setupErasureDSR seeds one open erasure DSR naming subjectRef against e's
// pool/workspace, and returns the handler set wired to the real erase path plus
// the store an assertion can read the request back through.
func setupErasureDSR(t *testing.T, e *Env, subjectRef string) (consent.Handlers, *consent.Store, ids.UUID) {
	t.Helper()
	store := consent.NewStore(e.Pool)
	created, err := store.CreateDSR(e.Admin(), consent.CreateDSRInput{
		Kind: "erasure", SubjectRef: subjectRef, DueAt: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("creating erasure DSR: %v", err)
	}
	h := consent.NewHandlers(e.Pool).WithEraser(privacy.NewEraser(e.Pool))
	return h, store, created.ID
}

func fulfilErasureDSR(t *testing.T, e *Env, h consent.Handlers, id ids.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPatch, "/v1/data-subject-requests/"+id.String(),
		strings.NewReader(body)).WithContext(e.Admin())
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.UpdateDataSubjectRequest(w, r, openapi_types.UUID(id))
	return w
}

// dsrValidationField decodes an httperr.Validation problem+json body and
// returns the failing field, so a test can assert WHICH guard fired instead of
// merely that the status code was 422.
func dsrValidationField(t *testing.T, body []byte) string {
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

// TestFulfillErasureHTTPRefusesASyntacticallyValidButNonexistentSubject drives
// the real privacy.Eraser (not a fake that always succeeds): ids.Parse proves
// syntax only, so a well-formed UUID that names no person in this workspace must
// be refused exactly like a subject_ref that never parsed at all, on the genuine
// "SELECT ... WHERE id = $1 found no row" ErrNotFound the real eraser returns —
// and the request must stay open, never certify a deletion that never ran.
func TestFulfillErasureHTTPRefusesASyntacticallyValidButNonexistentSubject(t *testing.T) {
	e := Setup(t)
	h, store, id := setupErasureDSR(t, e, ids.NewV7().String())

	w := fulfilErasureDSR(t, e, h, id, `{"status":"fulfilled","resolution":"verified by phone"}`)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a well-formed but nonexistent subject must be refused 422, got %d: %s", w.Code, w.Body)
	}
	if field := dsrValidationField(t, w.Body.Bytes()); field != "subject_ref" {
		t.Fatalf("want the subject_ref field to fail, got %q: %s", field, w.Body)
	}
	after, err := store.GetDSR(e.Admin(), id)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if after.Status != "open" {
		t.Fatalf("a refused fulfilment must not move the request: status=%q", after.Status)
	}
}

// TestFulfillErasureHTTPIsIdempotentAcrossTwoFulfilments is the load-bearing
// proof for the premise the ErrNotFound-refusal path rests on: ErasePerson
// anonymizes a person row IN PLACE rather than deleting it, so fulfilling the SAME
// erasure request a second time must keep succeeding — the second call finds the
// (now-anonymized) row exactly like the first did, re-runs the scrub harmlessly,
// and returns nil, never ErrNotFound. Only the real eraser can prove this; a fake
// would succeed unconditionally either way. If this test fails, the
// anonymize-in-place premise is wrong and the handler's ErrNotFound refusal is
// blocking a re-fulfil idempotency actually requires to succeed.
func TestFulfillErasureHTTPIsIdempotentAcrossTwoFulfilments(t *testing.T) {
	e := Setup(t)
	personID := e.SeedPerson(t, "Erasure Subject", nil)
	h, _, id := setupErasureDSR(t, e, personID.String())

	first := fulfilErasureDSR(t, e, h, id, `{"status":"fulfilled","resolution":"verified in person"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first fulfilment of a real person must succeed, got %d: %s", first.Code, first.Body)
	}

	// The request is already "fulfilled"; validateDSRUpdate treats a status
	// equal to the current one as a no-op (not a transition), so this is legal
	// to submit again — and it re-triggers the erase side effect.
	second := fulfilErasureDSR(t, e, h, id, `{"status":"fulfilled"}`)
	if second.Code != http.StatusOK {
		t.Fatalf("re-fulfilling an already-erased person must still succeed (idempotent), got %d: %s",
			second.Code, second.Body)
	}
}

// gatedEraser wraps the real eraser so the test can act WHILE a scrub is in
// flight: it signals entry, blocks until released, then delegates. ErasePerson
// runs only after FulfilErasure has already taken the request's FOR UPDATE
// lock, so "inside the gate" is exactly the window the lock must span.
type gatedEraser struct {
	inner   consent.Eraser
	entered chan struct{}
	release chan struct{}
}

func (g *gatedEraser) ErasePerson(ctx context.Context, personID ids.UUID, reason string) error {
	close(g.entered)
	<-g.release
	return g.inner.ErasePerson(ctx, personID, reason)
}

// errLockHeld marks the one expected failure of the probe below — the row is
// locked by the in-flight fulfilment (55P03). Returning it (rather than nil)
// keeps WithWorkspaceTx on its rollback path: a FOR UPDATE NOWAIT that trips
// 55P03 leaves the transaction aborted, and committing an aborted tx would
// surface pgx.ErrTxCommitRollback instead of the clean signal we want.
var errLockHeld = errors.New("request row lock held")

// dsrRowLockHeld probes whether the request row is currently locked by another
// transaction, WITHOUT blocking: FOR UPDATE NOWAIT turns a contended lock into
// an immediate 55P03 rather than a wait, so the assertion is deterministic (no
// sleep, no racing timeout). It runs through database.WithWorkspaceTx like every
// other tenant query — binding app.workspace_id the one sanctioned way, never a
// hand-rolled GUC over a raw pool connection (the row is FORCE-RLS; the app role
// sees zero rows unbound).
func dsrRowLockHeld(t *testing.T, e *Env, id ids.UUID) bool {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		var got ids.UUID
		scanErr := tx.QueryRow(context.Background(),
			`SELECT id FROM data_subject_request WHERE id = $1 FOR UPDATE NOWAIT`, id).Scan(&got)
		if scanErr == nil {
			return nil // the lock was free — nobody is holding it
		}
		var pgErr *pgconn.PgError
		if errors.As(scanErr, &pgErr) && pgErr.Code == "55P03" {
			return errLockHeld
		}
		return scanErr
	})
	if err == nil {
		return false
	}
	if errors.Is(err, errLockHeld) {
		return true
	}
	t.Fatalf("probing the request lock: %v", err)
	return false
}

// TestFulfillErasureHoldsTheRequestLockedAcrossTheErase pins the concurrency
// guarantee the whole FulfilErasure shape exists for: the request's FOR UPDATE
// lock is held for the ENTIRE erase, not dropped between "this fulfil is legal"
// and the scrub. Without it, a second officer could reject or re-fulfil the
// same request in that window — leaving a subject erased on a request the queue
// still shows open or rejected. The gate freezes execution mid-scrub so the
// probe observes the lock while it must be held; a bare fulfil would complete
// too fast to catch the window at all.
func TestFulfillErasureHoldsTheRequestLockedAcrossTheErase(t *testing.T) {
	e := Setup(t)
	personID := e.SeedPerson(t, "Locked Subject", nil)

	store := consent.NewStore(e.Pool)
	created, err := store.CreateDSR(e.Admin(), consent.CreateDSRInput{
		Kind: "erasure", SubjectRef: personID.String(), DueAt: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("creating erasure DSR: %v", err)
	}
	gate := &gatedEraser{
		inner:   privacy.NewEraser(e.Pool),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	h := consent.NewHandlers(e.Pool).WithEraser(gate)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- fulfilErasureDSR(t, e, h, created.ID, `{"status":"fulfilled","resolution":"verified"}`)
	}()

	<-gate.entered // the scrub is running ⇒ the request lock must be held now
	if !dsrRowLockHeld(t, e, created.ID) {
		t.Fatal("the request row must stay locked for the whole erase; a concurrent lock succeeded mid-scrub")
	}
	close(gate.release) // let the scrub commit and the fulfil finalize

	w := <-done
	if w.Code != http.StatusOK {
		t.Fatalf("the fulfilment must succeed once the erase completes, got %d: %s", w.Code, w.Body)
	}
	// Once the lock releases the probe finds it free again, and the request has
	// actually reached fulfilled — the lock was held across the scrub, not left
	// dangling.
	if dsrRowLockHeld(t, e, created.ID) {
		t.Fatal("the request lock must release once the fulfilment commits")
	}
	after, err := store.GetDSR(e.Admin(), created.ID)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if after.Status != "fulfilled" {
		t.Fatalf("the request must end fulfilled, got %q", after.Status)
	}
}
