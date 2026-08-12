// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A bodyless extension operation's arguments come from the query string, and
// this file is where that decode is held to its declaration.
//
// It is tested this closely because it is the ONLY thing checking those
// arguments. Nothing downstream validates a tool's input against its declared
// schema — this codebase carries no jsonschema dependency by choice (see
// automation's catalog for the same decision) — so a value the decode lets
// through reaches a unit's handler as whatever it happened to parse as. A
// tolerant decode here is not a lenient API, it is a handler receiving a string
// where its declaration promised an integer.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// queryVerb is a read-scoped GET declaring one argument of each query-encodable
// type, with `payload` required — the shape notes's signature read has, widened
// to cover every primitive the decode must coerce.
func queryVerb() extension.Verb {
	v := unitVerb("alpha", "sign_payload", extension.TierAutoExecute, extension.ScopeRead)
	v.Method = http.MethodGet
	v.InputSchema = json.RawMessage(`{"type":"object","properties":{` +
		`"payload":{"type":"string"},"limit":{"type":"integer"},` +
		`"ratio":{"type":"number"},"exact":{"type":"boolean"}},` +
		`"required":["payload"],"additionalProperties":false}`)
	return v
}

// echoArgs mounts one verb behind an invoker that seals the arguments it was
// given, so a case can assert the exact JSON that reached the tool. Sealed
// because the real invoker seals: the route unwraps the envelope, and a stub
// answering bare bytes would test a seam shape that does not exist.
func echoArgs(t *testing.T, v extension.Verb) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	verbs := []extension.Verb{v}
	if _, err := MountExtensionRoutes(mux, verbs, allServed(verbs),
		func(_ context.Context, _ string, in json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"schema_version":"1.0.0","trace_id":"019fe351-1f62-749f-ac9f-a89d5a81abfa",` +
				`"freshness":{"authoritative":true},"trust":"t0","evidence":[],"warnings":[],` +
				`"data":` + string(in) + `}`), nil
		}); err != nil {
		t.Fatalf("mounting the query verb: %v", err)
	}
	return mux
}

// TestAQueryArgumentArrivesAsItsDeclaredJsonType: the coercion, which is the
// whole point of the decode. A query value is always text, and the declaration
// is what says what it must become — so this asserts the JSON that reaches the
// tool, not merely that the call succeeded.
func TestAQueryArgumentArrivesAsItsDeclaredJsonType(t *testing.T) {
	mux := echoArgs(t, queryVerb())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v1/ext/alpha/sign-payload?payload=hello&limit=7&ratio=1.5&exact=true", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	// Keys sorted, because the arguments are marshalled from a map: one call's
	// document must not differ from another's by query order alone.
	const want = `{"exact":true,"limit":7,"payload":"hello","ratio":1.5}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("arguments =\n  %s\nwant\n  %s", got, want)
	}
}

// TestAnIntegerIsRemarshalledRatherThanPassedThrough: text that PARSES is not
// necessarily text JSON accepts in that position. "007" and "%2B7" both parse to
// 7, and passing the original through would put a token in the arguments
// document that no JSON reader would accept as a number — a handler unmarshalling
// its own arguments would fail on input this route called valid.
//
// The sign is percent-encoded because a bare `+` in a query string IS a space,
// so `limit=+7` is the value " 7" and correctly refused. That is the decode
// reading the query the way the URL spec defines it, not a special case.
func TestAnIntegerIsRemarshalledRatherThanPassedThrough(t *testing.T) {
	mux := echoArgs(t, queryVerb())
	for _, query := range []string{"limit=007", "limit=%2B7"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/v1/ext/alpha/sign-payload?payload=p&"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body %s)", query, rec.Code, rec.Body)
		}
		if got, want := rec.Body.String(), `{"limit":7,"payload":"p"}`; got != want {
			t.Errorf("%s: arguments = %s, want %s", query, got, want)
		}
	}
}

// TestAnOmittedOptionalArgumentIsAbsentRatherThanNull: an argument the caller did
// not send must not appear at all. Sending it as null would hand the handler a
// value its declaration does not describe — `{"type":"integer"}` does not admit
// null — and "absent" and "explicitly nothing" are different statements.
func TestAnOmittedOptionalArgumentIsAbsentRatherThanNull(t *testing.T) {
	mux := echoArgs(t, queryVerb())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/ext/alpha/sign-payload?payload=only", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if got, want := rec.Body.String(), `{"payload":"only"}`; got != want {
		t.Fatalf("arguments = %s, want %s", got, want)
	}
}

