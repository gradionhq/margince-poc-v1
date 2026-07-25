// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package googleconn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// fakeOAuth is a stub Google OAuth2 handshake for the Authenticate tests.
type fakeOAuth struct {
	refresh, access string
	exchangeErr     error
	accessErr       error
}

func (fakeOAuth) AuthCodeURL(state, _ string) string { return "https://auth?state=" + state }
func (f fakeOAuth) Exchange(context.Context, string, string) (string, error) {
	return f.refresh, f.exchangeErr
}

func (f fakeOAuth) AccessToken(context.Context, string) (string, error) {
	return f.access, f.accessErr
}

func TestBoundedClientHasTimeout(t *testing.T) {
	if c := BoundedClient(); c.Timeout != httpTimeout {
		t.Errorf("BoundedClient timeout = %v, want %v", c.Timeout, httpTimeout)
	}
}

func TestScopeStringsRendersScopes(t *testing.T) {
	got := ScopeStrings([]principal.Scope{principal.ScopeRead})
	if len(got) != 1 || got[0] != string(principal.ScopeRead) {
		t.Errorf("ScopeStrings = %v, want [%q]", got, principal.ScopeRead)
	}
	if got := ScopeStrings(nil); len(got) != 0 {
		t.Errorf("ScopeStrings(nil) = %v, want empty", got)
	}
}

func TestAuthRequestFromRoundTrips(t *testing.T) {
	req, err := AuthRequestFrom("the-code", "https://app/callback")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	var p authPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		t.Fatalf("payload not decodable: %v", err)
	}
	if p.Code != "the-code" || p.RedirectURI != "https://app/callback" {
		t.Errorf("payload = %+v, want the-code / the callback", p)
	}
}

func TestAuthenticateSealsRefreshTokenAndOwner(t *testing.T) {
	req, err := AuthRequestFrom("the-code", "https://app/callback")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	owner := func(context.Context, string) (string, error) { return "rep@myco.com", nil }
	auth, err := Authenticate(context.Background(), fakeOAuth{refresh: "refresh-1", access: "access-1"}, req,
		[]principal.Scope{principal.ScopeRead}, owner)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	var st AuthState
	if err := json.Unmarshal(auth, &st); err != nil {
		t.Fatalf("auth is not AuthState json: %v", err)
	}
	if st.RefreshToken != "refresh-1" || st.Owner != "rep@myco.com" || len(st.Scopes) != 1 {
		t.Errorf("AuthState = %+v, want refresh-1 / rep@myco.com / [read]", st)
	}
}

func TestAuthenticateRejectsMissingCode(t *testing.T) {
	req, err := AuthRequestFrom("", "https://app/callback")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	owner := func(context.Context, string) (string, error) { return "x", nil }
	if _, err := Authenticate(context.Background(), fakeOAuth{}, req, nil, owner); !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("want ErrAuthRejected for a missing code, got %v", err)
	}
}

func TestAuthenticatePropagatesOwnerError(t *testing.T) {
	req, err := AuthRequestFrom("the-code", "https://app/callback")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	boom := errors.New("owner lookup failed")
	owner := func(context.Context, string) (string, error) { return "", boom }
	if _, err := Authenticate(context.Background(), fakeOAuth{refresh: "r", access: "a"}, req, nil, owner); !errors.Is(err, boom) {
		t.Fatalf("want the owner-resolution error, got %v", err)
	}
}

func TestAuthenticateRejectsMalformedPayload(t *testing.T) {
	owner := func(context.Context, string) (string, error) { return "x", nil }
	if _, err := Authenticate(context.Background(), fakeOAuth{}, connector.AuthRequest{Payload: []byte("}bad{")}, nil, owner); err == nil {
		t.Fatal("Authenticate must reject a malformed auth payload")
	}
}

