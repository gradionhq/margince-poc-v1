// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import (
	"context"
	"testing"
)

func noopDelivery(context.Context, Runtime, Delivery) error { return nil }

func TestSubscriptionValidateAcceptsADeclaredListener(t *testing.T) {
	s := Subscription{
		Name:   "withdraw_filing",
		Events: []string{"activity.archived", "ext_notes.note_added"},
		Handle: noopDelivery,
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("a well-formed subscription was refused: %v", err)
	}
}

func TestSubscriptionValidateRefusesADeclarationNothingCouldServe(t *testing.T) {
	for name, s := range map[string]Subscription{
		"a name that is not lower snake_case": {
			Name: "WithdrawFiling", Events: []string{"activity.archived"}, Handle: noopDelivery,
		},
		"no name": {Events: []string{"activity.archived"}, Handle: noopDelivery},
		// A consumer group that never delivers is worse than no group: it is
		// created, it holds a cursor, and it looks like a working subscription.
		"no events": {Name: "withdraw_filing", Handle: noopDelivery},
		"an empty event type": {
			Name: "withdraw_filing", Events: []string{""}, Handle: noopDelivery,
		},
		"the same event twice": {
			Name:   "withdraw_filing",
			Events: []string{"activity.archived", "activity.archived"},
			Handle: noopDelivery,
		},
		// Registering this would ack every delivery into nothing, which reads
		// exactly like a handler that decided to ignore them.
		"no handler": {Name: "withdraw_filing", Events: []string{"activity.archived"}},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("Subscription.Validate accepted %s", name)
		}
	}
}

// Validate says nothing about whether a type is ROUTABLE. It cannot: the
// catalog lives in the core, which this package may not import — so the check
// is the registration's, exactly as a job's tier refusal is the serving side's.
func TestSubscriptionValidateDoesNotJudgeEventTypes(t *testing.T) {
	s := Subscription{
		Name:   "listens_for_nothing_real",
		Events: []string{"invoice.created"},
		Handle: noopDelivery,
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate refused an unroutable type; that judgment belongs to registration: %v", err)
	}
}
