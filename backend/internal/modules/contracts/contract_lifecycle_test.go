// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package contracts

import (
	"errors"
	"strings"
	"testing"
)

// The state machine has exactly two rules, and both exist to stop a record
// describing somebody's second thoughts rather than what happened.
func TestTerminalStatusesDoNotReopen(t *testing.T) {
	for _, from := range []string{StatusExpired, StatusCancelled, StatusSuperseded} {
		t.Run(from, func(t *testing.T) {
			err := refuseInvalidTransition(from, StatusActive)

			var transition *InvalidStatusTransitionError
			if !errors.As(err, &transition) {
				t.Fatalf("reviving a %s contract: err = %v, want InvalidStatusTransitionError", from, err)
			}
			if transition.From != from || transition.To != StatusActive {
				t.Errorf("refusal names %s→%s, want %s→%s",
					transition.From, transition.To, from, StatusActive)
			}
		})
	}
}

// Superseded is reached by renewing, which writes the successor pointer in the
// same transaction. Asserting it directly would leave the status and the
// pointer disagreeing, and the CHECK constraint would then refuse the write
// with a message about our schema rather than about the caller's intent.
func TestSupersededIsNotDirectlyAssertable(t *testing.T) {
	err := refuseInvalidTransition(StatusActive, StatusSuperseded)

	var transition *InvalidStatusTransitionError
	if !errors.As(err, &transition) {
		t.Fatalf("asserting superseded directly: err = %v, want InvalidStatusTransitionError", err)
	}
}

func TestOrdinaryTransitionsAreAllowed(t *testing.T) {
	allowed := []struct{ from, to string }{
		{StatusDraft, StatusActive},
		{StatusActive, StatusExpired},
		{StatusActive, StatusCancelled},
		{StatusDraft, StatusDraft},
		// A no-op on a terminal status is not a revival: nothing moves, so
		// there is nothing to refuse.
		{StatusCancelled, StatusCancelled},
	}
	for _, tc := range allowed {
		if err := refuseInvalidTransition(tc.from, tc.to); err != nil {
			t.Errorf("%s→%s: err = %v, want nil", tc.from, tc.to, err)
		}
	}
}

// A renewal chain stays single-headed. Renewing an already-superseded
// agreement would give one predecessor two successors, and the chain stops
// being readable back.
func TestRenewingASupersededContractIsRefused(t *testing.T) {
	if err := refuseRenewalOfTerminal(StatusSuperseded); err == nil {
		t.Fatal("renewing a superseded contract: err = nil, want a refusal")
	}
	if err := refuseRenewalOfTerminal(StatusActive); err != nil {
		t.Errorf("renewing an active contract: err = %v, want nil", err)
	}
}

// Every constraint a human can trip must answer with the field they should
// look at and a sentence about their agreement — never our constraint name.
func TestConstraintRefusalsNameTheFieldAndNotTheSchema(t *testing.T) {
	cases := map[string]string{
		"contract_value_pair":               "value_minor",
		"contract_fx_pair":                  "fx_rate_to_base",
		"contract_term_order":               "ends_on",
		"contract_cancellation_within_term": "cancellation_effective_on",
		"contract_cancellation_order":       "cancellation_effective_on",
		"contract_superseded_agrees":        "status",
	}
	for constraint, wantField := range cases {
		t.Run(constraint, func(t *testing.T) {
			var check *ContractCheckError
			if !errors.As(contractCheckError(constraint), &check) {
				t.Fatalf("%s did not map to a ContractCheckError", constraint)
			}
			if check.Field != wantField {
				t.Errorf("field = %q, want %q", check.Field, wantField)
			}
			if strings.Contains(check.Reason, constraint) {
				t.Errorf("reason leaks the constraint name: %q", check.Reason)
			}
			if check.Reason == "" {
				t.Error("reason is empty; a refusal must say what to fix")
			}
		})
	}
}

// An unmapped constraint still refuses without naming our schema.
func TestAnUnmappedConstraintStillHidesTheSchema(t *testing.T) {
	var check *ContractCheckError
	if !errors.As(contractCheckError("contract_some_future_rule"), &check) {
		t.Fatal("an unmapped constraint did not map to a ContractCheckError")
	}
	if strings.Contains(check.Reason, "contract_some_future_rule") {
		t.Errorf("reason leaks the constraint name: %q", check.Reason)
	}
}
