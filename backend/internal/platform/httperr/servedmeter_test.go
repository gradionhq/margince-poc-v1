// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httperr

// What a response body is worth to the read bound, and what a meter that cannot
// charge for it does to the answer.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type page struct {
	Data []string `json:"data"`
	Next string   `json:"next"`
}

type record struct {
	ID string `json:"id"`
}

// notAPage carries a Data field that is NOT a page, which is the case the
// count rule has to get right rather than trust the field name.
type notAPage struct {
	Data string `json:"data"`
}

func TestRecordsInCountsThePageNotTheResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		body any
		want int
	}{
		{"a page counts its rows", page{Data: []string{"a", "b", "c"}}, 3},
		{"an empty page hands over nothing", page{Data: []string{}}, 0},
		{"a nil page hands over nothing", page{}, 0},
		{"a pointer to a page is still a page", &page{Data: []string{"a"}}, 1},
		{"a single record is one", record{ID: "r"}, 1},
		{"a pointer to a single record is one", &record{ID: "r"}, 1},
		{"a bare slice counts its members", []record{{ID: "a"}, {ID: "b"}}, 2},
		{"a nil body hands over nothing", nil, 0},
		{"a typed nil pointer hands over nothing", (*record)(nil), 0},
		{"a Data field that is not a page is one record", notAPage{Data: "scalar"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := recordsIn(tc.body); got != tc.want {
				t.Errorf("recordsIn(%#v) = %d, want %d", tc.body, got, tc.want)
			}
		})
	}
}

// meterStub records what it was told and answers whether the write proceeds.
type meterStub struct {
	http.ResponseWriter
	noted   int
	calls   int
	proceed bool
}

func (m *meterStub) NoteServed(n int) bool {
	m.calls++
	m.noted += n
	return m.proceed
}

func TestWriteJSONReportsWhatItIsAboutToServe(t *testing.T) {
	rec := httptest.NewRecorder()
	meter := &meterStub{ResponseWriter: rec, proceed: true}

	WriteJSON(meter, http.StatusOK, page{Data: []string{"a", "b"}})

	if meter.calls != 1 {
		t.Errorf("the meter was consulted %d times, want exactly 1 per response", meter.calls)
	}
	if meter.noted != 2 {
		t.Errorf("the meter was told %d records, want 2", meter.noted)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status %d, want 200 — a proceeding meter must not change the answer", rec.Code)
	}
	var got page
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding the served body: %v", err)
	}
	if len(got.Data) != 2 {
		t.Errorf("served %d rows, want 2", len(got.Data))
	}
}

// A meter that refuses has already answered the request; WriteJSON must add
// nothing to it. Writing the body anyway would hand over the very records the
// surface just said it could not account for.
func TestWriteJSONWritesNothingWhenTheMeterRefuses(t *testing.T) {
	rec := httptest.NewRecorder()
	meter := &meterStub{ResponseWriter: rec, proceed: false}

	WriteJSON(meter, http.StatusOK, page{Data: []string{"a", "b"}})

	if body := rec.Body.String(); body != "" {
		t.Errorf("a refused response carried a body %q; the records were handed over anyway", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "" {
		t.Errorf("a refused response set Content-Type %q, overwriting whatever the refusal wrote", ct)
	}
}

// An unmetered writer is every human request and every composition with no
// volume meter. It must take exactly the path it always did.
func TestWriteJSONServesAnUnmeteredWriterUnchanged(t *testing.T) {
	rec := httptest.NewRecorder()

	WriteJSON(rec, http.StatusCreated, record{ID: "r"})

	if rec.Code != http.StatusCreated {
		t.Errorf("status %d, want 201", rec.Code)
	}
	var got record
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding the served body: %v", err)
	}
	if got.ID != "r" {
		t.Errorf("served %+v, want the record unchanged", got)
	}
}

// recordsIn must not panic on a body no handler would send but a future one
// might, because a panic here takes down a response that was about to succeed.
func TestRecordsInSurvivesUnexpectedBodies(t *testing.T) {
	for _, body := range []any{
		map[string]int{"a": 1},
		"a bare string",
		42,
		errors.New("an error as a body"),
	} {
		if got := recordsIn(body); got != 1 {
			t.Errorf("recordsIn(%#v) = %d, want 1 — an unrecognized body is one thing handed over", body, got)
		}
	}
}
