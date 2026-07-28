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
		p.Recorded(d)
	}
}

// Wait only PEEKS the limiter; only Recorded — standing for a message that
// actually reached the provider — spends a slot. A delivery that is asked
// and then deferred (by an earlier policy in the chain, or by a retry that
// ends in another deferral) must not have spent quota it never used: asking
// N times in a row, with no send ever recorded, must still permit every ask
// while the mailbox is under its limit.
func TestMailboxRatePolicyWaitAloneNeverConsumesQuota(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	p := NewMailboxRatePolicy(1, time.Minute, func() time.Time { return now })
	d := Delivery{UserID: ids.New[ids.UserKind]()}
	for i := range 5 {
		if wait := p.Wait(context.Background(), d); wait != 0 {
			t.Errorf("ask %d waited %v, want 0 — Wait alone must not consume a slot", i+1, wait)
		}
	}
}

func TestMailboxRatePolicyDefersBeyondTheLimit(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	p := NewMailboxRatePolicy(2, time.Minute, func() time.Time { return now })
	d := Delivery{UserID: ids.New[ids.UserKind]()}
	p.Recorded(d)
	p.Recorded(d)
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
	aliceFirst := Delivery{UserID: alice, MessageID: "a@t"}
	if wait := p.Wait(context.Background(), aliceFirst); wait != 0 {
		t.Fatalf("alice's first send waited %v", wait)
	}
	p.Recorded(aliceFirst)
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
