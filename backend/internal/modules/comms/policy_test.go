// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestMailboxRatePolicyPermitsUpToTheLimit(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	p := NewMailboxRatePolicy(2, time.Minute, func() time.Time { return now })
	d := Delivery{UserID: ids.New[ids.UserKind]()}
	for i := range 2 {
		if wait := p.Wait(context.Background(), d); wait != 0 {
			t.Errorf("send %d waited %v, want 0 (within the limit)", i+1, wait)
		}
	}
}

func TestMailboxRatePolicyDefersBeyondTheLimit(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	p := NewMailboxRatePolicy(2, time.Minute, func() time.Time { return now })
	d := Delivery{UserID: ids.New[ids.UserKind]()}
	p.Wait(context.Background(), d)
	p.Wait(context.Background(), d)
	if wait := p.Wait(context.Background(), d); wait <= 0 {
		t.Errorf("third send waited %v, want a positive deferral", wait)
	}
}

// The limit is per MAILBOX. Keying it on anything per-message would give every
// message its own window and pace nothing at all.
func TestMailboxRatePolicyIsPerMailboxNotPerMessage(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	p := NewMailboxRatePolicy(1, time.Minute, func() time.Time { return now })
	alice, bob := ids.New[ids.UserKind](), ids.New[ids.UserKind]()
	if wait := p.Wait(context.Background(), Delivery{UserID: alice, MessageID: "a@t"}); wait != 0 {
		t.Fatalf("alice's first send waited %v", wait)
	}
	if wait := p.Wait(context.Background(), Delivery{UserID: bob, MessageID: "b@t"}); wait != 0 {
		t.Errorf("bob waited %v because of alice's send; the key is not the mailbox", wait)
	}
	if wait := p.Wait(context.Background(), Delivery{UserID: alice, MessageID: "c@t"}); wait <= 0 {
		t.Error("alice's second send was permitted; a per-message key would do this")
	}
}

func TestPolicyNameIsRecordedSoAnOperatorKnowsWhatDeferred(t *testing.T) {
	p := NewMailboxRatePolicy(1, time.Minute, nil)
	if p.Name() == "" {
		t.Error("a policy with no name leaves an unexplained deferral on the row")
	}
}
