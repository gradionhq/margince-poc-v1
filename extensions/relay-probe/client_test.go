// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// The provider client: the egress guard first, because it is the one thing here
// that is a security boundary rather than a decoding detail.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The guard's real definition. base_url is text a member typed, so without this
// a name resolving to a link-local address turns this installation's own worker
// into a probe of its network — and the answer comes back to whoever asked.
func TestTheEgressGuardRefusesEveryNonPublicAddress(t *testing.T) {
	for address, public := range map[string]bool{
		// Routable, and the only class that may be dialed.
		"93.184.216.34":     true,
		"2606:2800:220:1::": true,
		// Loopback, private, link-local — the obvious three.
		"127.0.0.1":       false,
		"10.0.0.5":        false,
		"192.168.1.10":    false,
		"172.16.4.4":      false,
		"169.254.169.254": false,
		"::1":             false,
		"fe80::1":         false,
		// The ranges the stdlib predicates miss, and the reason the list of
		// CIDRs exists at all: each of these reads as ordinary public space to
		// IsPrivate.
		"0.0.0.0":              false,
		"0.1.2.3":              false,
		"100.64.0.1":           false, // CGNAT
		"192.0.0.1":            false, // protocol assignment
		"192.0.2.5":            false, // documentation
		"198.18.0.1":           false, // benchmarking
		"198.51.100.5":         false,
		"203.0.113.5":          false,
		"240.0.0.1":            false, // reserved
		"255.255.255.255":      false,
		"192.88.99.1":          false, // 6to4 relay anycast
		"2001:db8::1":          false, // documentation
		"3fff::1":              false, // documentation, the newer range
		"64:ff9b::a9fe:a9fe":   false, // NAT64 onto the metadata address
		"64:ff9b:1::a9fe:a9fe": false, // local-use NAT64 onto the same address
		"2002:7f00:1::1":       false, // 6to4 carrying 127.0.0.1
		"2001::1":              false, // Teredo, inside the 2001::/23 blanket
		"100::1":               false, // discard-only
		"100:0:0:1::1":         false, // dummy prefix
		"5f00::1":              false, // SRv6 SIDs
		"fec0::1":              false, // deprecated site-local
		"::ffff:0:a9fe:a9fe":   false, // IPv4-translated onto the metadata address
		"::ffff:127.0.0.1":     false, // IPv4-mapped loopback
	} {
		t.Run(address, func(t *testing.T) {
			ip := net.ParseIP(address)
			if ip == nil {
				t.Fatalf("%q is not an address — the fixture is wrong, which would make this row pass for free", address)
			}
			if got := publicIP(ip); got != public {
				t.Errorf("publicIP(%s) = %v, want %v", address, got, public)
			}
		})
	}
}

// The control hook is what actually runs, on the CONCRETE address the resolver
// returned — so a DNS answer pointing inward cannot bypass a check made on the
// name.
func TestTheDialControlRefusesAResolvedPrivateAddress(t *testing.T) {
	if err := refusePrivate("tcp", "169.254.169.254:443", nil); err == nil {
		t.Fatal("the dial control admitted the cloud metadata address")
	}
	if err := refusePrivate("tcp", "93.184.216.34:443", nil); err != nil {
		t.Fatalf("the dial control refused a public address: %v", err)
	}
	if err := refusePrivate("tcp", "not-an-address", nil); err == nil {
		t.Error("the dial control admitted something that is not host:port")
	}
}

// The guard is attached by the production constructor, which is the property
// that matters: a client built the way a poll builds it cannot reach a loopback
// listener, however the URL was spelled.
func TestTheProductionClientCannotDialLoopback(t *testing.T) {
	// An address literal is refused by the parser before any dial, so the URL
	// is spelled with a NAME that resolves to loopback — which is the shape the
	// dial control exists for, and the one a check on the text cannot catch.
	api, err := newClient("https://localhost", "pat_test")
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	_, err = api.me(context.Background())
	if err == nil {
		t.Fatal("the production client reached a loopback host")
	}
	// Asserted on the guard's own words rather than on any failure: a name that
	// did not resolve would fail too, and would prove nothing about the guard.
	if !strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("err = %v, want the egress guard's refusal", err)
	}
}

