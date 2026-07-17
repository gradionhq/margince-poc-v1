// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func TestStripTagsSurvivesUnicodeCaseFolding(t *testing.T) {
	// U+212A (KELVIN SIGN) lowercases to a 1-byte "k": an index into a
	// lowered copy of the document would drift off the source bytes and
	// slice out of range. The stripper must work on the original bytes.
	kelvin := strings.Repeat("\u212a", 3)
	got := stripTags(kelvin + "<p>hello</p><script>evil()</script> world")
	if !strings.HasSuffix(got, "hello world") {
		t.Fatalf("stripTags = %q", got)
	}
	if stripTags("<SCRIPT>x</SCRIPT>visible<STYLE>y</STYLE>") != "visible" {
		t.Fatal("case-insensitive script/style stripping broke")
	}
}

// The contract's oneOf: EXACTLY ONE of url|text|self_description. Zero or
// several inputs are a 422 whose details carry how many were populated —
// before any fetch, model call or staging happens.
func TestColdStartReadbackRequiresExactlyOneInput(t *testing.T) {
	handler := coldstartHandlers{engine: &coldStartEngine{}}

	cases := []struct {
		name      string
		body      string
		populated int
	}{
		{"no input", `{}`, 0},
		{"two inputs", `{"url":"https://acme.example","text":"Acme builds CRMs."}`, 2},
		{"all three", `{"url":"https://acme.example","text":"t","self_description":"s"}`, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/coldstart", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			handler.ColdStartReadback(rec, req)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rec.Code)
			}
			var problem struct {
				Code    string         `json:"code"`
				Details map[string]int `json:"details"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatalf("response is not the problem shape: %v", err)
			}
			if problem.Code != "validation_error" || problem.Details["populated_fields"] != tc.populated {
				t.Fatalf("problem = %+v, want validation_error with populated_fields=%d", problem, tc.populated)
			}
		})
	}
}

// The preview transport shares the staging path's request contract EXACTLY —
// same oneOf, same 422 — because the only thing that differs between them is
// what happens to the read-back, never what may be asked for.
func TestColdStartPreviewRequiresExactlyOneInput(t *testing.T) {
	handler := coldstartHandlers{engine: &coldStartEngine{}}

	cases := []struct {
		name      string
		body      string
		populated int
	}{
		{"no input", `{}`, 0},
		{"two inputs", `{"text":"Acme builds CRMs.","self_description":"we sell CRMs"}`, 2},
		{"all three", `{"url":"https://acme.example","text":"t","self_description":"s"}`, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ColdStartPreview(rec, httptest.NewRequest(http.MethodPost, "/v1/coldstart/preview", strings.NewReader(tc.body)))

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rec.Code)
			}
			var problem struct {
				Code    string         `json:"code"`
				Details map[string]int `json:"details"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatalf("response is not the problem shape: %v", err)
			}
			if problem.Code != "validation_error" || problem.Details["populated_fields"] != tc.populated {
				t.Fatalf("problem = %+v, want validation_error with populated_fields=%d", problem, tc.populated)
			}
		})
	}
}