// TestTheQueryDecodeRefusesWhatNothingElseWouldCatch: one case per refusal.
//
// Every row here would otherwise reach a unit's handler, because no schema
// validator stands between this decode and the tool. A refusal naming the
// argument is the answer; the alternative is not a permissive API but a handler
// holding a value of the wrong type, or an argument the caller believes they sent.
//
// 422, not 400: httperr.Validation is the core surface's own vocabulary for a
// well-formed request carrying unusable input, and an extension route answering
// a different status for the same class of fault would be a second convention.
func TestTheQueryDecodeRefusesWhatNothingElseWouldCatch(t *testing.T) {
	for name, tc := range map[string]struct{ query, wantCode string }{
		"an argument this operation does not declare": {"payload=p&nope=1", "unknown_parameter"},
		// Silently dropping it is the failure this prevents: the caller sent a
		// filter, the tool never saw it, and the answer looks like an unfiltered
		// result rather than an error.
		"a misspelled argument name":  {"payload=p&limitt=5", "unknown_parameter"},
		"a repeated argument":         {"payload=a&payload=b", "repeated_parameter"},
		"a required argument missing": {"limit=5", "missing_parameter"},
		"an integer that is not one":  {"payload=p&limit=many", "invalid_type"},
		"an integer given a decimal":  {"payload=p&limit=1.5", "invalid_type"},
		"a number that is not one":    {"payload=p&ratio=lots", "invalid_type"},
		// A bare `+` in a query string is a space per the URL spec, so this is the
		// value " 7" rather than a signed 7 — refused, and the reason it is refused
		// is worth a row so a future edit does not "fix" it by trimming.
		"an integer whose sign was not encoded": {"payload=p&limit=+7", "invalid_type"},
		// The loose boolean spellings, each a convention some client uses and none
		// the contract states. Guessing which was meant is how a flag reads false.
		"a boolean spelled 1":   {"payload=p&exact=1", "invalid_type"},
		"a boolean spelled yes": {"payload=p&exact=yes", "invalid_type"},
		"a boolean cased True":  {"payload=p&exact=True", "invalid_type"},
	} {
		t.Run(name, func(t *testing.T) {
			mux := echoArgs(t, queryVerb())
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/ext/alpha/sign-payload?"+tc.query, nil))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.wantCode) {
				t.Errorf("the refusal does not carry %q: %s", tc.wantCode, rec.Body)
			}
		})
	}
}

// TestABodylessRouteIgnoresARequestBody: the contract says this operation has no
// body, so the seam does not read one — not even to reject it. Reading it would
// make the route's behaviour depend on something no client was told to send, and
// a caller who sent arguments both ways would get one of the two silently.
func TestABodylessRouteIgnoresARequestBody(t *testing.T) {
	mux := echoArgs(t, queryVerb())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/ext/alpha/sign-payload?payload=query",
		strings.NewReader(`{"payload":"body"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	// The QUERY's value, and only it.
	if got, want := rec.Body.String(), `{"payload":"query"}`; got != want {
		t.Fatalf("arguments = %s, want %s", got, want)
	}
}

// TestQueryArgumentsForRefusesADeclarationItCannotDescribe: the mount-time half.
// Verb.Validate already refuses everything this trips on, so reaching it means
// the served declaration did not come through that path — and a route that
// quietly accepted no arguments would be worse than a boot that stops, because
// every call would then look successful and do the wrong thing.
func TestQueryArgumentsForRefusesADeclarationItCannotDescribe(t *testing.T) {
	// Required names an argument the schema does not declare: nothing a caller
	// could send would ever satisfy it, so the route can never answer 200.
	unsatisfiable := queryVerb()
	unsatisfiable.InputSchema = json.RawMessage(
		`{"type":"object","properties":{"payload":{"type":"string"}},"required":["missing"]}`)
	if _, err := queryArgumentsFor(unsatisfiable); err == nil {
		t.Error("a required argument the schema does not declare was accepted")
	}
	malformed := queryVerb()
	malformed.InputSchema = json.RawMessage(`{"type":"object","properties":[]}`)
	if _, err := queryArgumentsFor(malformed); err == nil {
		t.Error("an input schema this route cannot read was accepted")
	}
	// And a body method gets no query description at all, which is the signal the
	// handler branches on.
	body := unitVerb("alpha", "sync_contacts", extension.TierAutoExecute, extension.ScopeWrite)
	args, err := queryArgumentsFor(body)
	if err != nil {
		t.Fatalf("a POST must resolve: %v", err)
	}
	if args != nil {
		t.Error("a body-carrying method was given a query description")
	}
}
