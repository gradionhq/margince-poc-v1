// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httperr

// The decode boundary, driven through the real contract structs a handler
// decodes into — the only way to reproduce what encoding/json says about
// `uuid.UUID`, `openapi_types.Date` and a generated request type.
//
// Two claims per case: the caller is told which input to fix, and the sentence
// carries nothing of the program that read it.

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// goInternals is the vocabulary of OUR program: Go's own error phrasing, the
// generated package's name, the types a caller never declared, and the
// reference layout `2006-01-02` — which is the worst of them, because a caller
// who reads it as an example sends a year that is not theirs.
var goInternals = []string{
	"Go struct", "Go value", "crmcontracts.", "openapi_types", "uuid.UUID",
	"time.Time", "int64", "2006", "github.com/", "json:", "encoding/json",
}

func assertNoInternals(t *testing.T, detail string) {
	t.Helper()
	for _, leak := range goInternals {
		if strings.Contains(detail, leak) {
			t.Errorf("the refusal carries %q, which describes this program rather than the request: %q", leak, detail)
		}
	}
}

// decodeBody runs one body through the real Decode path and returns the rendered
// problem detail, so every assertion below reads what a client reads.
func decodeBody(t *testing.T, body string, into func(w http.ResponseWriter, r *http.Request) bool) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/things", strings.NewReader(body))
	if into(rec, req) {
		t.Fatalf("body %q was accepted", body)
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decoding problem body %q: %v", rec.Body.String(), err)
	}
	return rec.Code, problem.Detail
}

// The four request shapes below are declared once and shared by every case: a
// decode target is a contract struct, and the table is about the SENTENCE each
// one produces rather than about which struct produced it.
var (
	intoAdvanceDeal = func(w http.ResponseWriter, r *http.Request) bool {
		var req crmcontracts.AdvanceDealRequest
		return Decode(w, r, &req)
	}
	intoCreateDeal = func(w http.ResponseWriter, r *http.Request) bool {
		var req crmcontracts.CreateDealRequest
		return Decode(w, r, &req)
	}
	intoCreateActivity = func(w http.ResponseWriter, r *http.Request) bool {
		var req crmcontracts.CreateActivityRequest
		return Decode(w, r, &req)
	}
	// A hand-written transport shape (activities' relink body) carries ids.UUID,
	// whose refusal is one WE wrote.
	intoRelink = func(w http.ResponseWriter, r *http.Request) bool {
		var req struct {
			EntityID ids.UUID `json:"entity_id"`
		}
		return Decode(w, r, &req)
	}
)

// decodeRefusalCase is one body and the sentence it must answer with.
type decodeRefusalCase struct {
	name, body, wantDetail string
	decode                 func(w http.ResponseWriter, r *http.Request) bool
}

// decodeRefusalCases is every decode failure a contract struct can produce. It
// is one table rather than several because the claim is one claim, made over
// the whole space of shapes: the sentence names the input and never the program.
var decodeRefusalCases = []decodeRefusalCase{
	{
		name:       "a number where a uuid belongs names the wire field and the shape",
		body:       `{"to_stage_id":5}`,
		decode:     intoAdvanceDeal,
		wantDetail: "`to_stage_id` must be a UUID string, not a number",
	},
	{
		name:       "a body that is not an object says so without naming the struct",
		body:       `[1,2]`,
		decode:     intoAdvanceDeal,
		wantDetail: "the payload must be a JSON object, not an array",
	},
	{
		name:       "a uuid the library refuses with no exported type falls back to the generic sentence",
		body:       `{"to_stage_id":"abcdef"}`,
		decode:     intoAdvanceDeal,
		wantDetail: genericDecodeDetail,
	},
	{
		name:       "a timestamp names RFC 3339, never the layout that describes it",
		body:       `{"occurred_at":"tomorrow"}`,
		decode:     intoCreateActivity,
		wantDetail: `"tomorrow" is not an RFC 3339 timestamp`,
	},
	{
		name:       "a date names the calendar format, never the layout",
		body:       `{"expected_close_date":"tomorrow"}`,
		decode:     intoCreateDeal,
		wantDetail: `"tomorrow" is not a date in YYYY-MM-DD form`,
	},
	{
		name:       "a nested path is quoted as the caller spelled it",
		body:       `{"links":[{"entity_id":5,"entity_type":"deal"}]}`,
		decode:     intoCreateActivity,
		wantDetail: "`links.entity_id` must be a UUID string, not a number",
	},
	{
		name:       "a value whose own unmarshaller ran states the shape without claiming a field",
		body:       `{"expected_close_date":5}`,
		decode:     intoCreateDeal,
		wantDetail: "a value in the payload must be a string, not a number",
	},
	{
		name:       "an enum names the wire shape, never the generated type",
		body:       `{"kind":5}`,
		decode:     intoCreateActivity,
		wantDetail: "`kind` must be a string, not a number",
	},
	{
		name:       "an integer field names an integer, not its width",
		body:       `{"amount_minor":"x"}`,
		decode:     intoCreateDeal,
		wantDetail: "a value in the payload must be an integer, not a string",
	},
	{
		name:       "malformed JSON carries the offset and nothing else",
		body:       `{"a":}`,
		decode:     intoAdvanceDeal,
		wantDetail: "the payload is not valid JSON at byte 6",
	},
	{
		name:       "a truncated body says it is incomplete",
		body:       `{`,
		decode:     intoAdvanceDeal,
		wantDetail: "the payload ends before its JSON value is complete",
	},
	{
		name:       "an empty body says it is empty",
		body:       ``,
		decode:     intoAdvanceDeal,
		wantDetail: "the payload is empty",
	},
	{
		name:       "our own value refusal reaches the caller as written",
		body:       `{"entity_id":"nope"}`,
		decode:     intoRelink,
		wantDetail: `"nope" is not a canonical UUID (expected the 8-4-4-4-12 hex form)`,
	},
}

