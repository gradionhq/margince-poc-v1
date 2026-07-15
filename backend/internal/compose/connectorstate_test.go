// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestConnectStateRoundTrips(t *testing.T) {
	s := newStateSigner([]byte("a-32-byte-or-longer-signing-key!!"))
	ws, user := ids.MustParse("11111111-1111-1111-1111-111111111111"), ids.MustParse("22222222-2222-2222-2222-222222222222")
	now := time.Unix(1_700_000_000, 0)

	token := s.sign(connectState{Workspace: ws, User: user, Provider: "gmail"}, now.Add(10*time.Minute))
	got, err := s.verify(token, now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Workspace != ws || got.User != user || got.Provider != "gmail" {
		t.Errorf("round-trip = %+v, want ws/user/gmail", got)
	}
}

func TestConnectStateRejectsTamper(t *testing.T) {
	s := newStateSigner([]byte("a-32-byte-or-longer-signing-key!!"))
	now := time.Unix(1_700_000_000, 0)
	token := s.sign(connectState{Workspace: ids.MustParse("11111111-1111-1111-1111-111111111111"), User: ids.MustParse("22222222-2222-2222-2222-222222222222"), Provider: "gmail"}, now.Add(time.Minute))

	// Flip a character in the payload half — the HMAC must no longer match.
	payload, mac, _ := strings.Cut(token, ".")
	tampered := payload[:len(payload)-1] + flip(payload[len(payload)-1:]) + "." + mac
	if _, err := s.verify(tampered, now); err == nil {
		t.Fatal("verify accepted a tampered state")
	}
}

func TestConnectStateRejectsWrongKey(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	token := newStateSigner([]byte("a-32-byte-or-longer-signing-key!!")).
		sign(connectState{Workspace: ids.MustParse("11111111-1111-1111-1111-111111111111"), User: ids.MustParse("22222222-2222-2222-2222-222222222222"), Provider: "gmail"}, now.Add(time.Minute))
	if _, err := newStateSigner([]byte("a-DIFFERENT-32-byte-signing-key!!!")).verify(token, now); err == nil {
		t.Fatal("verify accepted a state signed with a different key")
	}
}

func TestConnectStateRejectsExpired(t *testing.T) {
	s := newStateSigner([]byte("a-32-byte-or-longer-signing-key!!"))
	now := time.Unix(1_700_000_000, 0)
	token := s.sign(connectState{Workspace: ids.MustParse("11111111-1111-1111-1111-111111111111"), User: ids.MustParse("22222222-2222-2222-2222-222222222222"), Provider: "gmail"}, now.Add(time.Minute))
	if _, err := s.verify(token, now.Add(2*time.Minute)); err == nil {
		t.Fatal("verify accepted an expired state")
	}
}

func flip(s string) string {
	if s == "A" {
		return "B"
	}
	return "A"
}
