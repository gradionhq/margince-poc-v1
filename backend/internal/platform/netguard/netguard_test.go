// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package netguard

import (
	"net"
	"strings"
	"testing"
)

func TestPublicIPClassifiesReservedRanges(t *testing.T) {
	cases := []struct {
		ip     string
		public bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},            // loopback
		{"::1", false},                  // loopback v6
		{"10.0.0.5", false},             // private
		{"192.168.1.10", false},         // private
		{"172.16.0.1", false},           // private
		{"169.254.169.254", false},      // link-local (cloud metadata)
		{"100.64.1.1", false},           // CGNAT (reserved)
		{"192.0.2.5", false},            // documentation (reserved)
		{"192.88.99.1", false},          // 6to4 relay anycast (deprecated)
		{"0.0.0.0", false},              // unspecified
		{"0.1.2.3", false},              // 0.0.0.0/8 "this network" (routes to loopback)
		{"2001:db8::1", false},          // documentation v6 (reserved)
		{"3fff::1", false},              // documentation v6 (RFC 9637)
		{"64:ff9b::a9fe:a9fe", false},   // NAT64 → 169.254.169.254 (metadata)
		{"64:ff9b:1::a9fe:a9fe", false}, // local-use NAT64 → the same metadata address
		{"2002:7f00:1::1", false},       // 6to4 embedding 127.0.0.1
		{"2002:0a00:5::1", false},       // 6to4 embedding 10.0.0.5
		{"2001::1", false},              // Teredo, inside the 2001::/23 protocol assignments
		{"2001:20::1", false},           // ORCHIDv2, same block
		{"100::1", false},               // discard-only
		{"100:0:0:1::1", false},         // dummy prefix
		{"5f00::1", false},              // SRv6 SIDs
		{"fec0::1", false},              // deprecated site-local
		{"::0.1.2.3", false},            // IPv4-compatible ::/96
		{"::ffff:0:a9fe:a9fe", false},   // IPv4-translated → 169.254.169.254, the third 4-in-6 spelling
		{"::ffff:8.8.8.8", true},        // IPv4-mapped public — still reachable
		{"::ffff:127.0.0.1", false},     // IPv4-mapped loopback — the same host, another spelling
		{"2606:4700::1111", true},       // an ordinary public v6 neighbour of the blocks above
		{"2001:200::1", true},           // the first RIR allocation, one bit past 2001::/23
		{"2001:4860:4860::8888", true},  // ordinary public resolver, above the blanket
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := PublicIP(ip); got != c.public {
			t.Errorf("PublicIP(%s) = %v, want %v", c.ip, got, c.public)
		}
	}
}

func TestRefusePrivateBlocksNonPublicAndAllowsPublic(t *testing.T) {
	if err := RefusePrivate("tcp", "8.8.8.8:993", nil); err != nil {
		t.Errorf("public address should dial: %v", err)
	}
	for _, addr := range []string{
		"169.254.169.254:80",         // cloud metadata
		"127.0.0.1:993",              // loopback
		"10.1.2.3:993",               // private
		"[64:ff9b::a9fe:a9fe]:993",   // NAT64 → metadata
		"[64:ff9b:1::a9fe:a9fe]:993", // local-use NAT64 → the same
		"[2002:7f00:1::1]:993",       // 6to4 → 127.0.0.1
	} {
		if err := RefusePrivate("tcp", addr, nil); err == nil {
			t.Errorf("RefusePrivate(%q) should refuse, got nil", addr)
		}
	}
}

func TestRefusePrivateRejectsNonLiteralAddress(t *testing.T) {
	// Control always receives a resolved ip:port; a hostname here means the
	// caller wired it wrong — refuse loudly rather than dial blind.
	err := RefusePrivate("tcp", "imap.example.com:993", nil)
	if err == nil || !strings.Contains(err.Error(), "not a literal IP") {
		t.Errorf("want non-literal refusal, got %v", err)
	}
}
