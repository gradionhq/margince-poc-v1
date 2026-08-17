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

// What these prove: the ceiling a body rides is decided by its KIND, and a
// multipart route's own cap is reachable.
//
// The bug they exist for shipped silently. Every body rode the 1 MiB JSON
// ceiling, so all three multipart routes — attachments at 25 MiB, the CSV
// import at 10, the LinkedIn export at 8 — had caps that never ran, and each
// went on refusing with a sentence naming a limit nothing enforced. Nothing
// failed: the constants were right, the `MaxBytesReader` calls were right, and
// a handler cannot widen a body the middleware already bounded. Only the
// effective ceiling was wrong, and only a request can see that.

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

func TestLimitBodiesCapsJSONAtTheJSONCeiling(t *testing.T) {
	var read int
	rec := httptest.NewRecorder()
	LimitBodies(readTo(t, &read)).ServeHTTP(rec,
		post("application/json", httperr.MaxBodyBytes+1024))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a JSON body past the 1 MiB ceiling was accepted: status %d", rec.Code)
	}
	if read > httperr.MaxBodyBytes {
		t.Fatalf("read %d bytes past the JSON ceiling of %d", read, httperr.MaxBodyBytes)
	}
}

func TestLimitBodiesLetsAMultipartBodyPastTheJSONCeiling(t *testing.T) {
	// The size that matters: over the JSON ceiling, under the multipart one.
	// This is the case every multipart route was refusing.
	size := httperr.MaxBodyBytes * 4
	var read int
	rec := httptest.NewRecorder()
	LimitBodies(readTo(t, &read)).ServeHTTP(rec,
		post("multipart/form-data; boundary=abc123", size))

	if rec.Code != http.StatusOK {
		t.Fatalf("a %d-byte multipart body was refused: status %d — the "+
			"multipart ceiling is not being applied", size, rec.Code)
	}
	if read != size {
		t.Fatalf("multipart body truncated: handler saw %d of %d bytes", read, size)
	}
}

func TestLimitBodiesStillCapsMultipart(t *testing.T) {
	// Not an exemption. A multipart body is bounded too, one ceiling up.
	var read int
	rec := httptest.NewRecorder()
	LimitBodies(readTo(t, &read)).ServeHTTP(rec,
		post("multipart/form-data; boundary=abc123", httperr.MaxMultipartBodyBytes+1024))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a multipart body past the 25 MiB ceiling was accepted: status %d", rec.Code)
	}
	if int64(read) > httperr.MaxMultipartBodyBytes {
		t.Fatalf("read %d bytes past the multipart ceiling of %d",
			read, httperr.MaxMultipartBodyBytes)
	}
}

func TestMultipartCeilingClearsEveryDeclaredRouteCap(t *testing.T) {
	// The invariant, not the call site: a route may TIGHTEN below the ceiling
	// and may not exceed it, because a cap above the ceiling is a cap that never
	// runs. The numbers are duplicated here on purpose — the point is to fail
	// when one of them moves above the ceiling, which reading it from the
	// constant would hide.
	for _, route := range []struct {
		name string
		cap  int64
	}{
		{"attachment upload (handlers_attachment.go)", 25 << 20},
		{"CSV import source (csvimport.go)", 10 << 20},
		{"LinkedIn export (handlers_linkedin.go)", 8 << 20},
	} {
		if route.cap > httperr.MaxMultipartBodyBytes {
			t.Errorf("%s declares %d bytes, above the %d-byte multipart ceiling — "+
				"that cap can never run, and its refusal names a limit nothing enforces",
				route.name, route.cap, httperr.MaxMultipartBodyBytes)
		}
	}
}

func TestBodyCeilingReadsTheMediaTypeNotTheWholeHeader(t *testing.T) {
	// A real multipart header carries its boundary after the media type, and the
	// casing is the sender's choice. Both have to land on the multipart ceiling
	// or the fix only works for a header nobody sends.
	for _, header := range []string{
		"multipart/form-data; boundary=----WebKitFormBoundary7MA4YWxkTrZu0gW",
		"Multipart/Form-Data; boundary=abc",
		"multipart/form-data",
	} {
		if got := bodyCeiling(post(header, 0)); got != httperr.MaxMultipartBodyBytes {
			t.Errorf("Content-Type %q rode ceiling %d, want the multipart ceiling %d",
				header, got, httperr.MaxMultipartBodyBytes)
		}
	}
	// Everything else is JSON-shaped as far as this bound is concerned, including
	// an absent header and a type that merely mentions multipart later.
	for _, header := range []string{
		"", "application/json", "text/plain",
		"application/x-www-form-urlencoded",
		"application/json; note=multipart/form-data",
	} {
		if got := bodyCeiling(post(header, 0)); got != httperr.MaxBodyBytes {
			t.Errorf("Content-Type %q rode ceiling %d, want the JSON ceiling %d",
				header, got, httperr.MaxBodyBytes)
		}
	}
}
