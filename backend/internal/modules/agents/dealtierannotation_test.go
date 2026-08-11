// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The contract's account of when advance_deal needs a human must be the
// resolver's account of it.
//
// The two drifted, and nothing noticed for a release. `x-mcp-tool` carries
// `tier_resolver` and `confirmation_required_when` for advance_deal, and NO CODE
// READS EITHER — they are prose in a machine-readable file, which is the most
// reliable way to end up with documentation that contradicts the build. The
// contract went on saying the tier came from the TARGET stage alone long after
// the resolver started reading both endpoints, so a reader implementing against
// the contract would have built an agent that expects a reopen to auto-execute.
//
// So the claim is derived from the resolver rather than compared to a fixture:
// ask advanceDealTier what a reopen actually costs, and hold the contract to
// saying the same thing. A future change to the rule fails here until the
// sentence that describes it moves with it.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// contractPath is the OpenAPI document this annotation lives in, from this
// package's directory.
const contractPath = "../../../api/crm.yaml"

// advanceDealAnnotation captures the x-mcp-tool line for advance_deal — the one
// that carries the tier claim.
var advanceDealAnnotation = regexp.MustCompile(`(?m)^.*x-mcp-tool:.*verb:\s*advance_deal.*$`)

func TestTheContractDescribesTheTierRuleTheResolverApplies(t *testing.T) {
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("reading the contract: %v", err)
	}
	annotation := advanceDealAnnotation.FindString(string(raw))
	if annotation == "" {
		t.Fatalf("no x-mcp-tool annotation for advance_deal in %s — the derivation lost its "+
			"subject, and a gate with no subject certifies nothing", contractPath)
	}

	// The resolver is the authority. If it charges a reopen — a move OUT of a
	// terminal stage into an open one — to the confirm-first tier, then the
	// source endpoint is part of the rule and the contract has to say so.
	// "won" spelled here rather than borrowed from a constant: the resolver
	// recognises exactly one semantic, `open`, and treats everything else as
	// terminal — so a constant for the won case would be this test's own, and a
	// test that supplies its own version of production proves nothing about it.
	// This is the string the pipeline config carries.
	reopen := mcp.TierResolverInput{
		SourceStageSemantic: "won",
		TargetStageSemantic: stageSemanticOpen,
	}
	if advanceDealTier(reopen) != mcp.TierConfirmationRequired {
		// Not a failure of the annotation: the rule itself changed, and this
		// gate is now describing a world that no longer exists.
		t.Fatalf("advanceDealTier no longer stages a reopen for approval — this gate encodes the "+
			"rule that it does (#610), so revisit it together with %s", contractPath)
	}
	if !strings.Contains(annotation, "source") {
		t.Errorf("the resolver stages a reopen (won → open) for approval, but the contract's "+
			"advance_deal annotation never mentions the SOURCE endpoint:\n\n%s\n\nA reader "+
			"implementing against the contract would build an agent that expects a reopen to "+
			"auto-execute. Nothing else reads these fields, so this gate is the only thing "+
			"holding them to the build.", strings.TrimSpace(annotation))
	}
}
