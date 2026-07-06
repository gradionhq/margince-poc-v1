// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package signals

// Handlers is the signals module's transport surface. Wire concerns only
// — decode, validate, map store errors to the sentinel registry; the
// store owns the transactional write shape and the row-scope gates.

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type Handlers struct {
	store *Store
}

// NewHandlers wires the transport over the store. strength is the §4
// relationship-strength seam (implemented by the people module, injected
// by the composition layer — never a sibling import).
func NewHandlers(pool *pgxpool.Pool, strength StrengthSource) Handlers {
	return Handlers{store: NewStore(pool, strength)}
}

func (h Handlers) ListSignals(w http.ResponseWriter, r *http.Request, params crmcontracts.ListSignalsParams) {
	in := ListSignalsInput{
		Cursor:          params.Cursor,
		Limit:           params.Limit,
		Status:          (*string)(params.Status),
		Kind:            (*string)(params.Kind),
		ResolutionState: (*string)(params.ResolutionState),
		IncludeArchived: params.IncludeArchived != nil && *params.IncludeArchived,
	}
	signals, page, err := h.store.ListSignals(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.SignalListResponse{Data: signals, Page: pageInfo(page)})
}

func (h Handlers) CreateSignal(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateSignalParams) {
	var req crmcontracts.CreateSignalRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := CreateSignalInput{
		Kind:       string(req.Kind),
		RawRef:     req.RawRef,
		Summary:    req.Summary,
		DetectedAt: req.DetectedAt,
		Source:     req.Source,
	}
	if req.SourceChannel != nil {
		in.SourceChannel = string(*req.SourceChannel)
	}
	if req.Severity != nil {
		in.Severity = string(*req.Severity)
	}
	if req.EntityType != nil {
		entityType := string(*req.EntityType)
		in.EntityType = &entityType
	}
	if req.EntityId != nil {
		entityID := ids.UUID(*req.EntityId)
		in.EntityID = &entityID
	}
	if req.Evidence != nil {
		in.Evidence = *req.Evidence
	}
	sig, err := h.store.CreateSignal(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/signals/"+sig.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, sig)
}

func (h Handlers) GetSignal(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	sig, err := h.store.GetSignal(r.Context(), ids.UUID(id), storekit.IncludeArchived)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, sig)
}

func (h Handlers) UpdateSignal(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateSignalParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateSignalRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := UpdateSignalInput{
		Status:    (*string)(req.Status),
		Note:      req.Note,
		Severity:  (*string)(req.Severity),
		IfVersion: ifVersion,
	}
	sig, err := h.store.UpdateSignal(r.Context(), ids.UUID(id), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, sig)
}

func (h Handlers) ArchiveSignal(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	sig, err := h.store.ArchiveSignal(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, sig)
}

func (h Handlers) ResolveSignal(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.ResolveSignalParams) {
	sig, err := h.store.Resolve(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, sig)
}

func (h Handlers) GetSignalWarmth(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	warmth, err := h.store.Warmth(r.Context(), ids.UUID(id), time.Now().UTC())
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, warmth)
}

func (h Handlers) GetSignalIntroPath(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	path, err := h.store.IntroPath(r.Context(), ids.UUID(id), time.Now().UTC())
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, path)
}

func pageInfo(p storekit.Page) crmcontracts.PageInfo {
	info := crmcontracts.PageInfo{HasMore: p.HasMore}
	if p.NextCursor != "" {
		info.NextCursor = &p.NextCursor
	}
	return info
}

// writeStoreErr maps this module's typed store errors onto the wire
// codes, then falls through to the sentinel registry.
func writeStoreErr(w http.ResponseWriter, r *http.Request, err error) {
	var missing *RequiredFieldError
	if errors.As(err, &missing) {
		httperr.Write(w, r, httperr.Validation(missing.Field, "required", missing.Error()))
		return
	}
	var notResolvable *NotResolvableError
	if errors.As(err, &notResolvable) {
		httperr.Write(w, r, httperr.Validation("resolution_state", "not_resolvable", notResolvable.Error()))
		return
	}
	var noWarmth *NoWarmthError
	if errors.As(err, &noWarmth) {
		httperr.Write(w, r, httperr.Validation("resolution_state", "no_warmth", noWarmth.Error()))
		return
	}
	httperr.Write(w, r, err)
}