func TestGetDecodesOKAndMapsSentinels(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		//craft:ignore swallowed-errors test stub encode
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "rep@myco.com"})
	})
	mux.HandleFunc("/forbidden", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	mux.HandleFunc("/toomany", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	mux.HandleFunc("/quota", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		//craft:ignore swallowed-errors test stub write
		_, _ = w.Write([]byte(`{"error":{"errors":[{"reason":"rateLimitExceeded"}]}}`))
	})
	mux.HandleFunc("/quotaexceeded", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		//craft:ignore swallowed-errors test stub write
		_, _ = w.Write([]byte(`{"error":{"errors":[{"reason":"quotaExceeded"}]}}`))
	})
	mux.HandleFunc("/boom", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	mux.HandleFunc("/garbage", func(w http.ResponseWriter, _ *http.Request) {
		//craft:ignore swallowed-errors test stub write
		_, _ = w.Write([]byte("not json"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := srv.Client()

	var out struct {
		ID string `json:"id"`
	}
	if status, err := Get(context.Background(), client, srv.URL, "tok", "/ok", nil, &out); err != nil || status != http.StatusOK || out.ID != "rep@myco.com" {
		t.Fatalf("Get /ok = (%d, %v), id=%q; want (200, nil, rep@myco.com)", status, err, out.ID)
	}
	if _, err := Get(context.Background(), client, srv.URL, "tok", "/forbidden", nil, &out); !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("Get /forbidden err = %v, want ErrAuthRejected", err)
	}
	if _, err := Get(context.Background(), client, srv.URL, "tok", "/boom", nil, &out); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Get /boom err = %v, want ErrUnreachable", err)
	}
	if _, err := Get(context.Background(), client, srv.URL, "tok", "/garbage", nil, &out); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Get /garbage (undecodable) err = %v, want ErrUnreachable", err)
	}
	// A 429 and a quota-403 are retryable rate limits, NOT rejected auth — the
	// registry must back off and honor Retry-After, not park the connection.
	var rl *connector.RateLimitedError
	if _, err := Get(context.Background(), client, srv.URL, "tok", "/toomany", nil, &out); !errors.As(err, &rl) {
		t.Fatalf("Get /toomany err = %v, want a RateLimitedError", err)
	} else if rl.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", rl.RetryAfter)
	}
	rl = nil
	if _, err := Get(context.Background(), client, srv.URL, "tok", "/quota", nil, &out); !errors.As(err, &rl) {
		t.Fatalf("Get /quota (403 rateLimitExceeded) err = %v, want a RateLimitedError", err)
	}
	rl = nil
	if _, err := Get(context.Background(), client, srv.URL, "tok", "/quotaexceeded", nil, &out); !errors.As(err, &rl) {
		t.Fatalf("Get /quotaexceeded (403 quotaExceeded) err = %v, want a RateLimitedError", err)
	}
}

func TestAuthenticateRejectsEmptyOwner(t *testing.T) {
	req, err := AuthRequestFrom("the-code", "https://app/callback")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	// A provider that returns a blank owner would make every counterparty look
	// external — refuse the connection rather than seal an unclassifiable one.
	owner := func(context.Context, string) (string, error) { return "  ", nil }
	if _, err := Authenticate(context.Background(), fakeOAuth{refresh: "r", access: "a"}, req, nil, owner); !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("want ErrAuthRejected for an empty owner, got %v", err)
	}
}

func TestGetUnreachableHost(t *testing.T) {
	var out struct{}
	// A closed server → transport error → ErrUnreachable.
	srv := httptest.NewServer(http.NewServeMux())
	url := srv.URL
	srv.Close()
	if _, err := Get(context.Background(), srv.Client(), url, "tok", "/x", nil, &out); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Get to a dead host err = %v, want ErrUnreachable", err)
	}
}

