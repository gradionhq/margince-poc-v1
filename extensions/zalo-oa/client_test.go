// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The provider boundary, and the one trap this provider sets that no other
// connector in this tree has to handle.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// EVERY Zalo response is HTTP 200, including its refusals. A client that
// classified on the status line would read a revoked token as a successful empty
// page: it would advance nothing, report nothing, and look exactly like an
// Official Account nobody messages.
//
// This is the single most load-bearing test in the unit, which is why it drives
// each refusal through the whole client rather than calling the classifier.
func TestAnHTTP200CarryingARefusalIsARefusal(t *testing.T) {
	for name, arm := range map[string]struct {
		code int
		want error
	}{
		"an expired token":       {codeTokenExpired, errUnauthorized},
		"an invalid token":       {codeTokenInvalid, errUnauthorized},
		"a package too low":      {codeTierTooLow, errTierTooLow},
		"an app without a group": {codeAPINotRegisterd, errAPINotRegistered},
		"a rate limit":           {codeRateLimited, errTransient},
		"a bad argument":         {codeInvalidArgument, errProvider},
		"a page over the cap":    {codePageTooLarge, errProvider},
		"an unknown endpoint":    {404, errProvider},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newZaloFake(t)
			fake.errorCode = arm.code
			_, err := fake.client("token").profile(t.Context())
			if !errors.Is(err, arm.want) {
				t.Fatalf("error %d classified as %v, want %v — the status line was 200 in every one of these", arm.code, err, arm.want)
			}
		})
	}
}

// The three refusals that cost different things to resolve are told apart. A
// package is an annual purchase, an app permission group is a free click, and a
// token is renewed by this unit without anybody being told — collapsing any two
// hands an operator an instruction that is wrong in a way they cannot check.
func TestThePayableRefusalAndTheFreeOneAreDifferentClasses(t *testing.T) {
	if errors.Is(errTierTooLow, errAPINotRegistered) || errors.Is(errAPINotRegistered, errTierTooLow) {
		t.Fatal("the package refusal and the app-permission refusal are the same class; one costs 2.500.000 đ a year and the other costs a click")
	}
	if errors.Is(errTierTooLow, errUnauthorized) {
		t.Fatal("a package refusal reads as a credential refusal, which would send an operator to re-authorize a working credential")
	}
}

