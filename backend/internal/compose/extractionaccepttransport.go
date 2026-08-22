// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The HTTP half of accepting a reading's fields onto a deal: decode, call the
// engine next door, and map its typed refusals onto the wire.
//
// Split from extractionaccept.go for the same reason the transcript reading's
// transport is its own file: that one owns the flow and the invariants, this
// one owns nothing but the wire.

import (
	"errors"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// attachmentExtractionHandlers is the transport for the accept-write; the
// engine above owns the flow.
type attachmentExtractionHandlers struct {
	accept *ExtractionAccept
}

func (h attachmentExtractionHandlers) AcceptAttachmentExtraction(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.AcceptExtractionRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	resp, err := h.accept.Accept(r.Context(), ids.UUID(id), req)
	if err != nil {
		writeExtractionAcceptErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

// writeExtractionAcceptErr maps the accept flow's typed refusals onto the
// wire, mirroring the deals transport's spellings for the store errors this
// flow can trip (the resulting-row money pair, INV-CLOSE-PAST), then falls
// through to the sentinel registry — which is also where a CHECK breach is
// answered, by httperr's own net rather than a spelling of it here.
func writeExtractionAcceptErr(w http.ResponseWriter, r *http.Request, err error) {
	// UnsupportedEntityTypeError and ExtractionAcceptError carry their own verdicts
	// (MessageFault / FieldFault), so the fallthrough below renders them. Do not
	// re-spell either here: two spellings of one refusal is how the surfaces drift.
	var amountPair *deals.AmountCurrencyPairError
	if errors.As(err, &amountPair) {
		httperr.Write(w, r, httperr.Validation(acceptFieldCurrency, "amount_currency_pair", amountPair.Error()))
		return
	}
	var pastClose *deals.PastCloseDateError
	if errors.As(err, &pastClose) {
		httperr.Write(w, r, httperr.Validation(acceptFieldExpectedClose, "close_date_past", pastClose.Error()))
		return
	}
	httperr.Write(w, r, err)
}
