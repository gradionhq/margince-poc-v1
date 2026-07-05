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

// reservedNets are the non-public ranges the stdlib predicates miss: the
// "this-network" 0.0.0.0/8 (only the exact 0.0.0.0 is IsUnspecified, but the
// whole block routes to loopback on Linux), CGNAT, benchmark, documentation,
// protocol-assignment and broadcast, plus the IPv6 ranges that translate to
// IPv4 internals — NAT64 (64:ff9b::/96, e.g. 64:ff9b::a9fe:a9fe → link-local
// metadata) and IPv4-compatible ::/96 — which To4()/IsPrivate() do not catch.
var reservedNets = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"2001:db8::/32", "64:ff9b::/96", "::/96",
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
