// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// `margince://capabilities` — what this installation lets THIS caller do,
// published so a client does not have to buy the tool listing to find out.
//
// The cost it answers is measured, not supposed. The listing is ~12,700 tokens
// across 37 tools, and 98% of that is input schemas (54%) and descriptions
// (44%) — the parts a client needs to CALL a tool, and exactly the parts it does
// not need to decide whether this server is worth calling at all. A fresh
// session asking "what is this?" pays for all of it.
//
// So this document carries the catalog's SHAPE and not its weight: the verbs by
// name, which of them execute and which stage for a human, and the scopes this
// passport actually holds. Names are cheap — every tool name on the surface is
// under a kilobyte together.
//
// It is DERIVED per caller, never a written summary kept beside the registry: a
// summary drifts the first time a tool is added and nothing notices, and a
// capabilities document that overstates the surface is worse than none, because
// a client believes it.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// CapabilitiesURI is the document's stable identity.
const CapabilitiesURI = "margince://capabilities"

// capabilitiesResourceName is the document's programmatic name.
//
// Deliberately NOT shared with the "capabilities" member of the initialize
// result in dispatch.go: that one is a protocol member of the handshake and
// this one names a document. They share a spelling and nothing else, and one
// constant across both would tie a resource's identity to a wire member that
// can move without it.
const capabilitiesResourceName = "capabilities"

// mimeApplicationJSON is the media type every JSON document on this surface is
// served as — one spelling, so the catalogue entry and the served contents can
// never disagree about what a client is about to parse.
const mimeApplicationJSON = "application/json"

// capabilitiesVersion identifies the document's SHAPE. A caller caching it needs
// to know when the shape changed; the verb lists move with the registry on their
// own schedule and are re-read either way.
const capabilitiesVersion = "1"

// CapabilitiesResource publishes the surface's own shape.
//
// It holds the registry because the answer is per caller: Offered applies the
// passport's scopes, so two callers legitimately get different documents. That
// is the same reason the query vocabulary is resolved per principal rather than
// composed once.
type CapabilitiesResource struct{ registry *Registry }

// NewCapabilitiesResource binds the document to the registry it describes.
func NewCapabilitiesResource(registry *Registry) CapabilitiesResource {
	return CapabilitiesResource{registry: registry}
}

// Resources advertises the one document this provider publishes.
func (CapabilitiesResource) Resources(context.Context) []mcp.Resource {
	return []mcp.Resource{{
		URI:   CapabilitiesURI,
		Name:  capabilitiesResourceName,
		Title: "What this installation can do",
		// ScopeRead, and the consequence is worth stating rather than
		// discovering: a passport holding only `write` cannot see this
		// document. That is the conservative reading and it is the right one
		// — the scope filter on the resource surface is fail-closed by
		// design, and "it would be convenient for discovery" is precisely the
		// argument that would turn it into the disclosure channel the
		// scope-filtered tool list is careful not to be.
		RequiredScope: principal.ScopeRead,
		MIMEType:      mimeApplicationJSON,
		// Says what it HOLDS. It does not tell a caller to read it first:
		// a description that orders a read is measured to draw reads from
		// runs that had no use for them, which is why the two tools that
		// used to do it stopped.
		Description: "The verbs this passport may call, which of them execute directly and which " +
			"stage for a human decision, and the scopes it holds. Names and governance only — " +
			"the input schemas live in tools/list.",
	}}
}

// ReadResource composes the document for this caller. An unknown URI answers
// ErrNotFound, matching every other read on this surface.
func (c CapabilitiesResource) ReadResource(ctx context.Context, uri string) (mcp.ResourceContents, error) {
	if uri != CapabilitiesURI {
		return mcp.ResourceContents{}, fmt.Errorf("agents: resource %q: %w", uri, apperrors.ErrNotFound)
	}
	body, err := json.Marshal(c.document(ctx))
	if err != nil {
		return mcp.ResourceContents{}, fmt.Errorf("agents: rendering the capabilities document: %w", err)
	}
	return mcp.ResourceContents{URI: uri, MIMEType: mimeApplicationJSON, Text: string(body)}, nil
}

