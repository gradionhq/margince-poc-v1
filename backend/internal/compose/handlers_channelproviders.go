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
// The authorization argument only holds while the answer stays unprivileged, so
// `label` is derived or compiled in and never operator-configured (see
// channelproviderfacts.go). An administrator-typed name here would make this a
// disclosure decision instead of a display one.
//
// Authentication itself is the chassis's, applied to every /v1 route before a
// handler runs — hence no gate call in the body, unlike ListExtensions.
func (channelProvidersHandlers) ListChannelProviders(w http.ResponseWriter, r *http.Request) {
	registered, sending := ComposedChannelProviders()
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ChannelProviderDirectory{
		Data: publishedChannelProviders(registered, sending),
	})
}

// publishedChannelProviders shapes the composed set for the wire. Split from the
// handler so the shaping is testable without a request, and sorted so the page
// is stable — a directory that reorders between calls makes a diff of two
// deployments unreadable.
func publishedChannelProviders(registered, sending []string) []crmcontracts.ChannelProviderEntry {
	facts := channelProviderFactsFor(registered, sending)
	out := make([]crmcontracts.ChannelProviderEntry, 0, len(facts))
	for _, f := range facts {
		out = append(out, crmcontracts.ChannelProviderEntry{
			Provider:          f.provider,
			Label:             f.label,
			CredentialModel:   crmcontracts.ChannelProviderEntryCredentialModel(f.credentialModel),
			SuppliesTransport: f.suppliesTransport,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// channelProviderDirectory adapts the boot snapshot to the agent surface's
// seam. It reports the SAME entries the HTTP directory serves, by calling the
// same shaping function — two surfaces answering one question two ways is
// exactly the drift this arc has spent its budget removing.
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
