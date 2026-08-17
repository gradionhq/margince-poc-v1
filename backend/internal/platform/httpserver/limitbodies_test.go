// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httpserver

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// What these prove: the middleware applies the ceiling its caller answers, and
// answers the tight one whenever nobody said otherwise.
//
// Only a request can observe an effective ceiling. A handler's own
// MaxBytesReader cannot widen a body this middleware already bounded, so a
// declared cap above its ceiling is a cap that never runs — and nothing in the
// tree can see that, because the constant, the wrap and the message are all
// individually correct.

// readTo is a handler that drains the body and reports how far it got, which is
// the only place the effective ceiling is observable.
func readTo(t *testing.T, got *int) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		*got = len(body)
		if err != nil {
			// The cap trips as a read error, which is exactly why the chassis uses
			// MaxBytesReader rather than a bare LimitReader: a truncated body that
			// parsed would be silent corruption.
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func post(contentType string, size int) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/anything",
		bytes.NewReader(bytes.Repeat([]byte("x"), size)))
	req.Header.Set("Content-Type", contentType)
	return req
}

// wide is a BodyCeiling that grants the multipart bound to everything, standing
// in for a composition that declared this route an upload route.
func wide(*http.Request) int64 { return httperr.MaxMultipartBodyBytes }

func TestLimitBodiesCapsJSONAtTheJSONCeiling(t *testing.T) {
	var read int
	rec := httptest.NewRecorder()
	LimitBodies(nil, readTo(t, &read)).ServeHTTP(rec,
		post("application/json", httperr.MaxBodyBytes+1024))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a JSON body past the 1 MiB ceiling was accepted: status %d", rec.Code)
	}
	if read > httperr.MaxBodyBytes {
		t.Fatalf("read %d bytes past the JSON ceiling of %d", read, httperr.MaxBodyBytes)
	}
}

func TestLimitBodiesGrantsTheCeilingItsCallerAnswers(t *testing.T) {
	// The size that matters: over the JSON ceiling, under the multipart one.
	// This is the case every upload route was refusing.
	size := httperr.MaxBodyBytes * 4
	var read int
	rec := httptest.NewRecorder()
	LimitBodies(wide, readTo(t, &read)).ServeHTTP(rec,
		post("multipart/form-data; boundary=abc123", size))

	if rec.Code != http.StatusOK {
		t.Fatalf("a %d-byte body was refused: status %d — the ceiling the "+
			"caller answered is not being applied", size, rec.Code)
	}
	if read != size {
		t.Fatalf("multipart body truncated: handler saw %d of %d bytes", read, size)
	}
}

func TestLimitBodiesStillCapsAWidenedRoute(t *testing.T) {
	// Not an exemption. A widened body is bounded too, one ceiling up.
	var read int
	rec := httptest.NewRecorder()
	LimitBodies(wide, readTo(t, &read)).ServeHTTP(rec,
		post("multipart/form-data; boundary=abc123", httperr.MaxMultipartBodyBytes+1024))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a multipart body past the 25 MiB ceiling was accepted: status %d", rec.Code)
	}
	if int64(read) > httperr.MaxMultipartBodyBytes {
		t.Fatalf("read %d bytes past the multipart ceiling of %d",
			read, httperr.MaxMultipartBodyBytes)
	}
}

func TestAMountThatDeclaredNoCeilingGetsTheTightOne(t *testing.T) {
	// A nil BodyCeiling is the safe reading, not an open door: a mount that has
	// not thought about which of its routes carry files has none that do.
	var read int
	rec := httptest.NewRecorder()
	LimitBodies(nil, readTo(t, &read)).ServeHTTP(rec,
		post("multipart/form-data; boundary=abc123", httperr.MaxBodyBytes+1024))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an undeclared mount widened on the sender's word: status %d", rec.Code)
	}
}
