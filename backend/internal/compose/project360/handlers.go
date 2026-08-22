// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package project360

// The HTTP transport for the project page. Wire concerns only: bind the
// path id, refuse the mode this read cannot honestly serve, and hand the
// result to the sentinel error mapping. The service owns the transaction
// and every gate.

import (
	"context"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// OverlayMode answers whether the workspace reads from an incumbent mirror
// instead of this system of record. Compose injects the one Dispatcher
// every other overlay-aware read uses.
type OverlayMode func(ctx context.Context) (bool, error)

// Handlers shadows the generated GetProject360 stub.
type Handlers struct {
	svc     *Service
	overlay OverlayMode
}

// NewHandlers binds the transport to a ready service.
func NewHandlers(svc *Service, overlay OverlayMode) Handlers {
	return Handlers{svc: svc, overlay: overlay}
}

// GetProject360 implements GET /projects/{id}/360.
func (h Handlers) GetProject360(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	overlay, err := h.overlay(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if overlay {
		// The mirror holds the incumbent's records and no project at all, so
		// there is no honest page to assemble from it. A mode-resolution
		// failure above refuses too: serving native data because the lookup
		// broke is the silent fallback the overlay module exists to prevent.
		httperr.Write(w, r, httperr.Validation("id", "unsupported_in_overlay_mode",
			"the project view is assembled from this system of record; while the workspace reads from the incumbent mirror there is no project to assemble it for"))
		return
	}
	view, err := h.svc.Assemble(r.Context(), ids.From[ids.ProjectKind](ids.UUID(id)))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, view)
}
