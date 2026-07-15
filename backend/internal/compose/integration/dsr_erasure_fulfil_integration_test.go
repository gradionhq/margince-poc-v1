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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
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
