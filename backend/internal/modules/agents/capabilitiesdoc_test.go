// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// capabilitiesRegistry is a surface with one tool of each tier and two scopes,
// so a test can tell "grouped by tier" apart from "listed in registration
// order", and "filtered by scope" apart from "listed whole".
func capabilitiesRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry(nil, nil)
	for _, spec := range []mcp.ToolSpec{
		{Name: "read_record", Tier: mcp.TierAutoExecute, RequiredScope: principal.ScopeRead},
		{Name: "search_records", Tier: mcp.TierAutoExecute, RequiredScope: principal.ScopeRead},
		{Name: "archive_record", Tier: mcp.TierConfirmationRequired, RequiredScope: principal.ScopeWrite},
		{Name: "advance_deal", Tier: mcp.TierDynamic, RequiredScope: principal.ScopeWrite},
	} {
		spec.Title, spec.Version, spec.Description = spec.Name, testToolVersion, describedForRegistration
		spec.InputSchema = json.RawMessage(`{"type":"object"}`)
		if spec.Tier == mcp.TierDynamic {
			spec.TierResolver = func(mcp.TierResolverInput) mcp.RiskTier { return mcp.TierConfirmationRequired }
		}
		r.Register(&fakeTool{spec: spec})
	}
	return r
}

func readCapabilities(t *testing.T, r *Registry, scopes ...principal.Scope) capabilitiesDoc {
	t.Helper()
	contents, err := NewCapabilitiesResource(r).ReadResource(agentHolding(scopes...), CapabilitiesURI)
	if err != nil {
		t.Fatalf("reading capabilities: %v", err)
	}
	var doc capabilitiesDoc
	if err := json.Unmarshal([]byte(contents.Text), &doc); err != nil {
		t.Fatalf("capabilities served text no client can parse: %v", err)
	}
	return doc
}

// THE property that makes this document safe to publish: it is DERIVED, so it
// can neither understate nor overstate the surface. A summary kept beside the
// registry drifts the first time a tool is added, and a capabilities document
// that is wrong is worse than none — a client believes it.
//
// Asserted as a partition rather than a count: every offered verb appears
// exactly once across the three groups, and nothing appears that is not offered.
func TestCapabilitiesPartitionsExactlyWhatTheCallerIsOffered(t *testing.T) {
	r := capabilitiesRegistry(t)
	ctx := agentHolding(principal.ScopeRead, principal.ScopeWrite)
	doc := readCapabilities(t, r, principal.ScopeRead, principal.ScopeWrite)

	listed := slices.Concat(doc.Verbs.ExecuteDirectly, doc.Verbs.StageForApproval, doc.Verbs.DecidedPerCall)
	offered := []string{}
	for _, spec := range r.Offered(ctx) {
		offered = append(offered, spec.Name)
	}
	slices.Sort(listed)
	slices.Sort(offered)
	if !slices.Equal(listed, offered) {
		t.Errorf("capabilities names %v; the caller is offered %v — a document that disagrees with the "+
			"registry is worse than no document, because a client believes it", listed, offered)
	}
	if doc.Verbs.Offered != len(offered) {
		t.Errorf("capabilities counts %d verbs and lists %d", doc.Verbs.Offered, len(listed))
	}
}

// Each verb lands in the group that says what CALLING it does, which is the
// split a caller plans against.
func TestCapabilitiesGroupsAVerbByWhatCallingItDoes(t *testing.T) {
	doc := readCapabilities(t, capabilitiesRegistry(t), principal.ScopeRead, principal.ScopeWrite)

	for _, want := range []struct {
		verb  string
		group []string
		why   string
	}{
		{"read_record", doc.Verbs.ExecuteDirectly, "an auto-execute verb runs under the passport's own authority"},
		{"archive_record", doc.Verbs.StageForApproval, "a confirm-first verb stages an approval a human decides"},
		{"advance_deal", doc.Verbs.DecidedPerCall, "a dynamic verb's tier depends on its arguments"},
	} {
		if !slices.Contains(want.group, want.verb) {
			t.Errorf("%s is not in the group that says %s: %+v", want.verb, want.why, doc.Verbs)
		}
	}
}

// The document is per CALLER. A read-only passport is told about the verbs it
// can call and not about the ones it cannot — the same rule the tool listing
// follows, and the reason this resource carries a RequiredScope at all: a
// document that described the whole surface to a narrow passport would be the
// disclosure channel the scope filter exists to close.
func TestCapabilitiesDescribesOnlyWhatThisPassportHolds(t *testing.T) {
	doc := readCapabilities(t, capabilitiesRegistry(t), principal.ScopeRead)

	listed := slices.Concat(doc.Verbs.ExecuteDirectly, doc.Verbs.StageForApproval, doc.Verbs.DecidedPerCall)
	for _, hidden := range []string{"archive_record", "advance_deal"} {
		if slices.Contains(listed, hidden) {
			t.Errorf("a read-only passport was told it may call %s, which its scopes do not admit: %v",
				hidden, listed)
		}
	}
	if !slices.Contains(doc.Verbs.ExecuteDirectly, "read_record") {
		t.Errorf("a read-only passport was not told about read_record: %v", listed)
	}
	if want := []string{"read"}; !slices.Equal(doc.Governance.ScopesHeld, want) {
		t.Errorf("scopes_held = %v, want %v — the document reports what this caller holds, "+
			"not what the product defines", doc.Governance.ScopesHeld, want)
	}
}

// A host's own confirmation dialog is not one of this surface's approvals, and
// conflating them is how an action nobody here approved gets executed. The
// document says so in its own field rather than burying it in prose.
func TestCapabilitiesSaysAHostConfirmationIsNotAnApproval(t *testing.T) {
	doc := readCapabilities(t, capabilitiesRegistry(t), principal.ScopeRead)
	if doc.Governance.HostConfirmation == "" {
		t.Fatal("the document does not distinguish a host confirmation from a staged approval")
	}
}

// An unknown URI is a not-found, matching every other read on this surface — a
// provider that answered something else would make the fan-out's first-match
// walk resolve to the wrong thing.
func TestCapabilitiesRefusesAURIItDoesNotPublish(t *testing.T) {
	_, err := NewCapabilitiesResource(capabilitiesRegistry(t)).
		ReadResource(agentHolding(principal.ScopeRead), "margince://schema/query")
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("reading another provider's URI answered %v, want ErrNotFound", err)
	}
}