// A provider's status is mapped onto the three classes a poll can act on, and
// the mapping decides behaviour: a 401 parks the connection, a 429 does not.
func TestTheStatusMapping(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusOK:                  nil,
		http.StatusUnauthorized:        errUnauthorized,
		http.StatusForbidden:           errUnauthorized,
		http.StatusTooManyRequests:     errTransient,
		http.StatusBadGateway:          errTransient,
		http.StatusInternalServerError: errTransient,
		http.StatusBadRequest:          errProvider,
		http.StatusNotFound:            errProvider,
	} {
		got := classify(status)
		if want == nil {
			if got != nil {
				t.Errorf("classify(%d) = %v, want nil", status, got)
			}
			continue
		}
		if !errors.Is(got, want) {
			t.Errorf("classify(%d) = %v, want %v", status, got, want)
		}
	}
}

// The batch lookup answers a bare ARRAY. Decoding it as an object with a member
// compiles, answers zero users, and leaves every landed record with a
// counterparty that has no address — which is why the shape is pinned.
func TestTheBatchLookupReadsABareArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"sender-1","email":"outside@example.com","display_name":"A Sender"}]`))
	}))
	defer server.Close()

	api := testClient(t, server)
	resolved, err := api.users(context.Background(), []string{"sender-1"})
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	if got := resolved["sender-1"].Email; got != "outside@example.com" {
		t.Fatalf("resolved %q, want the sender's address — a wrapper shape reads zero users and fails silently", got)
	}
}

// Nothing is looked up when there is nobody to look up: a batch call with an
// empty list is a request a guest makes for no reason.
func TestNoSendersMeansNoLookup(t *testing.T) {
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	if _, err := testClient(t, server).users(context.Background(), nil); err != nil {
		t.Fatalf("users: %v", err)
	}
	if called {
		t.Error("an empty batch still called the provider")
	}
}

// A provider deciding how much this worker reads into memory is the same class
// of problem as one deciding how much it stores.
func TestAnOversizedAnswerIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[`))
		filler := strings.Repeat("x", 64*1024)
		for range (maxResponseBytes / len(filler)) + 2 {
			_, _ = w.Write([]byte(`"` + filler + `",`))
		}
		_, _ = w.Write([]byte(`""]}`))
	}))
	defer server.Close()

	_, err := testClient(t, server).inbox(context.Background(), 0)
	if !errors.Is(err, errProvider) {
		t.Fatalf("err = %v, want the oversized answer refused", err)
	}
}

// An account answer missing the two fields the poll needs is unusable, and
// saying so beats polling an inbox this unit cannot key records against.
func TestAnAccountAnswerWithoutAWorkspaceIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"provider-member","email":"member@installation.test"}`))
	}))
	defer server.Close()

	if _, err := testClient(t, server).me(context.Background()); !errors.Is(err, errProvider) {
		t.Fatalf("err = %v, want the incomplete account answer refused", err)
	}
}

// An account with no ADDRESS is refused for a different reason than the one
// above, and it is the reason worth a second test: the two ids only key the
// records, while the address is a PARTY of every one of them. The core reads
// the set of parties to decide whether a message is only colleagues talking,
// and a party it cannot read is one it silently skips — so an addressless
// member would not fail that gate, it would quietly narrow it, once per
// record, for as long as the connection lived.
func TestAnAccountAnswerWithoutAnAddressIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"provider-member","workspace_id":"ws-7","email":"   "}`))
	}))
	defer server.Close()

	if _, err := testClient(t, server).me(context.Background()); !errors.Is(err, errProvider) {
		t.Fatalf("err = %v, want an account with no address refused", err)
	}
}

// The token rides every request, which is what makes https and the absence of
// credentials in the URL load-bearing rather than tidy.
func TestEveryRequestCarriesTheMembersToken(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"id":"provider-member","workspace_id":"ws-7","email":"member@installation.test"}`))
	}))
	defer server.Close()

	if _, err := testClient(t, server).me(context.Background()); err != nil {
		t.Fatalf("me: %v", err)
	}
	if seen != "Bearer pat_test" {
		t.Errorf("Authorization = %q, want the member's own token", seen)
	}
}

// testClient points a client at a test server, without the production
// constructor's guard — which refuses loopback by design and has its own tests
// above.
func testClient(t *testing.T, server *httptest.Server) *client {
	t.Helper()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parsing the test server's URL: %v", err)
	}
	return &client{base: base, token: "pat_test", http: server.Client()}
}