func TestDecode_refusalNamesTheInputAndNeverTheProgram(t *testing.T) {
	for _, tc := range decodeRefusalCases {
		t.Run(tc.name, func(t *testing.T) {
			status, detail := decodeBody(t, tc.body, tc.decode)
			if status != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422 — a body the caller got wrong is never a server fault", status)
			}
			if !strings.Contains(detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to say %q", detail, tc.wantDetail)
			}
			assertNoInternals(t, detail)
		})
	}
}

// The exact sentences the surface used to answer with. Pinned verbatim because
// each one was reachable from a real request, and a substring sweep over the
// vocabulary alone would pass on a paraphrase that still leaked one of them.
func TestDecode_neverAnswersWithADecoderSentence(t *testing.T) {
	for _, was := range []string{
		"json: cannot unmarshal number into Go struct field AdvanceDealRequest.to_stage_id of type uuid.UUID",
		"json: cannot unmarshal array into Go value of type crmcontracts.AdvanceDealRequest",
		"invalid UUID length: 6",
		`parsing time "tomorrow" as "2006-01-02": cannot parse "tomorrow" as "2006"`,
	} {
		for _, body := range []string{
			`{"to_stage_id":5}`, `[1,2]`, `{"to_stage_id":"abcdef"}`,
		} {
			_, detail := decodeBody(t, body, intoAdvanceDeal)
			if strings.Contains(detail, was) {
				t.Errorf("body %s still answers %q", body, was)
			}
		}
		_, detail := decodeBody(t, `{"expected_close_date":"tomorrow"}`, intoCreateDeal)
		if strings.Contains(detail, was) {
			t.Errorf("a malformed date still answers %q", was)
		}
	}
}

// Withholding a message is not the same as losing it: the shape nothing could
// name is the one an operator most needs to see, because it is the one that
// says a decode failure exists that this file does not yet translate.
func TestDecode_theUnnamedShapeStillReachesTheLog(t *testing.T) {
	var logged bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	_, detail := decodeBody(t, `{"to_stage_id":"abcdef"}`, intoAdvanceDeal)
	if detail != genericDecodeDetail {
		t.Fatalf("detail = %q, want the generic sentence — this case is the masked one", detail)
	}
	if !strings.Contains(logged.String(), "invalid UUID length") {
		t.Errorf("the withheld cause is in no log line: %q", logged.String())
	}
}

// The provider seam reaches the SAME contract structs from the tool surface, so
// the decoder text has a second route to a client. That seam wraps its own
// unknown-key refusal and the decoder's failure in one type: the first is ours
// and must survive, the second must be restated.
func TestClassify_seamFieldDecodeIsRestatedButNeverOverwritten(t *testing.T) {
	for _, tc := range []struct {
		name, fields, wantDetail string
	}{
		{
			name:       "a decoder type error is restated",
			fields:     `{"assignee_id":5}`,
			wantDetail: "`assignee_id` must be a UUID string, not a number",
		},
		{
			name:       "the seam's own key refusal quotes the caller's key",
			fields:     `{"subjekt":"x"}`,
			wantDetail: `unknown field "subjekt"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req crmcontracts.CreateActivityRequest
			err := datasource.StrictDecode(json.RawMessage(tc.fields), &req)
			if err == nil {
				t.Fatalf("fields %s were accepted", tc.fields)
			}
			status, body := writeAndDecode(t, err)
			if status != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422", status)
			}
			detail, _ := body["detail"].(string)
			if !strings.Contains(detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to say %q", detail, tc.wantDetail)
			}
			assertNoInternals(t, detail)
		})
	}
}
