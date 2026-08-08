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
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
// or "" when it states purpose instead. A description that explains how a tool
// is POLICED answers a question no model selecting a tool is asking, and the
// governance clause already answers it.
func restatedGovernance(spec mcp.ToolSpec) string {
	for _, phrase := range governanceOnlyPhrases {
		if strings.Contains(spec.Description, phrase) {
			return phrase
		}
	}
	return ""
}

func TestEveryRegisteredToolIsDescribedInTextAClientCanRender(t *testing.T) {
	for _, spec := range servedSurface(t).Specs() {
		if defect := renderableDescriptionDefect(spec); defect != "" {
			t.Errorf("%s: %s", spec.Name, defect)
		}
	}
}

func TestNoWrittenDescriptionRestatesGovernanceInsteadOfPurpose(t *testing.T) {
	for _, spec := range servedSurface(t).Specs() {
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
	if first, second, shared := duplicateDescription(servedSurface(t).Specs()); shared {
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
		// \x01 rather than a newline: TrimSpace catches trailing whitespace one
		// branch earlier, so a newline would have proved the framing rule twice
		// and the rune loop never once.
		{"carrying a control character", mcp.ToolSpec{Name: "t", Description: "Find people" + "\x01" + " by name, when you do not yet know which record you mean."}},
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
	registry := NewRegistry(nil, SendPath{})
	specs := registry.Specs()
	listed := toolsListDescriptions(t, registry)
	served := agentToolsFromSpecs(specs)
	if len(served) != len(specs) {
		t.Fatalf("the console serves %d tools, the registry holds %d", len(served), len(specs))
	}
	for i, spec := range specs {
		tool := served[i]
		if want := listed[spec.Name]; tool.Description != want {
			t.Errorf("%s: the console serves %q, tools/list serves %q", spec.Name, tool.Description, want)
		}
		if tool.Title != spec.Title {
			t.Errorf("%s: the console does not serve the tool's written display title", spec.Name)
		}
	}
}

// The tool listing may take at most listingBudgetNumerator/listingBudgetDenominator
// of the runner's prompt ceiling. The listing lives in the system prompt, which
// elision never touches — only the transcript gives way — so a catalog that grew
// past this would not overflow, it would quietly leave the run less and less
// room for the observations it is reasoning over.
//
// It was 1/2, and the comment there said half was "generous next to where the
// surface sits today". That stopped being true at 33 tools: the catalog reached
// ~11,900 tokens against a 12,000 bound, and the two tools after it did not fit.
//
// Raised to 5/8 rather than answered by trimming copy, and the choice is worth
// stating because the cheaper option is the wrong one. What is in these
// descriptions is the ONE thing measured to move tool selection — A2 took
// gemini from 0.80 to 0.87 by making tools say what they are for, and took one
// restraint scenario from 0/3 to 3/3 on a single sentence. Cutting that to fit a
// fraction chosen when the catalog was smaller would trade a measured gain for
// an unmeasured one. 5/8 still leaves 9,000 tokens for the goal and the
// transcript, which is a working run.
//
// This is a ceiling on growth and it is closer than it looks: at 35 tools the
// listing is ~12,700 of 15,000. The listing is O(catalog) and the next few tools
// will reach this bound too. The real answer is a listing that does not print
// every tool's full copy into every run — filed as an issue rather than guessed
// at here, because it is a change to what a run is given, not to a test.
const (
	listingBudgetNumerator   = 5
	listingBudgetDenominator = 8
)

// Every written description rides in every Surface-B prompt, and nothing in
// the loop notices if they grow. The listing is measured by rendering it — the
// runner's own renderer, not a second spelling of its format — and its tokens
// are estimated by the ~4-bytes rule the window itself estimates with, so this
// holds the real string against the real ceiling.
//
// It measures the CORE catalog — see servedSurface for why it stops there, and
// for the per-tool bound Register applies to extension tools in its place. The
// headroom this reports is therefore the core surface's, not an installation's:
// a tree that adds units has to do that arithmetic itself, which is what the
// bound at the door makes survivable.
func TestTheToolListingLeavesTheRunRoomInTheWindow(t *testing.T) {
	tokens := len(runner.ToolListing(servedSurface(t).Specs())) / 4
	if budget := runner.PromptTokenCeiling * listingBudgetNumerator / listingBudgetDenominator; tokens > budget {
		t.Errorf("the tool listing is ~%d tokens of a %d-token window, past the %d it may take — "+
			"the listing is never elided, so what grows here comes out of the run's own observations",
			tokens, runner.PromptTokenCeiling, budget)
	}
}

// servedSurface is the core tool surface these rules are held against.
//
// It is the CORE catalog and not the composed one. Reaching the composed set
// from here would mean importing the composition module, which only a role main
// may do (TestCompositionWiredOnlyFromCmd) — and that boundary is worth more
// than the coverage would be. An extension tool is not unchecked in its place:
// Register applies the same per-tool bounds to every tool that comes through
// it, core and extension alike, so no single unit can blow the listing on its
// own. What this leaves unmeasured is a tree that adds MANY units at once,
// which is an installation's own arithmetic to do.
func servedSurface(t *testing.T) *agents.Registry {
	t.Helper()
	return NewRegistry(nil, SendPath{})
}

// toolsListDescriptions is what an MCP client is actually served, read off a
// real request to the real hosted handler — not off the helper the console also
// calls. That distinction is the whole point of the test that uses it:
// comparing two callers of one function proves they agree with each other and
// nothing about what reaches the wire.
//
// It runs over a real server rather than a ResponseRecorder because the handler
// extends its own write deadline per request, which a recorder cannot do — a
// recorder answers 500 before the dispatcher is ever reached.
func toolsListDescriptions(t *testing.T, registry *agents.Registry) map[string]string {
	t.Helper()
	// Every scope, because tools/list is scope-filtered: a caller holding less
	// would be served a shorter listing, and the comparison would silently skip
	// whatever it could not see.
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:descriptions", OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(principal.ScopeRead, principal.ScopeDraft,
			principal.ScopeWrite, principal.ScopeSend, principal.ScopeEnrich),
	})
	handler := agents.NewHTTPHandler(registry,
		func(*http.Request) (context.Context, error) { return ctx, nil },
		nil, "margince-crm", "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("building the tools/list request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// A plain JSON answer, not the SSE framing the handler also offers: this
	// test is about the text in the response, and the stream would wrap it.
	req.Header.Set("Accept", "application/json")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("calling tools/list: %v", err)
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Errorf("closing the tools/list response: %v", err)
		}
	}()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading the tools/list response: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("tools/list answered %d: %s", res.StatusCode, body)
	}
	var decoded struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decoding tools/list: %v\n%s", err, body)
	}
	if len(decoded.Result.Tools) == 0 {
		t.Fatal("tools/list advertised nothing, so there is no served text to compare against")
	}
	out := make(map[string]string, len(decoded.Result.Tools))
	for _, tool := range decoded.Result.Tools {
		out[tool.Name] = tool.Description
	}
	return out
}
