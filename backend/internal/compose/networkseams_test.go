// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The relationship-graph seam mappings, where a shape decision is made that a
// model reads.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/network"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// An uncovered deal answers empty ARRAYS, not nulls.
//
// "No stakeholder seats and nobody from our side" is the most useful answer this
// tool gives — it is the one that says the deal rests on nothing — and a model
// handed `null` reads it as "unknown" and hedges. Every sibling read on this
// surface normalizes; found by UAT on the one that did not.
func TestAnUncoveredDealAnswersEmptyArraysNotNulls(t *testing.T) {
	answer := toAgentCoverage(network.DealCoverage{DealID: ids.NewV7()}, nil)

	raw, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("marshalling the coverage answer: %v", err)
	}
	for _, member := range []string{`"stakeholders":[]`, `"our_side":[]`, `"risks":[]`} {
		if !strings.Contains(string(raw), member) {
			t.Errorf("payload = %s, want %s — a null reads to a model as \"unknown\", which is a "+
				"different claim from \"nobody\"", raw, member)
		}
	}
}
