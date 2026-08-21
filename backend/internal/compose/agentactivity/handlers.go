// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentactivity

// The personal read (GET /me/agent-activity), shadowing the generated stub over
// the store. No RBAC object gates it: the feed is the caller's own by
// construction, so there is no wider set to withhold and the caller's identity
// is the whole of the authorization.

import (
	"context"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Reader is the one read this transport needs. Stated as an interface so the
// refusal and the wire mapping — the two rules that live in the transport
// rather than in SQL — can be pinned without a database. *Store is the
// production implementation.
type Reader interface {
	Mine(ctx context.Context, userID ids.UUID) (running, recent []Item, err error)
}

// Handlers serves one person's view of the scheduled agent's work.
type Handlers struct {
	store Reader
	// now stamps as_of, which is when the server read — injected so a test
	// states the instant it means instead of asserting against the wall clock.
	now func() time.Time
}

// NewHandlers binds the transport to a ready reader; compose constructs it once
// per process role.
func NewHandlers(store Reader, now func() time.Time) Handlers {
	return Handlers{store: store, now: now}
}

// GetMyAgentActivity answers with what is running for the caller now and what
// settled for them today.
//
// A caller with no user identity is REFUSED rather than served empty arrays: an
// empty feed is the real answer for an agent at rest, so handing one to an
// unidentified caller would report "nothing is running" about a person the
// server never resolved.
func (h Handlers) GetMyAgentActivity(w http.ResponseWriter, r *http.Request) {
	p, ok := principal.Actor(r.Context())
	if !ok || p.UserID.IsZero() {
		httperr.Unauthorized(w, r, "reading your agent activity needs an authenticated caller")
		return
	}
	running, recent, err := h.store.Mine(r.Context(), p.UserID)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.AgentActivity{
		AsOf:    h.now(),
		Running: toWire(running),
		Recent:  toWire(recent),
	})
}

// toWire maps the store's facts onto the contract's shape.
//
// The result is always an allocated slice, so an empty feed serializes as `[]`
// and never as `null`: the contract declares both fields as arrays, and a
// client that iterates what it was promised crashes on a null.
func toWire(items []Item) []crmcontracts.ActivityItem {
	wire := make([]crmcontracts.ActivityItem, 0, len(items))
	for _, item := range items {
		wire = append(wire, crmcontracts.ActivityItem{
			Id: openapi_types.UUID(item.ID),
			// Kind is the runner's own catalog name, passed through rather than
			// re-mapped. A name this contract's enum does not carry renders no
			// line on the client, which is a better answer than a server that
			// silently omits work the agent really did. The state vocabulary is
			// the opposite case and the store already translated it.
			Kind:          crmcontracts.ActivityItemKind(item.Kind),
			State:         crmcontracts.ActivityItemState(item.State),
			StartedAt:     item.StartedAt,
			FinishedAt:    item.FinishedAt,
			DegradeReason: item.DegradeReason,
			Summary:       item.Summary,
		})
	}
	return wire
}
