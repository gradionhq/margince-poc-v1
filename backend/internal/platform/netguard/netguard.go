// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package netguard is the egress SSRF guard: a tenant-supplied host (a website
// URL to read back, a mailbox to capture) must never become a probe of the
// deployment's own network. It classifies an IP as public or reserved and
// offers a net.Dialer.Control that refuses to dial anything non-public, checked
// on the concrete resolved address so a DNS answer cannot bypass it.
package netguard

import (
	"fmt"
	"net"
	"syscall"
)

// reservedNets are the non-public ranges the stdlib predicates miss: every IANA
// special-purpose range that is not globally reachable and that
// IsLoopback/IsPrivate/IsLinkLocalUnicast/IsMulticast/IsUnspecified do not
// already cover, plus two blankets taken whole, described below.
//
// Two groups, because they are refused for two different reasons. Ranges that
// reach nobody, so a request to one is a request to this deployment or to
// nothing: this-network 0.0.0.0/8 (only the exact 0.0.0.0 is IsUnspecified, but
// the whole block routes to loopback on Linux), CGNAT, benchmarking,
// documentation (v4, 2001:db8::/32 and the newer 3fff::/20), protocol
// assignments, broadcast, discard-only 100::/64, the dummy prefix
// 100:0:0:1::/64, SRv6 SIDs and deprecated site-local.
//
// And ranges that CARRY another address inside them, which is how an internal
// address is named without looking like one: both NAT64 prefixes (well-known
// 64:ff9b::/96 and local-use 64:ff9b:1::/48 — 64:ff9b::a9fe:a9fe is link-local
// metadata wearing a v6 address), 6to4 2002::/16 with its relay anycast
// 192.88.99.0/24 (2002:7f00:1::1 is 127.0.0.1), IPv4-compatible ::/96 and
// IPv4-translated ::ffff:0:0:0/96.
//
// The two blankets are 2001::/23 and the NAT64 prefixes. Both hold entries IANA
// marks globally reachable, so they are wider than the rule above: what lives
// there is Teredo, AMT and ORCHIDv2 — encapsulations and identifiers, not hosts
// this deployment has business dialing on a tenant's say-so.
//
// IPv4-MAPPED ::ffff:0:0/96 is deliberately NOT here, and is not the same thing
// as IPv4-translated one paragraph up: it is an ordinary host under a second
// spelling, and both the stdlib predicates and net.IPNet.Contains fold it
// through To4(), so ::ffff:127.0.0.1 is already refused as loopback while
// ::ffff:8.8.8.8 stays reachable. Listing it would block the public internet.
var reservedNets = func() []*net.IPNet {
	// These literal reserved/special-use ranges ARE the guard: the SSRF
	// denylist must name them explicitly. NOSONAR: hardcoding them is the point.
	cidrs := []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", // NOSONAR
		"192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", // NOSONAR
		"203.0.113.0/24", "240.0.0.0/4", // NOSONAR
		"100::/64", "100:0:0:1::/64", "2001::/23", "2001:db8::/32", "2002::/16",
		"3fff::/20", "5f00::/16", "64:ff9b::/96", "64:ff9b:1::/48", "fec0::/10",
		"::ffff:0:0:0/96", "::/96",
	}
	nets := make([]*net.IPNet, len(cidrs))
	for i, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		nets[i] = n
	}
	return nets
}()

// PublicIP reports whether ip is a globally routable unicast address — i.e.
// safe to dial from a request carrying a tenant-supplied host. Loopback,
// private, link-local, multicast, unspecified and the reserved ranges above
// are all rejected.
func PublicIP(ip net.IP) bool {
	// IsMulticast already covers link-local multicast, so it is not repeated.
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, n := range reservedNets {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// RefusePrivate is a net.Dialer.Control hook that refuses to dial any
// non-public address. It runs after DNS resolution on the concrete IP the
// dialer is about to connect to, so a host that resolves to an internal
// address (or rebinds to one) is blocked at connect time, not merely
// pre-checked. Wire it as Dialer.Control on any dialer fed a tenant host.
func RefusePrivate(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("netguard: unparseable dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("netguard: dial address %q is not a literal IP", host)
	}
	if !PublicIP(ip) {
		return fmt.Errorf("netguard: refusing to dial non-public address %s", host)
	}
	return nil
}

// ReservedNetsForTest exposes the guard's own parsed denylist so the module's
// parity test can hold it equal to the published pkg/extension list. Two lists
// exist because a unit may import only pkg/**, and the guard must not depend on
// the surface it is guarding traffic for; what stops them drifting is that test.
//
// Test-only by name and by contract: nothing in the tree calls it outside
// netguardparity_test.go, and PublicIP is the API for deciding about an address.
func ReservedNetsForTest() []*net.IPNet { return reservedNets }
