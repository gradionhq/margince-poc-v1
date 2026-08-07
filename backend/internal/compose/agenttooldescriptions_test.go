// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The description gate, derived from the composed registry rather than from a
// list of tools. NewRegistry builds the whole surface with no database — the
// same construction every other sweep over this surface uses — so a tool added,
// renamed or withdrawn reaches these assertions the commit it reaches the
// product, and there is nothing here to keep current by hand.
//
// Each rule is a named function over one spec, and each is proved against a
// spec that BREAKS it as well as against the surface that keeps it. A gate only
// ever run over a clean tree is a gate nobody has seen fail, and one written
// against a defect it cannot actually detect looks exactly like a passing one.

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// minimumDescriptionRunes is the length below which a description cannot have
// answered the questions one is for. It is a floor against an entry filled in
// to satisfy the registry, not a style rule: the shortest written entry on this
// surface is several times this.
const minimumDescriptionRunes = 60

// renderableDescriptionDefect names what stops a description being text a
// client can render, or "" when there is none. Registration already refuses an
// empty one, so this is the check registration cannot make: that what was
// written says something, in characters that survive the wire.
func renderableDescriptionDefect(spec mcp.ToolSpec) string {
	if strings.TrimSpace(spec.Description) != spec.Description {
		return "the description is framed by whitespace, which a client renders verbatim"
	}
	if n := len([]rune(spec.Description)); n < minimumDescriptionRunes {
		return "the description is too short to have said what the tool is for, what it does not do, or what to keep from it"
	}
	for _, r := range spec.Description {
		if r < 0x20 || r == 0x7f {
			// Go and JSON string quoting agree on every character but these,
			// and the description is spliced into a JSON response.
			return "the description carries a control character, which Go would quote in a form JSON rejects"
		}
	}
	return ""
}

// governanceOnlyPhrases are the words the GENERATED description was made of.
// A written description may say that a person approves a call — that is a real
// limit a caller plans around — but reaching for these is the generated line
// coming back, and governance already reaches the client appended to the
// written half.
var governanceOnlyPhrases = []string{"Autonomy:", "passport scope", "Maps to ", "auto_execute", "confirmation_required"}

// restatedGovernance names the governance phrase a written description repeats,
// or "" when it states purpose instead. This is the defect the whole change is
// about: for the surface's whole life a description explained how a tool was
// POLICED to a model whose question was which tool to call.
func restatedGovernance(spec mcp.ToolSpec) string {
	for _, phrase := range governanceOnlyPhrases {
		if strings.Contains(spec.Description, phrase) {
			return phrase
		}
	}
	return ""
}

func TestEveryRegisteredToolIsDescribedInTextAClientCanRender(t *testing.T) {
	for _, spec := range NewRegistry(nil, SendPath{}).Specs() {
		if defect := renderableDescriptionDefect(spec); defect != "" {
			t.Errorf("%s: %s", spec.Name, defect)
		}
	}
}

func TestNoWrittenDescriptionRestatesGovernanceInsteadOfPurpose(t *testing.T) {
	for _, spec := range NewRegistry(nil, SendPath{}).Specs() {
		if phrase := restatedGovernance(spec); phrase != "" {
			t.Errorf("%s: the written description carries %q, which the governance clause already states",
				spec.Name, phrase)
		}
	}
}

// A description that could belong to two tools has not told a model how to
// choose between them, which is the entire job. Exact equality is the only
// version of this a test can hold honestly — near-duplication is an editorial
// judgement — but it is the version that catches a copy-paste.
func TestNoTwoToolsShareADescription(t *testing.T) {
	if first, second, shared := duplicateDescription(NewRegistry(nil, SendPath{}).Specs()); shared {
		t.Errorf("%s and %s are described identically, so nothing in the listing tells them apart", first, second)
	}
}

func duplicateDescription(specs []mcp.ToolSpec) (first, second string, shared bool) {
	owner := make(map[string]string, len(specs))
	for _, spec := range specs {
		if seen, dup := owner[spec.Description]; dup {
			return seen, spec.Name, true
		}
		owner[spec.Description] = spec.Name
	}
	return "", "", false
}

