// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

// ListDealRoomThreads returns the room's conversation.
func (h Handlers) ListDealRoomThreads(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.ListDealRoomThreadsParams) {
	threads, err := h.store.ListThreads(r.Context(), pathID(id), optionalUUID(params.DocumentId))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.DealRoomThreadListResponse{Data: threads})
}

// OpenDealRoomThread opens a thread as the seller's side.
func (h Handlers) OpenDealRoomThread(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.OpenDealRoomThreadRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in, err := openThreadInput(req, true)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	thread, err := h.store.OpenThread(r.Context(), pathID(id), in)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, thread)
}

// ReplyDealRoomThread answers in a thread as the seller's side.
func (h Handlers) ReplyDealRoomThread(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, threadID openapi_types.UUID) {
	var req crmcontracts.PostDealRoomCommentRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	body, source, err := commentInput(req, true)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	thread, err := h.store.Reply(r.Context(), pathID(id), ids.UUID(threadID), body, source)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, thread)
}

// ResolveDealRoomThread closes a thread.
func (h Handlers) ResolveDealRoomThread(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, threadID openapi_types.UUID) {
	thread, err := h.store.ResolveThread(r.Context(), pathID(id), ids.UUID(threadID))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, thread)
}

// ListDealRoomDecisions returns what the buyer decided.
func (h Handlers) ListDealRoomDecisions(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	decisions, err := h.store.ListDecisions(r.Context(), pathID(id))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.DealRoomDecisionListResponse{Data: decisions})
}

func optionalUUID(id *openapi_types.UUID) *ids.UUID {
	if id == nil {
		return nil
	}
	u := ids.UUID(*id)
	return &u
}

// openThreadInput validates a thread opening for either side. The seller's
// transport requires a provenance source like every other seller write; the
// buyer's edge has no source to state and records the credential's.
func openThreadInput(req crmcontracts.OpenDealRoomThreadRequest, sellerSide bool) (OpenThreadInput, error) {
	body, err := cleanBody(req.Body)
	if err != nil {
		return OpenThreadInput{}, err
	}
	source, err := sourceFor(req.Source, sellerSide)
	if err != nil {
		return OpenThreadInput{}, err
	}
	in := OpenThreadInput{DocumentID: optionalUUID(req.DocumentId), Body: body, Source: source}
	if req.RequiredChange != nil {
		in.RequiredChange = *req.RequiredChange
	}
	return in, nil
}

func commentInput(req crmcontracts.PostDealRoomCommentRequest, sellerSide bool) (body, source string, err error) {
	body, err = cleanBody(req.Body)
	if err != nil {
		return "", "", err
	}
	source, err = sourceFor(req.Source, sellerSide)
	return body, source, err
}

func sourceFor(given *string, sellerSide bool) (string, error) {
	if !sellerSide {
		return sourceCredential, nil
	}
	if given == nil {
		return "", &fieldError{field: "source", code: codeRequired, msg: "source is required"}
	}
	if err := provenance.Refuse("source", *given); err != nil {
		return "", err
	}
	return *given, nil
}
