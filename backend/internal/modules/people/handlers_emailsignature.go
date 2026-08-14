// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The caller's own sign-off, over HTTP. Both operations resolve the subject
// from the session rather than a path parameter, which is what makes "always
// your own, never anybody else's" a property of the surface rather than a rule
// somebody has to remember to check.

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// GetMyEmailSignature implements GET /me/email-signature.
func (h Handlers) GetMyEmailSignature(w http.ResponseWriter, r *http.Request) {
	signature, err := h.store.GetMyEmailSignature(r.Context())
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, emailSignatureWire(signature))
}

// SaveMyEmailSignature implements PUT /me/email-signature.
func (h Handlers) SaveMyEmailSignature(w http.ResponseWriter, r *http.Request) {
	var body crmcontracts.SaveEmailSignatureRequest
	if !httperr.Decode(w, r, &body) {
		return
	}
	signature, err := h.store.SaveMyEmailSignature(r.Context(), body.Body)
	if err != nil {
		// No branch for SignatureTooLongError: it implements
		// apperrors.FieldFault, so every surface renders the same 422 from the
		// error itself rather than from a transport that has to remember.
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, emailSignatureWire(signature))
}

func emailSignatureWire(signature EmailSignature) crmcontracts.EmailSignature {
	return crmcontracts.EmailSignature{
		Body:      signature.Body,
		UpdatedAt: signature.UpdatedAt,
	}
}