// The two rules above are only worth having if they fire. Each case here is a
// spec carrying exactly one defect, so a rule that silently stopped detecting
// its own subject fails here rather than passing over a clean tree forever.
func TestTheDescriptionRulesFailOnTheDefectsTheyDescribe(t *testing.T) {
	written := "Find people and organizations by name when you do not yet know which record you mean."
	for _, tc := range []struct {
		name string
		spec mcp.ToolSpec
	}{
		{"framed by whitespace", mcp.ToolSpec{Name: "t", Description: " " + written}},
		{"too short to have said anything", mcp.ToolSpec{Name: "t", Description: "Searches."}},
		{"carrying a control character", mcp.ToolSpec{Name: "t", Description: written + "\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if renderableDescriptionDefect(tc.spec) == "" {
				t.Errorf("a description %s was reported as renderable", tc.name)
			}
		})
	}
	if renderableDescriptionDefect(mcp.ToolSpec{Name: "t", Description: written}) != "" {
		t.Error("a written description was reported as defective, so the rule refuses what the surface ships")
	}

	for _, phrase := range governanceOnlyPhrases {
		if restatedGovernance(mcp.ToolSpec{Name: "t", Description: written + " " + phrase + " read."}) == "" {
			t.Errorf("a description restating %q was not reported", phrase)
		}
	}
	if restatedGovernance(mcp.ToolSpec{Name: "t", Description: written}) != "" {
		t.Error("a purpose-stating description was reported as restating governance")
	}

	if _, _, shared := duplicateDescription([]mcp.ToolSpec{
		{Name: "a", Description: written}, {Name: "b", Description: written},
	}); !shared {
		t.Error("two tools described identically were not reported")
	}
}

// The two surfaces that serve this text must serve the SAME text. The REST
// endpoint's own contract says it "mirrors exactly what an MCP client sees from
// tools/list"; a second rendering here is how that promise quietly stops being
// true.
func TestTheOperatorConsoleServesTheTextAnMCPClientIsServed(t *testing.T) {
	specs := NewRegistry(nil, SendPath{}).Specs()
	served := agentToolsFromSpecs(specs)
	if len(served) != len(specs) {
		t.Fatalf("the console serves %d tools, the registry holds %d", len(served), len(specs))
	}
	for i, spec := range specs {
		tool := served[i]
		if tool.Description == nil {
			t.Errorf("%s: the console serves no description", spec.Name)
			continue
		}
		if want := agents.DescribeForClient(spec); *tool.Description != want {
			t.Errorf("%s: the console serves %q, an MCP client is served %q", spec.Name, *tool.Description, want)
		}
		if tool.Title == nil || *tool.Title != spec.Title {
			t.Errorf("%s: the console does not serve the tool's written display title", spec.Name)
		}
	}
}

// listingShareOfTheWindow is the most of the runner's prompt ceiling the tool
// listing may take. The listing lives in the system prompt, which elision never
// touches — only the transcript gives way — so a catalog that grew past this
// would not overflow, it would quietly leave the run less and less room for the
// observations it is reasoning over.
//
// Half is generous next to where the surface sits today and still leaves the
// whole other half for the goal and the transcript. It is a ceiling on growth,
// not a target.
const listingShareOfTheWindow = 2

// The cost this change introduced: thirty written descriptions go into every
// Surface-B prompt, and nothing else in the loop notices if they grow. The
// tokens are estimated the way the window itself estimates them, so this
// measures the same thing the ceiling is enforced against rather than a second
// idea of a token.
func TestTheToolListingLeavesTheRunRoomInTheWindow(t *testing.T) {
	bytes := 0
	for _, spec := range NewRegistry(nil, SendPath{}).Specs() {
		// What systemPrompt prints per tool: the name, the written description
		// and the input schema, plus the few characters of framing around them.
		bytes += len(spec.Name) + len(spec.Description) + len(spec.InputSchema) + len("-  — \n  input schema: \n")
	}
	tokens := bytes / 4
	if budget := runner.PromptTokenCeiling / listingShareOfTheWindow; tokens > budget {
		t.Errorf("the tool listing is ~%d tokens of a %d-token window, past the %d it may take — "+
			"the listing is never elided, so what grows here comes out of the run's own observations",
			tokens, runner.PromptTokenCeiling, budget)
	}
}
