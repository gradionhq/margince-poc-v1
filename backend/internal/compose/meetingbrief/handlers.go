// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

// The HTTP transport for the pre-meeting brief. Wire concerns only: bind the
// path id, refuse the mode this read cannot honestly serve, and hand the result
// to the sentinel error mapping.

import (
	"context"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// OverlayMode answers whether the calling workspace reads from an incumbent
// mirror instead of this system of record.
type OverlayMode func(ctx context.Context) (bool, error)

// Handlers shadows the generated meeting-brief stub.
type Handlers struct {
	svc     *Service
	overlay OverlayMode
}

// NewHandlers binds the transport to a ready service.
func NewHandlers(svc *Service, overlay OverlayMode) Handlers {
	return Handlers{svc: svc, overlay: overlay}
}

// GetMeetingBrief implements GET /activities/{id}/meeting-brief.
func (h Handlers) GetMeetingBrief(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if !h.native(w, r) {
		return
	}
	brief, err := h.svc.Get(r.Context(), ids.UUID(id))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, brief)
}

// native refuses the read in overlay mode.
//
// A mirror holds none of these conversations, so a brief assembled from it
// would describe commitments and objections this installation does not own —
// and the reader would walk into the room having prepared from them.
func (h Handlers) native(w http.ResponseWriter, r *http.Request) bool {
	if h.overlay == nil {
		return true
	}
	overlay, err := h.overlay(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return false
	}
	if overlay {
		httperr.Write(w, r, httperr.Validation("id", "unsupported_in_overlay_mode",
			"the pre-meeting brief is assembled from this system of record; while the workspace reads from the incumbent mirror, open the meeting in the incumbent's own UI"))
		return false
	}
	return true
}
