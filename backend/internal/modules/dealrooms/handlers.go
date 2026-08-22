// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"context"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/mailer"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers is this module's transport.
type Handlers struct {
	store *Store
	// inviteMailer delivers a buyer's invitation. Nil means the installation
	// has no outbound mail configured: invitations are still recorded and their
	// credential still returned, and the response says it was not delivered.
	inviteMailer mailer.Mailer
	// publicBaseURL is the origin a buyer link is built on.
	publicBaseURL string
	// documents opens the bytes behind a published document. Nil means the
	// installation has no object store, and a buyer's download says so
	// rather than pretending the file is absent.
	documents blobstore.Store
}

// WithDocumentStore binds the object store a buyer's download reads from.
func (h Handlers) WithDocumentStore(store blobstore.Store) Handlers {
	h.documents = store
	return h
}

// NewHandlers builds the Deal Room handler set.
func NewHandlers(db *database.DB) Handlers {
	return Handlers{store: NewStore(db)}
}

func pathID(id crmcontracts.Id) ids.DealRoomID {
	return ids.DealRoomID{UUID: ids.UUID(id)}
}

// ListDealRooms pages the rooms whose deals the caller can see.
func (h Handlers) ListDealRooms(w http.ResponseWriter, r *http.Request, params crmcontracts.ListDealRoomsParams) {
	rooms, page, err := h.store.ListRooms(r.Context(), listInput(params))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.DealRoomListResponse{
		Data: rooms,
		Page: pageInfo(page),
	})
}

// CreateDealRoom opens a room on a deal.
func (h Handlers) CreateDealRoom(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateDealRoomParams) {
	var req crmcontracts.CreateDealRoomRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in, err := createInput(req)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	room, err := h.store.CreateRoom(r.Context(), in)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/deal-rooms/"+room.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, room)
}

// GetDealRoom reads one room.
func (h Handlers) GetDealRoom(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	room, err := h.store.GetRoom(r.Context(), pathID(id))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, room)
}

// UpdateDealRoom edits the working copy. A live room keeps serving its last
// release until somebody publishes again.
func (h Handlers) UpdateDealRoom(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateDealRoomParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateDealRoomRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	room, err := h.store.UpdateRoom(r.Context(), pathID(id), updateInput(req, ifVersion))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, room)
}

// ArchiveDealRoom ends the room and revokes buyer access.
func (h Handlers) ArchiveDealRoom(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.ArchiveDealRoomParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	room, err := h.store.ArchiveRoom(r.Context(), pathID(id), ifVersion)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, room)
}

// PublishDealRoom freezes the working copy as the next release.
func (h Handlers) PublishDealRoom(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	note, ok := publishNote(w, r)
	if !ok {
		return
	}
	release, err := h.store.PublishRoom(r.Context(), pathID(id), note)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, release)
}

// PauseDealRoom refuses buyer reads while credentials stay valid.
func (h Handlers) PauseDealRoom(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	h.writeMove(w, r, h.store.PauseRoom, id)
}

// ResumeDealRoom returns a paused room to live on its existing release.
func (h Handlers) ResumeDealRoom(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	h.writeMove(w, r, h.store.ResumeRoom, id)
}

// CloseDealRoom freezes content while leaving access intact.
func (h Handlers) CloseDealRoom(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	h.writeMove(w, r, h.store.CloseRoom, id)
}

// writeMove runs one lifecycle transition and writes its room. The three moves
// differ only in which store method they call, so they share this.
func (h Handlers) writeMove(w http.ResponseWriter, r *http.Request,
	move func(ctx context.Context, id ids.DealRoomID) (crmcontracts.DealRoom, error), id crmcontracts.Id,
) {
	room, err := move(r.Context(), pathID(id))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, room)
}

// SetDealRoomExpiry moves or clears when buyer access lapses.
func (h Handlers) SetDealRoomExpiry(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.SetDealRoomExpiryParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.SetDealRoomExpiryRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	room, err := h.store.SetExpiry(r.Context(), pathID(id), req.ExpiresAt, ifVersion)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, room)
}

// ListDealRoomReleases pages what this room has shown a buyer over its life.
func (h Handlers) ListDealRoomReleases(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.ListDealRoomReleasesParams) {
	releases, page, err := h.store.ListReleases(r.Context(), pathID(id), limitArg(params.Limit), cursorArg(params.Cursor))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.DealRoomReleaseListResponse{
		Data: releases,
		Page: pageInfo(page),
	})
}

// GetDealRoomChanges serves what a publish would change.
func (h Handlers) GetDealRoomChanges(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	changes, err := h.store.Changes(r.Context(), pathID(id))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, changes)
}