// A disabled API and a revoked grant are both 403s. They schedule the same way,
// but a human can only fix one of them — so the reason code has to survive the
// transport, or the two stay indistinguishable in a log.
func TestForbiddenCarriesGoogleReasonAndStaysAuthRejected(t *testing.T) {
	mux := http.NewServeMux()
	// Google's classic envelope for an API that was never enabled.
	mux.HandleFunc("/disabled", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		//craft:ignore swallowed-errors test stub write
		_, _ = w.Write([]byte(`{"error":{"code":403,"errors":[{"reason":"accessNotConfigured"}],"status":"PERMISSION_DENIED"}}`))
	})
	// The newer ErrorInfo form of the same fact.
	mux.HandleFunc("/disabled-details", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		//craft:ignore swallowed-errors test stub write
		_, _ = w.Write([]byte(`{"error":{"code":403,"details":[{"reason":"SERVICE_DISABLED"}]}}`))
	})
	// A revoked/insufficient grant: rejected, but NOT a misconfiguration.
	mux.HandleFunc("/revoked", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		//craft:ignore swallowed-errors test stub write
		_, _ = w.Write([]byte(`{"error":{"code":401,"errors":[{"reason":"authError"}]}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var out struct{}
	for _, tc := range []struct {
		path          string
		wantStatus    int
		wantReason    string
		wantMisconfig bool
	}{
		{"/disabled", http.StatusForbidden, "accessNotConfigured", true},
		{"/disabled-details", http.StatusForbidden, "SERVICE_DISABLED", true},
		{"/revoked", http.StatusUnauthorized, "authError", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			_, err := Get(context.Background(), srv.Client(), srv.URL, "tok", tc.path, nil, &out)
			// Classification must be untouched: the scheduler still sees auth.
			if !errors.Is(err, ErrAuthRejected) {
				t.Fatalf("err = %v, want ErrAuthRejected", err)
			}
			pe, ok := errors.AsType[*connector.ProviderError](err)
			if !ok {
				t.Fatalf("err = %v, want a *connector.ProviderError carrying the detail", err)
			}
			if pe.Status != tc.wantStatus {
				t.Errorf("Status = %d, want %d", pe.Status, tc.wantStatus)
			}
			if pe.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", pe.Reason, tc.wantReason)
			}
			if pe.Op != tc.path {
				t.Errorf("Op = %q, want the failing path %q", pe.Op, tc.path)
			}
			if got := Misconfigured(err); got != tc.wantMisconfig {
				t.Errorf("Misconfigured = %v, want %v", got, tc.wantMisconfig)
			}
			// The operator has to be able to read all three facts at a glance.
			if msg := err.Error(); !strings.Contains(msg, tc.path) || !strings.Contains(msg, tc.wantReason) {
				t.Errorf("Error() = %q, want it to name both the call and the reason", msg)
			}
		})
	}
}

// A body that is not Google's envelope must yield no reason at all: an absent
// reason that reads as a present one would misroute the operator.
func TestReasonIsEmptyForABodyThatNamesNone(t *testing.T) {
	for name, body := range map[string]string{
		"not json":       "<html>502 from a proxy</html>",
		"empty":          "",
		"no reason":      `{"error":{"code":403}}`,
		"unrelated json": `{"ok":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := Reason([]byte(body)); got != "" {
				t.Errorf("Reason(%q) = %q, want \"\"", body, got)
			}
		})
	}
}

// Misconfigured must answer false for errors that carry no provider detail at
// all — a plain sentinel is not evidence of a disabled API.
func TestMisconfiguredIsFalseWithoutAProviderReason(t *testing.T) {
	if Misconfigured(ErrAuthRejected) {
		t.Error("Misconfigured(bare ErrAuthRejected) = true, want false")
	}
	if Misconfigured(nil) {
		t.Error("Misconfigured(nil) = true, want false")
	}
}

