// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The egress denylist is part of the published extension surface.
//
//margince:extension-surface

package extension

import "net"

// reservedCIDRs are the non-public ranges the stdlib predicates miss: the
// this-network block, CGNAT, benchmarking, documentation, protocol assignments,
// broadcast and discard space, plus the IPv6 ranges that CARRY an IPv4 address
// inside them — both NAT64 prefixes, 6to4 and its relay anycast,
// IPv4-compatible and IPv4-translated — which To4()/IsPrivate() do not catch,
// and which are therefore how an internal address is named without looking like
// one. Naming them IS the guard, so they are literals.
//
// WHY THEY ARE PUBLISHED. A unit that dials a host one of its own members
// supplied needs this denylist, and a unit is its own module that may import
// only pkg/**. Before this existed the only way to have it was to hand-copy the
// literals, which is a copy that drifts — and a range the core refuses while the
// copy admits it is not a formatting difference, it is a member naming an
// internal address and reaching it. The list is the same list as
// internal/platform/netguard's, and netguardparity_test.go holds the two equal.
//
// IPv4-MAPPED ::ffff:0:0/96 is deliberately absent, and is not the same thing as
// IPv4-translated below: it is an ordinary host under a second spelling, folded
// through To4() by both the stdlib predicates and net.IPNet.Contains, so
// ::ffff:127.0.0.1 is already refused as loopback while ::ffff:8.8.8.8 stays
// reachable. Listing it would block the public internet.
var reservedCIDRs = []string{
	// These literal reserved/special-use ranges ARE the guard: the SSRF
	// denylist must name them explicitly. NOSONAR: hardcoding them is the point.
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", // NOSONAR
	"192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", // NOSONAR
	"203.0.113.0/24", "240.0.0.0/4", // NOSONAR
	"100::/64", "100:0:0:1::/64", "2001::/23", "2001:db8::/32", "2002::/16",
	"3fff::/20", "5f00::/16", "64:ff9b::/96", "64:ff9b:1::/48", "fec0::/10",
	"::ffff:0:0:0/96", "::/96",
}

// reservedNets is the parsed form, built once. A CIDR that does not parse is a
// typo in the guard itself, which panics at init rather than silently narrowing
// the denylist — the same direction netguard takes, and for the same reason: a
// guard that quietly refuses less is worse than one that does not start.
var reservedNets = func() []*net.IPNet {
	nets := make([]*net.IPNet, len(reservedCIDRs))
	for i, c := range reservedCIDRs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		nets[i] = n
	}
	return nets
}()

// ReservedNets returns the non-public ranges a unit must refuse to dial, in
// addition to what net.IP's own predicates already cover (loopback, private,
// link-local unicast, multicast, unspecified). Use it in a unit that dials a
// host it did not choose:
//
//	for _, n := range extension.ReservedNets() {
//		if n.Contains(ip) {
//			return errRefused
//		}
//	}
//
// A FUNCTION returning a fresh slice, not an exported var: the slice crosses a
// module boundary to callers the core does not control, and one that overwrote
// an element would be editing the core's own guard. The *net.IPNet values are
// shared, which is why the doc comment does not invite mutating them either;
// copying the whole graph on every dial would be a real cost for a hazard no
// caller has a reason to reach for.
func ReservedNets() []*net.IPNet {
	out := make([]*net.IPNet, len(reservedNets))
	copy(out, reservedNets)
	return out
}
