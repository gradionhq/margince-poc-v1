// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The transport directory (GET /v1/channel-providers): which messaging
// transports this installation registered, and what to call them.
//
// It is the resolver for `ProviderRef` (ADR-0107/A158). A provider vocabulary is
// a DEPLOYMENT fact — what this binary composed, including any unit present
// under extensions/ — so the contract cannot enumerate it without asserting that
// the legal set is identical everywhere, which is false. The contract states the
// invariant and this operation resolves it, moving the typing from build time to
// a runtime capability document.
//
// It lives in compose for the same reason the extension inventory does, and one
// more besides: the composed set is the composition root's fact, and
// `channel_provider` carries no workspace_id, so a module could not read it
// without an unscoped pool it is not allowed to open.

import (
	"context"
	"net/http"
	"sort"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// channelProvidersHandlers serves the directory. It holds NO state for
// extensionsHandlers' reason: every value is process-level and already recorded
// at boot, so a field here would be a second copy that could go stale.
type channelProvidersHandlers struct{}

// ListChannelProviders (GET /v1/channel-providers). Any authenticated seat.
//
// Deliberately NOT admin-only, which is where it parts company with its closest
// precedent (GET /v1/extensions). That one enumerates the installation's
// internal surface — routes, jobs, unit versions — which is operator
// information. This one answers "what do I call the transport this message
// arrived on", and EVERY member's timeline needs it: gating it on admin would
// leave every other seat rendering raw provider ids.
//
// The authorization argument has two halves, and both have to hold.
//
// The first is label PROVENANCE: every `label` here, on both arrays, is derived
// or compiled in and never operator-configured (see channelproviderfacts.go). An
// administrator-typed name would make this a disclosure decision instead of a
// display one.
//
// The second is the SET. `capture_sources` publishes which units this binary
// composed and which external system each one captures from, to every seat and
// to any read-scope passport. That is deliberately unprivileged, on the same
// ground LoadChannelProviderDirectory already records for a unit's provider id: a
// member whose row arrived through a unit needs to know what to call it, and what
// is gated is the ACT rather than the NAME. The one thing it widens is that a
// CAPTURE-ONLY unit — no channel, possibly nothing the caller may read — becomes
// visible; that is fingerprinting, not access, and the alternative is the raw
// `ext:` form on a member's own capture-activity window, which they can already
// reach without a grant. Anything privileged about a unit (its version, routes,
// jobs, RBAC objects) stays on the admin-only /v1/extensions.
//
// Authentication itself is the chassis's, applied to every /v1 route before a
// handler runs — hence no gate call in the body, unlike ListExtensions.
func (channelProvidersHandlers) ListChannelProviders(w http.ResponseWriter, r *http.Request) {
	registered, sending := ComposedChannelProviders()
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ChannelProviderDirectory{
		Data:           publishedChannelProviders(registered, sending),
		CaptureSources: publishedCaptureSources(ComposedExtensions()),
	})
}

// publishedCaptureSources shapes the provenance ids a unit's records land under
// for the wire — the half of the directory a reader resolves a NON-transport
// provenance id against.
//
// It is served from the composed declaration set rather than from a table, which
// is the same decision ComposedChannelProviders documents: a unit's ingress
// sources are a fact about what this binary composed, they are fixed for the
// life of the process, and no row anywhere records them.
//
// Sorted for publishedChannelProviders' reason — a directory that reorders
// between calls makes a diff of two deployments unreadable.
//
// Nil rather than an empty slice, which is what the field's optionality on the
// wire means; the test that pins it says why.
func publishedCaptureSources(exts []extension.Extension) *[]crmcontracts.CaptureSourceEntry {
	facts := captureSourceFactsFor(exts)
	if len(facts) == 0 {
		return nil
	}
	out := make([]crmcontracts.CaptureSourceEntry, 0, len(facts))
	for _, f := range facts {
		out = append(out, crmcontracts.CaptureSourceEntry{Source: f.source, Label: f.label})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return &out
}

// publishedChannelProviders shapes the composed set for the wire. Split from the
// handler so the shaping is testable without a request, and sorted so the page
// is stable — a directory that reorders between calls makes a diff of two
// deployments unreadable.
func publishedChannelProviders(registered []string, sending map[string]connector.Carriage) []crmcontracts.ChannelProviderEntry {
	facts := channelProviderFactsFor(registered, sending)
	out := make([]crmcontracts.ChannelProviderEntry, 0, len(facts))
	for _, f := range facts {
		entry := crmcontracts.ChannelProviderEntry{
			Provider:          f.provider,
			Label:             f.label,
			CredentialModel:   crmcontracts.ChannelProviderEntryCredentialModel(f.credentialModel),
			SuppliesTransport: f.suppliesTransport,
		}
		// Field by field rather than a shared struct: the contract's entry
		// declares the object inline, so there is no named type to convert to.
		entry.Attachments.Carries = f.carriage.Carries
		entry.Attachments.MaxFiles = f.carriage.MaxFiles
		entry.Attachments.MaxBytesPerFile = f.carriage.MaxBytesPerFile
		entry.Attachments.MaxBodyWithFiles = f.carriage.MaxBodyWithFiles
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// channelProviderDirectory adapts the boot snapshot to the agent surface's
// seam. It reports the same entries the HTTP directory serves, by calling the
// same shaping function — two surfaces answering one question two ways is
// exactly the drift this arc has spent its budget removing.
//
// THE ONE FIELD IT DELIBERATELY DROPS is `attachments`, and the reason is a
// budget rather than a decision about what an agent should know. The core tool
// listing rides in every Surface-B prompt and is already within a few hundred
// tokens of the ceiling TestTheToolListingLeavesTheRunRoomInTheWindow holds it
// to; the carriage object plus the copy that would explain it takes the listing
// past that ceiling, which comes out of the run's own observations. So an agent
// staging a channel message with files still learns the bounds the way it does
// today — by having the delivery parked with a reason that names them. Widening
// the tool surface needs the listing's budget looked at first (issue #1985).
//
// IT ALSO DROPS `capture_sources`, on the same budget and with the same shape of
// consequence: a passport reading REST resolves a unit's `ext:` provenance and
// the same passport's tool listing does not. Nothing is governed differently by
// it — both surfaces are the one read-scope operation, and an id nothing resolves
// falls back to itself — so what an agent loses is a display string, not an
// answer. It is named here rather than left implicit because an unnamed
// divergence is how the two surfaces come to disagree about something that
// matters.
type channelProviderDirectory struct{}

func (channelProviderDirectory) ChannelProviders(context.Context) ([]agents.ChannelProviderEntry, error) {
	registered, sending := ComposedChannelProviders()
	published := publishedChannelProviders(registered, sending)
	out := make([]agents.ChannelProviderEntry, 0, len(published))
	for _, e := range published {
		out = append(out, agents.ChannelProviderEntry{
			Provider:          e.Provider,
			Label:             e.Label,
			CredentialModel:   string(e.CredentialModel),
			SuppliesTransport: e.SuppliesTransport,
		})
	}
	return out, nil
}
