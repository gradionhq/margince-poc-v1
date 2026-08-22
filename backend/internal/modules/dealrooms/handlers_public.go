// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// The buyer edge's transport. Every handler here reaches the store ONLY
// through the session-scoped methods in store_public*.go — never the seller's
// — and TestPublicHandlersReachOnlyTheSessionScopedStore holds that line.
//
// The session itself is resolved by the compose middleware in front of these
// routes, which binds it with WithSession; a handler that finds none answers
// 401 rather than assuming, because the only way that happens is a route
// mounted outside the middleware.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// noSessionDetail is the 401 a buyer reads. It names no room and no reason.
const noSessionDetail = "this link no longer admits you: ask for a new one"

// PeekDealRoomCredential says whether a credential can still be exchanged.
func (h Handlers) PeekDealRoomCredential(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.DealRoomCredentialRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	ok, err := h.store.PeekCredential(r.Context(), strings.TrimSpace(req.Credential))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.DealRoomPeekResponse{Exchangeable: ok})
}

// ExchangeDealRoomCredential consumes a credential and opens a session.
func (h Handlers) ExchangeDealRoomCredential(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.DealRoomCredentialRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	issued, err := h.store.ExchangeCredential(r.Context(), strings.TrimSpace(req.Credential))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.DealRoomSessionIssued{
		SessionToken: issued.Token,
		ExpiresAt:    issued.ExpiresAt,
	})
}

// RequestDealRoomLink mails a fresh link to a known address, and says 202 to
// every address. The reissue and the mail are both best-effort from the
// caller's point of view: a failure is logged for the operator and never
// reported to the anonymous requester, who must not be able to tell a known
// address from an unknown one by the shape of the answer.
func (h Handlers) RequestDealRoomLink(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.DealRoomLinkRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	// Parsed the way an invite parses it, so the lookup compares one spelling
	// against the lowercased column rather than whatever the form sent.
	email, err := values.ParseEmail(string(req.Email))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	// Answered before the work, so a known address costs the caller no more
	// wall-clock than an unknown one. The work then runs DETACHED from the
	// request: a caller who hangs up the moment the 202 lands must not be able
	// to cancel between the reissue committing and the mail going out, which
	// would retire a credential and deliver nothing. The reissue is attributed
	// to the installation, the same actor the other anonymous edges write under.
	w.WriteHeader(http.StatusAccepted)
	ctx := principal.WithActor(context.WithoutCancel(r.Context()), linkRequestPrincipal)
	if !h.canSendInvite() {
		// Without a relay there is nothing to do with a credential but mail it;
		// minting one that nobody delivers would retire a link the buyer may
		// still have. The seller's roster is the path in that installation.
		return
	}
	issued, err := h.store.ReissueByEmail(ctx, email.String())
	if err != nil {
		slog.ErrorContext(ctx, "deal room link request failed", "err", err)
		return
	}
	attributed := r.WithContext(ctx)
	for _, inv := range issued {
		sendErr := h.sendInvite(attributed, inv)
		if sendErr != nil {
			slog.ErrorContext(ctx, "deal room link request email failed",
				"participant_id", inv.Participant.Id, "err", sendErr)
		}
		h.recordSendOutcome(attributed, inv, sendErr)
	}
}

// GetBuyerRoom is the room bootstrap.
func (h Handlers) GetBuyerRoom(w http.ResponseWriter, r *http.Request) {
	sess, ok := SessionFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, noSessionDetail)
		return
	}
	view, err := h.store.BuyerView(r.Context(), sess)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, view)
}

// ListBuyerRoomTasks serves the published list with live completion.
func (h Handlers) ListBuyerRoomTasks(w http.ResponseWriter, r *http.Request) {
	sess, ok := SessionFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, noSessionDetail)
		return
	}
	tasks, err := h.store.BuyerTasks(r.Context(), sess)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.BuyerRoomTaskListResponse{Data: tasks})
}

// CompleteBuyerRoomTask ticks or un-ticks one item.
func (h Handlers) CompleteBuyerRoomTask(w http.ResponseWriter, r *http.Request, taskID openapi_types.UUID) {
	sess, ok := SessionFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, noSessionDetail)
		return
	}
	var req crmcontracts.CompleteBuyerRoomTaskRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	task, err := h.store.CompleteBuyerTask(r.Context(), sess,
		ids.From[ids.DealRoomTaskKind](ids.UUID(taskID)), req.Done)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, task)
}

// SignOutBuyerRoom ends the session.
func (h Handlers) SignOutBuyerRoom(w http.ResponseWriter, r *http.Request) {
	sess, ok := SessionFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, noSessionDetail)
		return
	}
	if err := h.store.SignOut(r.Context(), sess); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListBuyerRoomDocuments serves the published manifest.
func (h Handlers) ListBuyerRoomDocuments(w http.ResponseWriter, r *http.Request) {
	sess, ok := SessionFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, noSessionDetail)
		return
	}
	docs, err := h.store.BuyerDocuments(r.Context(), sess)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.BuyerRoomDocumentListResponse{Data: docs})
}

// DownloadBuyerRoomDocument streams one published document's bytes.
func (h Handlers) DownloadBuyerRoomDocument(w http.ResponseWriter, r *http.Request, documentID openapi_types.UUID) {
	sess, ok := SessionFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, noSessionDetail)
		return
	}
	if h.documents == nil {
		httperr.Write(w, r, errNoDocumentStore)
		return
	}
	file, err := h.store.BuyerDocumentLocator(r.Context(), sess, documentIDOf(documentID))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	body, _, err := h.documents.Get(r.Context(), file.StorageKey)
	if err != nil {
		httperr.Write(w, r, fmt.Errorf("open deal room document %s: %w", documentID, err))
		return
	}
	contentType := "application/octet-stream"
	if file.ContentType != nil && *file.ContentType != "" {
		contentType = *file.ContentType
	}
	var size int64
	if file.ByteSize != nil {
		size = *file.ByteSize
	}
	httperr.StreamObject(w, r, httperr.StreamedObject{
		Body:        body,
		ContentType: contentType,
		Filename:    file.Filename,
		Size:        size,
	}, "deal room document "+documentID.String())
}

// errNoDocumentStore says the installation cannot serve files at all, which is
// a deployment fact and not something a buyer can fix — but it is not "the
// file is gone", which is what a 404 would claim.
var errNoDocumentStore = errors.New("dealrooms: no object store is configured, so documents cannot be downloaded; the operator must configure one")