// The GET grammar is the provider's own: the whole parameter object rides in one
// `data` query parameter as JSON, not as ordinary query arguments.
func TestAGetSendsItsParametersAsOneJSONDataArgument(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Query().Get("data")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": []any{}}); err != nil {
			t.Errorf("writing the answer: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	api := newClient("token")
	api.base = server.URL
	if _, err := api.recentChat(t.Context(), 30); err != nil {
		t.Fatalf("recentChat: %v", err)
	}
	var params map[string]int
	if err := json.Unmarshal([]byte(seen), &params); err != nil {
		t.Fatalf("the data argument %q is not JSON: %v", seen, err)
	}
	if params["offset"] != 30 {
		t.Fatalf("offset = %d, want 30", params["offset"])
	}
	if params["count"] != maxChatPage {
		t.Fatalf("count = %d, want the provider's cap of %d — a larger one is refused rather than clamped", params["count"], maxChatPage)
	}
}

// The credential rides its OWN header. Zalo names it `access_token` and ignores a
// bearer, so a client that sent Authorization would authenticate as nobody and
// read every refusal as an empty account.
func TestTheCredentialRidesTheProvidersOwnHeader(t *testing.T) {
	var bearer, accessToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer, accessToken = r.Header.Get("Authorization"), r.Header.Get("access_token")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"name": "NFQ"}}); err != nil {
			t.Errorf("writing the answer: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	api := newClient("the-token")
	api.base = server.URL
	if _, err := api.profile(t.Context()); err != nil {
		t.Fatalf("profile: %v", err)
	}
	if accessToken != "the-token" {
		t.Fatalf("access_token header = %q, want the token", accessToken)
	}
	if bearer != "" {
		t.Fatalf("an Authorization header was sent (%q); this provider ignores it", bearer)
	}
}

// Every message keeps the provider's ORIGINAL document. What capture stores as
// evidence must be what the provider said, and a re-marshal of the struct this
// unit decodes into would store only the fields it happens to know.
func TestEveryMessageKeepsTheProvidersOwnDocument(t *testing.T) {
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{{message("m1", 1000, srcUserToOA, "xin chào")}}

	read, err := fake.client("t").recentChat(t.Context(), 0)
	if err != nil {
		t.Fatalf("recentChat: %v", err)
	}
	if len(read) != 1 {
		t.Fatalf("read %d messages, want 1", len(read))
	}
	var original map[string]any
	if err := json.Unmarshal(read[0].Raw, &original); err != nil {
		t.Fatalf("the kept document is not JSON: %v", err)
	}
	// A field this unit's struct does not decode still has to survive, because
	// that is the difference between evidence and a summary.
	if original["to_display_name"] != "NFQ" {
		t.Fatalf("the kept document lost a field the struct does not read: %v", original)
	}
}

// A transport failure carries the unanswered class alongside the transient one.
// For a READ the two are the same fact and the next tick asks again; for a SEND
// they are not, which is what send.go depends on.
func TestATransportFailureIsBothTransientAndUnanswered(t *testing.T) {
	api := newClient("t")
	// A base nothing is listening on: the request goes out and no answer comes
	// back, which is exactly the shape the send path has to tell apart.
	api.base = "http://127.0.0.1:1"
	_, err := api.profile(t.Context())
	if !errors.Is(err, errTransient) {
		t.Fatalf("error = %v, want it to be transient for a read", err)
	}
	if !errors.Is(err, errUnanswered) {
		t.Fatalf("error = %v, want it to carry the unanswered class a send needs", err)
	}
}

// A body that is not the envelope, and a success carrying no data, are both
// refusals rather than silently-zero answers: decoding nothing into the caller's
// target would leave it holding a zero value as though the provider had said so.
func TestAnAnswerThisUnitCannotReadIsARefusalRatherThanAZeroValue(t *testing.T) {
	for name, body := range map[string]string{
		"not the envelope":        `["a list where an object belongs"]`,
		"success, no data":        `{"error":0,"message":"Success"}`,
		"data of the wrong shape": `{"error":0,"data":"a string"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var into oaProfile
			err := decodeEnvelope([]byte(body), &into)
			if !errors.Is(err, errProvider) {
				t.Fatalf("error = %v, want a provider-answer refusal", err)
			}
		})
	}
}

// An account answer with no name is refused: it is the connection's own label,
// it is rendered, and a blank one would present as an Official Account nobody
// named.
func TestAnAccountAnswerWithNoNameIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"error": 0, "data": map[string]any{"oa_id": "1"}}); err != nil {
			t.Errorf("writing the answer: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	api := newClient("t")
	api.base = server.URL
	if _, err := api.profile(t.Context()); !errors.Is(err, errProvider) {
		t.Fatalf("error = %v, want a provider-answer refusal", err)
	}
}

// A send returns the provider's own message id, which is the same value a later
// read of that message carries — that equality is what lets one number be both
// the receipt and the natural key.
func TestASendReturnsTheProvidersOwnMessageID(t *testing.T) {
	fake := newZaloFake(t)
	id, err := fake.client("t").sendConsultation(t.Context(), "user-1", "xin chào")
	if err != nil {
		t.Fatalf("sendConsultation: %v", err)
	}
	if id != "sent-1" {
		t.Fatalf("message id = %q, want the provider's own", id)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("the provider saw %d sends, want 1", len(fake.sent))
	}
	recipient, ok := fake.sent[0]["recipient"].(map[string]any)
	if !ok || recipient["user_id"] != "user-1" {
		t.Fatalf("the send named %v, want the bare account id inside a recipient object", fake.sent[0])
	}
}

// A numeric field this provider types as a STRING is read as one. `expires_in`
// and `last_interaction` both arrive quoted, and a unit that assumed a number
// would fail the whole document over a field it barely uses.
func TestAProviderNumberTypedAsAStringIsRead(t *testing.T) {
	if got := atoiOrZero("  90000 "); got != 90000 {
		t.Fatalf("atoiOrZero = %d, want 90000", got)
	}
	if got := atoiOrZero("not a number"); got != 0 {
		t.Fatalf("atoiOrZero = %d, want 0 for a value the provider did not state", got)
	}
}

// A response larger than the cap is refused rather than read: a provider deciding
// how much this worker holds in memory is the same class of problem as one
// deciding how much it stores.
func TestAnOversizedAnswerIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"error":0,"data":"` + strings.Repeat("x", maxResponseBytes+16) + `"}`)); err != nil {
			t.Errorf("writing the answer: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	api := newClient("t")
	api.base = server.URL
	if _, err := api.profile(t.Context()); !errors.Is(err, errProvider) {
		t.Fatalf("error = %v, want the over-cap refusal", err)
	}
}

// Something other than this API answering — a proxy, a maintenance page — is
// transient rather than a refusal from Zalo, because it is not Zalo speaking.
func TestATransportStatusThatIsNotThisAPIIsTransient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	api := newClient("t")
	api.base = server.URL
	if _, err := api.profile(t.Context()); !errors.Is(err, errTransient) {
		t.Fatalf("error = %v, want transient", err)
	}
}
