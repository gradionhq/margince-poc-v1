// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The regenerateOffer shadow (arc 4b, AI-drafted offer regeneration):
// deals.Handlers already promotes a RegenerateOffer method into Server
// (dealsHandlers is embedded at depth 1), but the mechanical mint alone
// cannot see offerDrafter — the AI-drafting orchestrator lives in this
// package because it needs the model lane and the retrieval seam,
// neither of which deals may import (a module never imports a sibling
// module, ADR-0054). A method declared directly on Server (depth 0)
// always wins over a promoted embedded-field method (depth 1+) with no
// ambiguity — the same "module handlers shadow the generated stubs"
// mechanism this package already relies on, one level shallower. Without
// WithOfferDraft wired, this degrades to EXACTLY the mechanical response
// dealsHandlers.RegenerateOffer would have written (same store call,
// same Location/201 shape) — the pre-4b behavior, unchanged.

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// RegenerateOffer runs the mechanical mint FIRST — deals' own
// send/accept/reject/FX-freeze/totals-engine/advisory-lock revision
// backbone (offer_lifecycle.go) is untouched by this file — and, only
// when an offer-drafting brain is wired, hands the freshly minted draft
// revision to offerDrafter to stage evidence-grounded lines on top. AI
// drafting is advisory over that already-committed mint (the same
// posture WithBrief's L2 re-order takes over the Morning Brief's
// deterministic floor): a drafting failure degrades to the mechanical
// revision the caller already has rather than losing it behind a 500.
func (s Server) RegenerateOffer(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.RegenerateOfferParams) {
	ctx := r.Context()
	priorID := ids.From[ids.OfferKind](ids.UUID(id))

	offer, err := s.dealsStore.RegenerateOffer(ctx, priorID)
	if err != nil {
		deals.WriteOfferError(w, r, err)
		return
	}

	if s.offerDrafter != nil {
		// offer.Id is the FRESH draft revision RegenerateOffer just minted,
		// never the path's priorID — that offer is now superseded, and
		// AddStagedOfferLines refuses to stage onto anything but a draft.
		newRevisionID := ids.From[ids.OfferKind](ids.UUID(offer.Id))
		drafted, draftErr := s.offerDrafter.DraftOfferLines(ctx, newRevisionID)
		if draftErr != nil {
			s.log.WarnContext(ctx, "compose: offer draft unavailable after regenerate — serving the mechanical draft",
				"offer_id", newRevisionID, "err", draftErr)
		} else {
			offer = drafted.Offer
		}
	}

	w.Header().Set("Location", "/v1/offers/"+offer.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, offer)
}
