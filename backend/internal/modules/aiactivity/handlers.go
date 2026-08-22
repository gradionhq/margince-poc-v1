// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aiactivity

// The personal read (GET /me/ai-activity), shadowing the generated stub over
// the projection. No RBAC object gates it: the feed is the caller's own by
// construction, so there is no wider set to withhold and the caller's identity
// is the whole of the authorization.

import (
	"context"
	"net/http"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Reader is the one read this transport needs. Stated as an interface so the
// refusal and the wire mapping — the two rules that live in the transport
// rather than in SQL — can be pinned without a database. *Store is the
// production implementation.
type Reader interface {
	Mine(ctx context.Context, startOfToday time.Time, kinds []string) (live, settled []Item, err error)
}

// Handlers serves one person's view of the AI's work.
type Handlers struct {
	store Reader
	// now stamps as_of and bounds "today" — injected so a test states the
	// instant it means rather than racing the wall clock across midnight.
	now func() time.Time
}

// NewHandlers binds the transport to a ready reader; compose constructs it once
// per process role.
func NewHandlers(store Reader, now func() time.Time) Handlers {
	return Handlers{store: store, now: now}
}

// GetMyAiActivity answers with what is live for the caller now and what settled
// for them today.
//
// A caller with no user identity is REFUSED rather than served empty arrays: an
// empty feed is the real answer for an AI at rest, so handing one to an
// unidentified caller would report "nothing is running" about a person the
// server never resolved.
func (h Handlers) GetMyAiActivity(w http.ResponseWriter, r *http.Request, params crmcontracts.GetMyAiActivityParams) {
	// Refused HERE as well as in the store, and the duplication is deliberate:
	// this one turns an unidentified caller into a 401 the client understands,
	// where the store's turns it into a 500. The store's is the one that makes
	// it impossible; this one makes it legible.
	p, ok := principal.Actor(r.Context())
	if !ok || p.UserID.IsZero() {
		httperr.Unauthorized(w, r, "reading your AI activity needs an authenticated caller")
		return
	}
	kinds, refusal := requestedKinds(params)
	if refusal != nil {
		httperr.Write(w, r, refusal)
		return
	}
	now := h.now()
	live, settled, err := h.store.Mine(r.Context(), startOfDay(now), kinds)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.AiActivity{
		AsOf:    now,
		Running: toWire(live),
		Recent:  toWire(settled),
	})
}

// requestedKinds is the caller's filter as the store takes it: nil for every
// kind, a populated slice for the kinds a client draws, or a refusal.
//
// Two filters are REFUSED rather than served, and they are the same defect
// reached through different doors: an empty list, and a list naming a kind this
// contract has no word for. Both make the feed come back empty, and an empty
// feed is the TRUE answer for an AI at rest — so serving either one reports
// "nothing happened" about a question the server never actually asked. `?kinds=`
// is what a client sends when its list went missing; `?kinds=summarise` is what
// it sends when somebody typed the vocabulary by hand.
//
// The membership test is the CONTRACT's own generated Valid(), never a list
// restated here: the generated binder does not check the enum (AiActivityKind
// is a string type, and BindQueryParameterWithOptions binds anything into it),
// so this is the only place the vocabulary is enforced — and a second copy of
// it would be a second vocabulary the moment somebody edited one.
func requestedKinds(params crmcontracts.GetMyAiActivityParams) (kinds []string, refusal error) {
	if params.Kinds == nil {
		return nil, nil
	}
	// `?kinds=` — the literal shape a client sends when its list went missing —
	// does NOT arrive as a zero-length slice. The generated binder splits the raw
	// value and hands back one empty member, so a length check alone would route
	// that case to "no kind by that name", which is true of the empty string and
	// useless to the reader. Both spellings of "I asked for nothing" answer the
	// same way, and it took a test through the real binder to notice that only
	// one of them ever reached this branch.
	if allBlank(*params.Kinds) {
		return nil, httperr.Validation("kinds", "empty_filter",
			"name at least one kind of AI work, or omit kinds entirely to receive every kind")
	}
	out := make([]string, 0, len(*params.Kinds))
	for _, kind := range *params.Kinds {
		if !kind.Valid() {
			return nil, httperr.Validation("kinds", "unknown_kind",
				"this server has no kind of AI work by that name, so the filter could only ever come back empty")
		}
		out = append(out, string(kind))
	}
	return out, nil
}

// allBlank reports a filter that names nothing: no members, or only empty ones.
func allBlank(kinds []crmcontracts.AiActivityKind) bool {
	for _, kind := range kinds {
		if strings.TrimSpace(string(kind)) != "" {
			return false
		}
	}
	return true
}

// startOfDay is midnight in the clock's own location, which is what "today"
// means to the person reading the rail.
func startOfDay(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

// toWire maps the projection's facts onto the contract's shape.
//
// The result is always an allocated slice, so an empty feed serializes as `[]`
// and never as `null`: the contract declares both fields as arrays, and a
// client that iterates what it was promised crashes on a null.
//
// kind and state are passed through rather than re-mapped. Both vocabularies
// are already the contract's — kind because the emitter writes the catalog name
// the copy is keyed on, state because the projection's CHECK and the contract's
// enum are held equal by a fitness test — so a translation layer here would be
// a second place for them to disagree.
func toWire(items []Item) []crmcontracts.AiActivityItem {
	wire := make([]crmcontracts.AiActivityItem, 0, len(items))
	for _, item := range items {
		wire = append(wire, crmcontracts.AiActivityItem{
			Id:            openapi_types.UUID(item.ID),
			Kind:          crmcontracts.AiActivityKind(item.Kind),
			State:         crmcontracts.AiActivityItemState(item.State),
			StartedAt:     item.StartedAt,
			FinishedAt:    item.FinishedAt,
			DegradeReason: item.DegradeReason,
			Summary:       item.Summary,
		})
	}
	return wire
}