// The predicate that decides park-vs-backoff, tested where it lives — including
// against the envelopes Google really sends, status field and all. A readable
// refusal beside a limit parks (the refusal is the more specific claim); a field
// we cannot read does not, because parking a healthy connection is the more
// expensive mistake.
func TestRateLimitBodyDistinguishesThrottlingFromARefusal(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"classic rate limit":                    {`{"error":{"errors":[{"reason":"rateLimitExceeded"}]}}`, true},
		"per-user rate limit":                   {`{"error":{"errors":[{"reason":"userRateLimitExceeded"}]}}`, true},
		"daily limit":                           {`{"error":{"errors":[{"reason":"dailyLimitExceeded"}]}}`, true},
		"quota":                                 {`{"error":{"errors":[{"reason":"quotaExceeded"}]}}`, true},
		"newer errorinfo enum":                  {`{"error":{"details":[{"reason":"RATE_LIMIT_EXCEEDED"}]}}`, true},
		"resource exhausted":                    {`{"error":{"status":"RESOURCE_EXHAUSTED"}}`, true},
		"a limit family we have not enumerated": {`{"error":{"errors":[{"reason":"concurrentLimitExceeded"}]}}`, true},

		// The envelopes Google actually sends. On a 403 the status is
		// PERMISSION_DENIED — the HTTP code restated, carrying nothing the caller
		// has not already branched on. Letting it speak for the body would veto
		// every throttling verdict here, since this predicate is asked about
		// nothing but 403s.
		"real gmail per-user limit, status populated": {
			`{"error":{"code":403,"message":"User-rate limit exceeded.","errors":[{"domain":"usageLimits","reason":"userRateLimitExceeded"}],"status":"PERMISSION_DENIED"}}`,
			true,
		},
		"real calendar quota, status populated": {
			`{"error":{"code":403,"message":"Calendar usage limits exceeded.","errors":[{"domain":"usageLimits","reason":"quotaExceeded"}],"status":"PERMISSION_DENIED"}}`,
			true,
		},
		"modern ErrorInfo limit, status populated": {
			`{"error":{"code":403,"errors":[{"reason":"rateLimitExceeded"}],"details":[{"reason":"RATE_LIMIT_EXCEEDED"}],"status":"PERMISSION_DENIED"}}`,
			true,
		},

		// The refusal cases. Each names something that is NOT a limit, so the
		// verdict must be "not throttling" however the limit code is dressed up.
		"a refusal first, a limit after": {`{"error":{"errors":[{"reason":"authError"},{"reason":"quotaExceeded"}]}}`, false},
		"a disabled API and a limit":     {`{"error":{"details":[{"reason":"SERVICE_DISABLED"},{"reason":"RATE_LIMIT_EXCEEDED"}]}}`, false},
		"a refusal with a limit status":  {`{"error":{"status":"RESOURCE_EXHAUSTED","errors":[{"reason":"authError"}]}}`, false},
		"a limit named only in prose":    {`{"error":{"errors":[{"reason":"authError","message":"quotaExceeded"}]}}`, false},
		"prose alone":                    {`{"error":{"message":"rateLimitExceeded for this user"}}`, false},
		// A reason we cannot read is no evidence either way. It must not veto a
		// limit the body also names: parking a healthy connection stops capture
		// until a human re-runs OAuth, while a genuinely revoked grant is refused
		// again at the token endpoint on the next sync — the two mistakes are not
		// equally cheap.
		"an unreadable reason does not veto a limit": {`{"error":{"errors":[{"reason":"Rate Limit Exceeded"},{"reason":"rateLimitExceeded"}]}}`, true},
		// But a reason we CAN read and that is not a limit does veto it.
		"a readable refusal vetoes a later limit": {`{"error":{"errors":[{"reason":"authError"},{"reason":"rateLimitExceeded"}]}}`, false},
		"names nothing at all":                    {`{"error":{"code":403}}`, false},
		"not google's envelope":                   {`<html>502 from a proxy</html>`, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := RateLimitBody([]byte(tc.body)); got != tc.want {
				t.Errorf("RateLimitBody(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// Reason answers a different question from RateLimitBody — "the most diagnosable
// code" — so it walks past a value it cannot use rather than letting it shadow a
// usable one, which would make the body less diagnosable than it really was.
func TestReasonSkipsAnUnusableValueForAUsableOne(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"prose first, code second": {
			`{"error":{"errors":[{"reason":"not a code at all"},{"reason":"accessNotConfigured"}]}}`,
			"accessNotConfigured",
		},
		"oversized first, code in details": {
			`{"error":{"errors":[{"reason":"` + strings.Repeat("a", 70) + `"}],"details":[{"reason":"SERVICE_DISABLED"}]}}`,
			"SERVICE_DISABLED",
		},
		"unusable everywhere but the status": {
			`{"error":{"errors":[{"reason":"has spaces"}],"status":"PERMISSION_DENIED"}}`,
			"PERMISSION_DENIED",
		},
		"first usable code wins": {
			`{"error":{"errors":[{"reason":"authError"},{"reason":"accessNotConfigured"}]}}`,
			"authError",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Reason([]byte(tc.body)); got != tc.want {
				t.Errorf("Reason = %q, want %q", got, tc.want)
			}
		})
	}
}
