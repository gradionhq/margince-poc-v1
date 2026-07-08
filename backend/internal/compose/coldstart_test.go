// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