var _ mcp.ResourceProvider = CapabilitiesResource{}

// capabilitiesDoc is the published shape.
type capabilitiesDoc struct {
	Version string `json:"version"`
	// Governance is first because it is the thing a host most often gets
	// wrong: a client's own "are you sure?" is not this surface's approval.
	Governance capabilitiesGovernance `json:"governance"`
	Verbs      capabilitiesVerbs      `json:"verbs"`
}

// capabilitiesGovernance is the tier model as it applies to THIS caller.
type capabilitiesGovernance struct {
	ScopesHeld []string `json:"scopes_held"`
	Model      string   `json:"model"`
	// HostConfirmation is called out on its own because conflating the two is
	// the failure that matters: a host that treats its own confirmation dialog
	// as satisfying a staged approval has executed an action no human here
	// approved.
	HostConfirmation string `json:"host_confirmation"`
}

// capabilitiesVerbs splits the offered surface by what a call DOES, which is
// the split a caller plans against — not by module, which is ours.
type capabilitiesVerbs struct {
	Offered              int      `json:"offered"`
	ExecuteDirectly      []string `json:"execute_directly"`
	StageForApproval     []string `json:"stage_for_approval"`
	DecidedPerCall       []string `json:"decided_per_call"`
	DecidedPerCallReason string   `json:"decided_per_call_reason"`
}

// The other documents this surface publishes are deliberately NOT listed here.
// resources/list already answers that and answers it cheaply — a handful of
// descriptors, unlike tools/list, which is the cost this document exists to
// spare a caller. Naming them here would also make this resource depend on the
// composite provider it is itself a member of, which is a cycle bought for
// nothing.

// document composes the answer for this caller.
func (c CapabilitiesResource) document(ctx context.Context) capabilitiesDoc {
	doc := capabilitiesDoc{
		Version: capabilitiesVersion,
		Governance: capabilitiesGovernance{
			ScopesHeld: scopesHeld(ctx),
			Model: "A call either executes under this passport's authority or stages an approval a " +
				"human decides. An agent can never exceed the human who granted the passport, and " +
				"every call is re-authorised at the moment it runs, so a revoked grant binds mid-session.",
			HostConfirmation: "A confirmation your own client shows the user is NOT one of these approvals. " +
				"A staged call is decided in Margince and returns its outcome here.",
		},
		Verbs: c.verbs(ctx),
	}
	return doc
}

// verbs groups what this caller is offered by tier.
func (c CapabilitiesResource) verbs(ctx context.Context) capabilitiesVerbs {
	out := capabilitiesVerbs{
		DecidedPerCallReason: "The tier depends on the arguments — the same verb can execute or stage " +
			"depending on what it is asked to do.",
	}
	for _, spec := range c.registry.Offered(ctx) {
		out.Offered++
		switch spec.Tier {
		case mcp.TierConfirmationRequired:
			out.StageForApproval = append(out.StageForApproval, spec.Name)
		case mcp.TierDynamic:
			out.DecidedPerCall = append(out.DecidedPerCall, spec.Name)
		case mcp.TierAutoExecute:
			out.ExecuteDirectly = append(out.ExecuteDirectly, spec.Name)
		}
	}
	sort.Strings(out.ExecuteDirectly)
	sort.Strings(out.StageForApproval)
	sort.Strings(out.DecidedPerCall)
	return out
}

// scopesHeld reports the passport's scopes, so the document says what this
// caller holds rather than what the product defines.
func scopesHeld(ctx context.Context) []string {
	p, ok := principal.Actor(ctx)
	if !ok {
		return nil
	}
	held := make([]string, 0, len(p.Scopes))
	for scope := range p.Scopes {
		held = append(held, string(scope))
	}
	sort.Strings(held)
	return held
}
