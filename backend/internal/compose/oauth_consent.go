// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// GetConsentRequest shadows the embedded authHandlers.GetConsentRequest with
// the connector's deployment gate. Every other /oauth/* route disappears
// when the connector is undeclared because routes.go mounts that whole
// group behind one nil-handler check (mcpHandler, mcpedge.go) — the routes
// are simply never registered. This operation cannot get the same
// treatment: its path lives in crm.yaml as /oauth/consent-request, so the
// GENERATED /v1 router serves it unconditionally, and a single shared chi
// router has no way to unmount one operation out of the surface it
// generates from the contract.
//
// Shadowing at the Server level is the only remaining way to give it the
// same fate as its siblings: with the gate off, a signed-in human asking
// for it gets the identical apperrors.ErrNotFound a request to any other
// /oauth/ path gets by finding no route at all — the same 404, produced by
// the only means available to a single shared router. identity stays
// unaware that a deployment gate exists; the gate is compose's to know.
func (s Server) GetConsentRequest(w http.ResponseWriter, r *http.Request, params crmcontracts.GetConsentRequestParams) {
	if !s.mcpConnectorEnabled {
		httperr.Write(w, r, apperrors.ErrNotFound)
		return
	}
	s.authHandlers.GetConsentRequest(w, r, params)
}
