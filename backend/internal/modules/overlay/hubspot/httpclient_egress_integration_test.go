// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package hubspot

// The lane's egress gate, exercised through the client NewClient actually
// hands its callers rather than through the hook in isolation.
//
// That distinction is the test. Asserting the predicate alone proves the
// predicate and says nothing about whether NewClient installed it, and the
// wiring is the half that regresses — a second construction path, or a plain
// http.Client restored "because the timeout is all it needs", and a unit test
// over the hook keeps passing while the lane goes back to dialling the vendor.
//
// It reads c.httpClient because the request has to be observed at the transport:
// do() maps every transport failure onto ErrUnreachable and deliberately drops
// the cause, so through the public surface a refused dial and a thirty-second
// timeout are the same error — and this test's whole job is to tell them apart.
//
// It needs no database despite the tag. The behaviour under test exists only in
// this build, and a tagged test is the only way to reach it.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// unroutable is TEST-NET-1 (RFC 5737): reserved for documentation, routed
// nowhere. Naming a real public host would make this test's verdict depend on
// that host being up, which is the dependency the gate exists to remove.
const unroutable = "http://192.0.2.1:9"

// The same address over TLS. DialTLSContext is only consulted for https, so the
// arm that exists to prove it was cleared has to ask for that scheme.
const unroutableTLS = "https://192.0.2.1:9"

// The three ways a client's transport can arrive here. The middle two are the
// ones the gate used to let through, and both read as ordinary test seams:
// WithHTTPClient is documented for injecting a transport, and a client with no
// Transport at all silently uses net/http's DefaultTransport, which dials.
var dialingClients = []struct {
	name string
	url  string
	opts []Option
}{
	{"the client NewClient builds itself", unroutable, nil},
	{"a caller's client with no transport of its own", unroutable, []Option{WithHTTPClient(&http.Client{})}},
	{"a caller's client carrying a real transport", unroutable, []Option{WithHTTPClient(&http.Client{Transport: &http.Transport{}})}},
	// The one net/http calls INSTEAD of DialContext for an https URL, which is
	// the scheme the vendor is reached over. A transport carrying one kept a
	// dialer the gate never saw.
	{"a caller's transport with its own TLS dialer", unroutableTLS, []Option{WithHTTPClient(&http.Client{Transport: &http.Transport{
		DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("this dialer must never be reached: the gate should have replaced it")
		},
	}})}},
}

func TestTheIntegrationBuildRefusesToDialOffThisHost(t *testing.T) {
	for _, tc := range dialingClients {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient("na1", "token", tc.opts...)

			resp, err := client.httpClient.Get(tc.url)
			if err == nil {
				if cerr := resp.Body.Close(); cerr != nil {
					t.Errorf("closing the response body: %v", cerr)
				}
				t.Fatal("a request to an off-host address was attempted and answered — the integration build must not reach the network")
			}
			if !strings.Contains(err.Error(), "the integration build refuses to dial") {
				t.Fatalf("the request failed, but not because the gate refused it — a timeout looks like this too: %v", err)
			}
		})
	}
}

// The gate must not touch a RoundTripper the caller wrote. Those dial nothing,
// and rewriting one would break every recorded-fixture test in this package for
// no gain — so the exemption is stated here rather than left to be rediscovered.
func TestTheEgressGateLeavesACallersOwnRoundTripperAlone(t *testing.T) {
	var reached bool
	client := NewClient("na1", "token", WithHTTPClient(&http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			reached = true
			return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
		}),
	}))

	resp, err := client.httpClient.Get(unroutable)
	if err != nil {
		t.Fatalf("the gate replaced a caller's own RoundTripper: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing the response body: %v", err)
	}
	if !reached {
		t.Fatal("the caller's RoundTripper never ran — something stood in front of it")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTheEgressGateStillAdmitsALoopbackTestServer(t *testing.T) {
	// The other direction, and the one that decides whether the gate is usable at
	// all: every other test in this package drives the client against an httptest
	// server, so a gate that refused loopback would take the whole suite with it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	resp, err := NewClient("na1", "token").httpClient.Get(srv.URL)
	if err != nil {
		t.Fatalf("the gate refused a loopback test server: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing the response body: %v", err)
	}
}
