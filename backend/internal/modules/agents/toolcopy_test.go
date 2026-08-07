// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// The renderer joins the parts a reader has to receive in order — the outcome
// first — and drops the ones an entry legitimately has nothing to say about,
// rather than leaving the gaps as double spaces a client renders verbatim.
func TestToolCopyRendersTheWrittenPartsInOrder(t *testing.T) {
	full := toolCopy{
		Purpose: "Does the thing.",
		Limits:  "Not the other thing.",
		Instead: "Use the neighbour for that.",
		Retain:  "Keep the id.",
	}
	want := "Does the thing. Not the other thing. Use the neighbour for that. Keep the id."
	if got := full.render(); got != want {
		t.Errorf("render() = %q, want %q", got, want)
	}

	// A tool with no confusable neighbour and nothing to carry forward says so
	// by omission. The result must still be exactly its purpose: an entry that
	// rendered as "Does the thing.   " would put trailing whitespace on the
	// wire for every such tool.
	purposeOnly := toolCopy{Purpose: "Does the thing."}
	if got := purposeOnly.render(); got != "Does the thing." {
		t.Errorf("purpose-only render() = %q, want the purpose alone", got)
	}

	// Whitespace-only parts are the same claim as absent ones. Left in, they
	// would each contribute a separator to a description that says nothing more.
	blank := toolCopy{Purpose: "Does the thing.", Limits: "   ", Retain: "\t"}
	if got := blank.render(); got != "Does the thing." {
		t.Errorf("blank-part render() = %q, want the purpose alone", got)
	}
}

// The written text is what a model selects on; the governance clause is what
// happens once it has. Both must be present, and the written half must come
// first — the surface spent its whole life with the order the other way round,
// which is what this PR changed.
func TestDescribeForClientLeadsWithTheWrittenTextAndAppendsGovernance(t *testing.T) {
	spec := mcp.ToolSpec{
		Name:          "send_email",
		Description:   "Put a mail on the wire.",
		RequiredScope: principal.ScopeSend,
		Tier:          mcp.TierConfirmationRequired,
		// A crm.yaml operation family is developer documentation. A model
		// cannot call an endpoint, so naming one spends the description's
		// opening on something it can never act on.
		OpenAPIOp: "sendEmail",
	}
	got := DescribeForClient(spec)
	if !strings.HasPrefix(got, "Put a mail on the wire.") {
		t.Errorf("description = %q, want it to open with the written text", got)
	}
	if !strings.Contains(got, "a person approves every call before it runs") {
		t.Errorf("description = %q, want the confirm-first tier stated", got)
	}
	if !strings.Contains(got, `"send"`) {
		t.Errorf("description = %q, want the required scope stated", got)
	}
	if strings.Contains(got, "sendEmail") {
		t.Errorf("description = %q, want the crm.yaml operation left out of the model-facing text", got)
	}
}

// The dynamic tier is the one a single sentence can get wrong: a deal move is
// auto-execute until the target stage closes the deal. A description that
// reported it as one or the other would be false half the time.
func TestDescribeForClientStatesEachTierDistinctly(t *testing.T) {
	rendered := map[mcp.RiskTier]string{}
	for _, tier := range []mcp.RiskTier{mcp.TierAutoExecute, mcp.TierConfirmationRequired, mcp.TierDynamic} {
		rendered[tier] = DescribeForClient(mcp.ToolSpec{
			Name: "t", Description: "Does the thing.", RequiredScope: principal.ScopeRead, Tier: tier,
		})
	}
	if rendered[mcp.TierAutoExecute] == rendered[mcp.TierDynamic] {
		t.Error("the dynamic tier reads exactly as auto-execute, so a closing move looks like it runs unattended")
	}
	if rendered[mcp.TierConfirmationRequired] == rendered[mcp.TierDynamic] {
		t.Error("the dynamic tier reads exactly as confirm-first, so an open→open move looks like it needs approval")
	}
}