// The single input must also be WELL-FORMED — refused before any fetch, model
// call or SSRF-relevant dial happens.
func TestColdStartPreviewRefusesAMalformedInput(t *testing.T) {
	handler := coldstartHandlers{engine: &coldStartEngine{}}

	cases := []struct{ name, body, field string }{
		{"relative url", `{"url":"/about"}`, "url"},
		{"non-http scheme", `{"url":"file:///etc/passwd"}`, "url"},
		{"blank text", `{"text":"   "}`, "text"},
		{"blank self description", `{"self_description":"\t\n"}`, "self_description"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ColdStartPreview(rec, httptest.NewRequest(http.MethodPost, "/v1/coldstart/preview", strings.NewReader(tc.body)))

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rec.Code)
			}
			var problem struct {
				Code    string `json:"code"`
				Details struct {
					Errors []struct {
						Field string `json:"field"`
					} `json:"errors"`
				} `json:"details"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatalf("response is not the problem shape: %v", err)
			}
			if problem.Code != "validation_error" ||
				len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != tc.field {
				t.Fatalf("problem = %+v, want validation_error naming %s", problem, tc.field)
			}
		})
	}
}

// A process role that declared no model path (--routing) answers an explicit
// 501 — never a silent guess, and never a read-back with no brain behind it.
// The refusal precedes decoding: an unwired engine cannot be talked into work
// by a well-formed body.
func TestColdStartPreviewIsNotImplementedWithoutAModelPath(t *testing.T) {
	handler := coldstartHandlers{}

	rec := httptest.NewRecorder()
	handler.ColdStartPreview(rec, httptest.NewRequest(http.MethodPost, "/v1/coldstart/preview",
		strings.NewReader(`{"url":"https://acme.example"}`)))

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 when no model path is configured", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "coldStartPreview") {
		t.Fatalf("the 501 does not name the operation: %s", rec.Body.String())
	}
}

// stubPage answers every fetch with itself — the model seam under test here is
// the gate's verdict, not the network.
type stubPage string

func (p stubPage) Fetch(context.Context, string) (string, error) { return string(p), nil }

// A read-back that can quote nothing is refused HONESTLY: the transport maps
// the unreadable verdict onto the contract's coldstart_unreadable 422, whose
// detail says what to try next in the terms of whatever the user supplied —
// and never leaks the real cause (the model's output, the fetch) to the client.
func TestColdStartPreviewAnswersTheHonest422WhenNothingCanBeQuoted(t *testing.T) {
	const page = "Acme GmbH — Onboard your team in minutes, not weeks. Built for RevOps leaders at scaling SaaS companies."
	// Every field the model offers cites something the source never says, so
	// the no-guess gate drops them all.
	hallucinating := func() *coldStartEngine {
		return &coldStartEngine{extract: evidenceExtractor{
			fetch: stubPage(page),
			brain: ai.NewFakeClient().Script(
				`{"fields":[{"field":"icp","value":"guessed","evidence_snippet":"a claim the source never makes","confidence":0.9}]}`),
		}}
	}

	cases := []struct{ name, body, wantDetail string }{
		{"url", `{"url":"https://acme.example"}`, "Couldn't read enough from this page. Retry or paste text."},
		{"text", `{"text":"` + page + `"}`, "Couldn't ground any company fact in this text. Paste more of the page."},
		{"self description", `{"self_description":"` + page + `"}`, "Couldn't ground any field in this description. Say more about your business."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := coldstartHandlers{engine: hallucinating()}
			rec := httptest.NewRecorder()
			handler.ColdStartPreview(rec, httptest.NewRequest(http.MethodPost, "/v1/coldstart/preview", strings.NewReader(tc.body)))

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rec.Code)
			}
			var problem struct {
				Code   string `json:"code"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatalf("response is not the problem shape: %v", err)
			}
			if problem.Code != "coldstart_unreadable" {
				t.Fatalf("code = %q, want coldstart_unreadable", problem.Code)
			}
			// The advice is phrased for the input the user actually gave — a
			// paste is not told to "retry the page".
			if problem.Detail != tc.wantDetail {
				t.Fatalf("detail = %q, want %q", problem.Detail, tc.wantDetail)
			}
		})
	}
}

// evidence_offset counts CHARS (runes), not bytes: a multibyte prefix must
// not shift the highlight-back position, and an unlocatable snippet is null
// rather than a fabricated position.
func TestRuneOffsetCountsCharsNotBytes(t *testing.T) {
	pasted := "münchener Käserei — we sell cheese"
	got := runeOffset(pasted, "we sell cheese")
	if got == nil || *got != 20 {
		t.Fatalf("runeOffset = %v, want 20 (rune position, not the byte index)", got)
	}
	if runeOffset(pasted, "not in the paste") != nil {
		t.Fatal("an unlocatable snippet must yield nil, never a guessed offset")
	}
}
