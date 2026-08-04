// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The buffered response the split gate replays through. What matters here is
// the shape of what reaches the wire: a client of this surface parses RFC 7807
// on every refusal, so one path out of the handler answering something else is
// a client that cannot read its own error.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// bufferedFor builds a buffered response already holding a status and headers,
// as it would after the wrapped handler answered.
func bufferedFor(status string) *bufferedResponse {
	b := newBufferedResponse()
	b.header.Set("Content-Type", "application/json")
	b.header.Set("X-Recorded", status)
	return b
}

func TestFlushJSONReplacesTheBodyAndItsContentLength(t *testing.T) {
	b := bufferedFor("ok")
	b.WriteHeader(http.StatusOK)
	if _, err := b.Write([]byte(`{"id":"1"}`)); err != nil {
		t.Fatalf("buffering the original body: %v", err)
	}
	rec := httptest.NewRecorder()

	b.flushJSON(rec, httptest.NewRequest(http.MethodPatch, "/v1/deals/1", nil),
		map[string]any{"id": "1", "staged_approval": map[string]any{"approval_id": "a1"}})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want the buffered 200", rec.Code)
	}
	if got := rec.Header().Get("X-Recorded"); got != "ok" {
		t.Errorf("the buffered headers were dropped: X-Recorded = %q", got)
	}
	body := rec.Body.String()
	// The Content-Length of the ORIGINAL body no longer applies, and a stale
	// one truncates the record for every client that honours it.
	if got, want := rec.Header().Get("Content-Length"), len(body); got != "" && got != itoa(want) {
		t.Errorf("Content-Length = %q, want %d — the length of what was actually written", got, want)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("the replayed body is not JSON: %v (%s)", err, body)
	}
	if _, staged := decoded["staged_approval"]; !staged {
		t.Errorf("the staging note was not spliced into the replayed record: %s", body)
	}
}

// unmarshalable is a payload value encoding/json refuses. A map decoded from
// JSON cannot hold one in production — which is exactly why the branch needs a
// test: it is unreachable by the handler and would otherwise never run.
type unmarshalable struct{}

func (unmarshalable) MarshalJSON() ([]byte, error) { return nil, errNotEncodable }

var errNotEncodable = &json.UnsupportedValueError{Str: "deliberately unencodable"}

// TestFlushJSONAnswersAMarshalFailureAsAProblemDocument — the staging already
// exists at this point, so the client must be told the request failed rather
// than handed a truncated record. Through httperr like every other refusal on
// this surface: a text/plain body here is one a client parsing the contract's
// error shape cannot read, on the path it needs most.
func TestFlushJSONAnswersAMarshalFailureAsAProblemDocument(t *testing.T) {
	b := bufferedFor("ok")
	b.WriteHeader(http.StatusOK)
	rec := httptest.NewRecorder()

	b.flushJSON(rec, httptest.NewRequest(http.MethodPatch, "/v1/deals/1", nil),
		map[string]any{"boom": unmarshalable{}})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — the staging exists and the record could not be built", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "problem+json") {
		t.Errorf("Content-Type = %q, want an RFC 7807 problem document", ct)
	}
	// The marshal error names Go internals; it belongs in the server log.
	if strings.Contains(rec.Body.String(), "unencodable") {
		t.Errorf("the marshal error leaked to the client: %s", rec.Body.String())
	}
}

// itoa keeps the length comparison above readable without pulling strconv into
// a file that needs it once.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
