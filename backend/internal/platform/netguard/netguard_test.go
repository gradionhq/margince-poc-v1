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
		{"127.0.0.1", false},          // loopback
		{"::1", false},                // loopback v6
		{"10.0.0.5", false},           // private
		{"192.168.1.10", false},       // private
		{"172.16.0.1", false},         // private
		{"169.254.169.254", false},    // link-local (cloud metadata)
		{"100.64.1.1", false},         // CGNAT (reserved)
		{"192.0.2.5", false},          // documentation (reserved)
		{"0.0.0.0", false},            // unspecified
		{"0.1.2.3", false},            // 0.0.0.0/8 "this network" (routes to loopback)
		{"2001:db8::1", false},        // documentation v6 (reserved)
		{"64:ff9b::a9fe:a9fe", false}, // NAT64 → 169.254.169.254 (metadata)
		{"::0.1.2.3", false},          // IPv4-compatible ::/96
		{"::ffff:8.8.8.8", true},      // IPv4-mapped public — still reachable
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
		"169.254.169.254:80",       // cloud metadata
		"127.0.0.1:993",            // loopback
		"10.1.2.3:993",             // private
		"[64:ff9b::a9fe:a9fe]:993", // NAT64 → metadata
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
