// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package hubspot

// In the integration build, a HubSpot client cannot reach the internet.
//
// The lane was calling api.hubapi.com for real. Every overlay suite connects an
// overlay, and Connect resolves the incumbent's portal id and owners directory
// through this client; both are best-effort, so 32 connects × 2 round trips came
// back 401 and were swallowed as warnings while the suite went green. It cost
// ~24s of a 34s package and made a merge gate depend on a vendor being up
// (#1996).
//
// The calls themselves are gone — compose binds a refusing incumbent under this
// tag rather than a live adapter — and this is what keeps them gone. It is the
// gate rather than the fix: the next provider call somebody adds to the connect
// path will not announce itself either, and a suite that dials a vendor and
// swallows the answer looks exactly like one that does not.
//
// netguard cannot do this job. It is an SSRF guard, so it refuses PRIVATE
// addresses and permits public ones — a public vendor host is precisely what it
// is built to allow. What the lane needs is the inverse.
//
// It runs AFTER every Option, so WithHTTPClient does not open a way around it.
// That mattered: leaving the injected client alone was an escape hatch worth
// more than the gate, since `WithHTTPClient(http.DefaultClient)` reads as an
// ordinary test seam and dials the vendor.
//
// One arm is not a gate and says so: a RoundTripper that is not net/http's own
// is left exactly as the caller built it. This TRUSTS the caller — a wrapper
// around a real transport would dial — and it is the right trade only because
// the alternative is rewriting the recorded fixtures every test in this package
// drives the client with. No non-test code passes WithHTTPClient at all.

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
)

// guardEgress returns hc with any dialing transport replaced by one that
// refuses to leave this host. The client is copied rather than edited: the
// caller may hold the one it passed in and use it elsewhere.
func guardEgress(hc *http.Client) *http.Client {
	guarded := *hc
	switch transport := hc.Transport.(type) {
	case nil:
		// net/http falls back to DefaultTransport, which dials.
		guarded.Transport = gatedTransport()
	case *http.Transport:
		clone := transport.Clone()
		clone.DialContext = (&net.Dialer{Control: refuseOffHostDial}).DialContext
		// DialTLSContext, not only DialContext. Clone preserves it, and for an
		// https URL net/http calls it INSTEAD of DialContext — so a caller that
		// set one would have kept a dialer the gate never sees, on exactly the
		// scheme the vendor is reached over. Clearing it puts the connection
		// back through DialContext with net/http's own TLS on top.
		clone.DialTLSContext = nil
		clone.Proxy = nil
		guarded.Transport = clone
	default:
		// A RoundTripper of the caller's own: a recorded fixture, an httptest
		// transport. It reaches no socket through this package.
	}
	return &guarded
}

// gatedTransport declares only the two fields that decide this build's
// behaviour.
//
// Proxy is left nil ON PURPOSE, here and in the clone above, and it is the half
// that is easy to miss. With http.ProxyFromEnvironment a machine carrying
// HTTPS_PROXY would dial the proxy instead of the vendor — the gate would see
// the proxy's address, admit it if it were loopback, and the proxy would fetch
// api.hubapi.com on the test's behalf. A gate that a stray environment variable
// routes around is not one.
func gatedTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{Control: refuseOffHostDial}).DialContext,
	}
}

// refuseOffHostDial fails any connection to an address that is not loopback.
//
// The hook fires on the resolved address just before connect, so an httptest
// server — which is how every test in this package drives the client — is
// unaffected, and a name that resolves anywhere else fails with a message
// naming what to do instead.
func refuseOffHostDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("hubspot: the integration build refuses to dial %q, which it could not parse as an address", address)
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("hubspot: the integration build refuses to dial %s — a test reached the real vendor. "+
		"Point the client at an httptest server with WithBaseURL, or substitute the incumbent "+
		"(compose builds a refusing one under this tag)", address)
}
