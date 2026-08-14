// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The occupied-stage refusal has to stay actionable when the stage holds
// more deals than the message names: the count is exact, the list is
// capped, and the sentence says so rather than reading as the whole truth.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func blockingDealsFixture(n int) []BlockingDeal {
	deals := make([]BlockingDeal, 0, n)
	for i := range n {
		deals = append(deals, BlockingDeal{ID: ids.New[ids.DealKind](), Name: fmt.Sprintf("Deal %d", i+1)})
	}
	return deals
}

func TestTheOccupiedRefusalNamesEveryDealItCounted(t *testing.T) {
	named := blockingDealsFixture(3)
	code, message := (&StageOccupiedError{Count: len(named), Deals: named}).MessageFault()

	if code != "stage_occupied" {
		t.Fatalf("the refusal carries code %q, which no surface is mapping", code)
	}
	for _, d := range named {
		if !strings.Contains(message, d.Name) {
			t.Fatalf("the refusal %q leaves out %q, which the admin has to move", message, d.Name)
		}
	}
	if strings.Contains(message, "more") {
		t.Fatalf("the refusal %q claims deals it did not name, but it named all of them", message)
	}
}

func TestTheOccupiedRefusalSaysHowManyItLeftUnnamed(t *testing.T) {
	named := blockingDealsFixture(namedBlockingDeals)
	_, message := (&StageOccupiedError{Count: namedBlockingDeals + 7, Deals: named}).MessageFault()

	if !strings.Contains(message, fmt.Sprintf("%d deal(s)", namedBlockingDeals+7)) {
		t.Fatalf("the refusal %q does not lead with the true count", message)
	}
	if !strings.Contains(message, "(and 7 more)") {
		t.Fatalf("the refusal %q reads as the whole list while it named only %d of %d",
			message, namedBlockingDeals, namedBlockingDeals+7)
	}
}

// The verdict lives on the error, not in a per-module HTTP mapper: that
// is what lets one choke point render both refusals, and what the
// seam-coverage gate demands of any 422 a module can raise, since a
// surface reaching the store without that mapper would otherwise report
// a governed refusal as an internal fault.
func TestBothRemovalRefusalsCarryTheirOwnVerdict(t *testing.T) {
	for _, err := range []error{
		&StageOccupiedError{Count: 1, Deals: blockingDealsFixture(1)},
		&TerminalStageError{Semantic: string(SemanticWon)},
	} {
		fault, ok := err.(apperrors.MessageFault)
		if !ok {
			t.Fatalf("%T does not carry its own verdict", err)
		}
		code, message := fault.MessageFault()
		if code == "" || message == "" {
			t.Fatalf("%T answers code=%q message=%q — a surface has nothing to say", err, code, message)
		}
	}
}

func TestTheTerminalRefusalNamesTheSemanticItRefused(t *testing.T) {
	for _, semantic := range []string{string(SemanticWon), string(SemanticLost)} {
		code, message := (&TerminalStageError{Semantic: semantic}).MessageFault()
		if code != "terminal_stage_not_removable" {
			t.Fatalf("the refusal carries code %q, which no surface is mapping", code)
		}
		if !strings.Contains(message, semantic) {
			t.Fatalf("the refusal %q does not say which stage kind it refused", message)
		}
	}
}
