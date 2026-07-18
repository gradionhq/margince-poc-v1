// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The backfill wire (CAP-WIRE-4): preview → explicit start → single-row
// status → cancel. Preview before spend is the consent (ADR-0020/ADR-0063):
// start carries the previewed estimate as the progress denominator, the
// status read is the activation view's one-row fetch, and cancel retains
// everything captured. GetMorningDigest ships its declared 501 here until
// the nightly suite lands (declared or absent, never a silent 404).

package compose

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/riverqueue/river"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// codeWindowInvalid names the RFC 7807 code for a window outside {3m,6m,12m}.
const codeWindowInvalid = "window_invalid"

type backfillHandlers struct {
	registry *capture.Registry
	inserter *jobs.Runner
	log      *slog.Logger
}

// WithCaptureBackfill wires the backfill ops over the connect registry and
// an insert-only River client (the api enqueues, the worker pages). Without
// it the four ops keep their generated 501.
func WithCaptureBackfill(inserter *jobs.Runner) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		if s.connectorHandlers.registry == nil || inserter == nil {
			return
		}
		s.backfillHandlers = backfillHandlers{
			registry: s.connectorHandlers.registry,
			inserter: inserter,
			log:      s.log,
		}
	}
}

// windowMonths maps the contract's window enum onto months.
func windowMonths(w string) (int, bool) {
	switch w {
	case "3m":
		return 3, true
	case "6m":
		return 6, true
	case "12m":
		return 12, true
	default:
		return 0, false
	}
}

func monthsWindow(m int) string {
	switch m {
	case 3:
		return "3m"
	case 6:
		return "6m"
	case 12:
		return "12m"
	default:
		return ""
	}
}

// caller extracts the signed-in human; every backfill op is per-user.
func (h backfillHandlers) caller(w http.ResponseWriter, r *http.Request) (ids.UserID, bool) {
	actor, ok := principal.Actor(r.Context())
	if !ok || actor.Type != principal.PrincipalHuman {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnauthorized, Code: codeUnauthorized,
			Detail: "Backfill is a signed-in human action.",
		})
		return ids.UserID{}, false
	}
	return ids.From[ids.UserKind](actor.UserID), true
}

func (h backfillHandlers) backfillWired(w http.ResponseWriter, r *http.Request, op string) bool {
	if h.registry == nil {
		httperr.NotImplemented(w, r, op)
		return false
	}
	return true
}

func (h backfillHandlers) PreviewConnectorBackfill(w http.ResponseWriter, r *http.Request, provider crmcontracts.CaptureProvider) {
	if !h.backfillWired(w, r, "PreviewConnectorBackfill") {
		return
	}
	userID, ok := h.caller(w, r)
	if !ok {
		return
	}
	var req crmcontracts.BackfillPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity, Code: "window_required",
			Detail: "Pick a window: none, 3m, 6m or 12m.",
		})
		return
	}
	if string(req.Window) == "none" {
		// An honest zero: no window, no scan, no spend.
		writeBackfillJSON(w, crmcontracts.BackfillPreview{
			Window: crmcontracts.BackfillPreviewWindow(req.Window), ComputedAt: time.Now().UTC(),
		})
		return
	}
	months, ok := windowMonths(string(req.Window))
	if !ok {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity, Code: codeWindowInvalid,
			Detail: "The window must be none, 3m, 6m or 12m.",
		})
		return
	}
	messages, tokens, err := h.registry.EstimateBackfill(r.Context(), string(provider), userID, months)
	if err != nil {
		h.writeBackfillError(w, r, err)
		return
	}
	costMinor := 0 // no price feed configured — tokens are the honest unit; 0, never a guess
	writeBackfillJSON(w, crmcontracts.BackfillPreview{
		Window:             crmcontracts.BackfillPreviewWindow(req.Window),
		EstimatedMessages:  messages,
		EstimatedAiTokens:  &tokens,
		EstimatedCostMinor: &costMinor,
		ComputedAt:         time.Now().UTC(),
	})
}

func (h backfillHandlers) StartConnectorBackfill(w http.ResponseWriter, r *http.Request, provider crmcontracts.CaptureProvider) {
	if !h.backfillWired(w, r, "StartConnectorBackfill") {
		return
	}
	userID, ok := h.caller(w, r)
	if !ok {
		return
	}
	var req crmcontracts.StartBackfillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity, Code: "window_required",
			Detail: "Pick a window: 3m, 6m or 12m.",
		})
		return
	}
	months, ok := windowMonths(string(req.Window))
	if !ok {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity, Code: codeWindowInvalid,
			Detail: "The window must be 3m, 6m or 12m ('none' is expressed by not starting).",
		})
		return
	}
	// The preview's estimate rides along as the progress denominator; a
	// client that skipped the preview starts with none (the bar shows counts
	// only — honest, just less shaped).
	estimate := 0
	if messages, _, err := h.registry.EstimateBackfill(r.Context(), string(provider), userID, months); err == nil {
		estimate = messages
	}
	run, err := h.registry.StartBackfill(r.Context(), string(provider), userID, months, estimate)
	if err != nil {
		h.writeBackfillError(w, r, err)
		return
	}
	ws, ok := principal.WorkspaceID(r.Context())
	if !ok {
		// StartBackfill just committed under a workspace-bound transaction, so
		// a missing workspace here is a wiring defect, surfaced honestly.
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusInternalServerError, Code: "workspace_missing",
			Detail: "The request carries no workspace context; the run was recorded but not scheduled. Try again.",
		})
		return
	}
	if err := h.inserter.Enqueue(r.Context(), CaptureBackfillArgs{
		Workspace: ws.String(), BackfillID: run.ID.String(),
	}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeSweepStates}}); err != nil {
		h.log.ErrorContext(r.Context(), "backfill: enqueue", "err", err)
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusInternalServerError, Code: "backfill_enqueue_failed",
			Detail: "The backfill was recorded but could not be scheduled. Try again — the run resumes, nothing is lost.",
		})
		return
	}
	w.WriteHeader(http.StatusAccepted)
	writeBackfillBody(w, h.statusPayload(&run))
}

func (h backfillHandlers) GetConnectorBackfillStatus(w http.ResponseWriter, r *http.Request, provider crmcontracts.CaptureProvider) {
	if !h.backfillWired(w, r, "GetConnectorBackfillStatus") {
		return
	}
	userID, ok := h.caller(w, r)
	if !ok {
		return
	}
	run, err := h.registry.BackfillStatus(r.Context(), string(provider), userID)
	if err != nil {
		h.writeBackfillError(w, r, err)
		return
	}
	writeBackfillJSON(w, h.statusPayload(run))
}

func (h backfillHandlers) CancelConnectorBackfill(w http.ResponseWriter, r *http.Request, provider crmcontracts.CaptureProvider) {
	if !h.backfillWired(w, r, "CancelConnectorBackfill") {
		return
	}
	userID, ok := h.caller(w, r)
	if !ok {
		return
	}
	run, err := h.registry.CancelBackfill(r.Context(), string(provider), userID)
	if err != nil {
		h.writeBackfillError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	writeBackfillBody(w, h.statusPayload(run))
}

// GetMorningDigest keeps its declared 501 until the nightly suite lands.
func (h backfillHandlers) GetMorningDigest(w http.ResponseWriter, r *http.Request, _ crmcontracts.GetMorningDigestParams) {
	httperr.NotImplemented(w, r, "GetMorningDigest")
}

// statusPayload maps a run (or its absence — state "none") onto the wire.
func (h backfillHandlers) statusPayload(run *capture.BackfillRun) crmcontracts.BackfillStatus {
	return backfillStatusPayload(run)
}

// backfillStatusPayload is the ONE run→wire mapping, shared with the
// connection-list surface so the two reads cannot drift.
func backfillStatusPayload(run *capture.BackfillRun) crmcontracts.BackfillStatus {
	if run == nil {
		return crmcontracts.BackfillStatus{State: crmcontracts.BackfillStatusStateNone}
	}
	id := openapi_types.UUID(run.ID)
	window := crmcontracts.BackfillStatusWindow(monthsWindow(run.WindowMonths))
	st := crmcontracts.BackfillStatus{
		State:       crmcontracts.BackfillStatusState(run.Status),
		BackfillId:  &id,
		Window:      &window,
		StartedAt:   run.StartedAt,
		CompletedAt: run.CompletedAt,
		UpdatedAt:   &run.UpdatedAt,
	}
	if run.Estimate != nil {
		st.EstimatedMessages = run.Estimate
	}
	st.Counts = &struct {
		Captured             *int `json:"captured,omitempty"`
		DedupeCandidates     *int `json:"dedupe_candidates,omitempty"`
		MessagesScanned      *int `json:"messages_scanned,omitempty"`
		OrganizationsCreated *int `json:"organizations_created,omitempty"`
		PeopleCreated        *int `json:"people_created,omitempty"`
		Skipped              *int `json:"skipped,omitempty"`
	}{
		MessagesScanned: &run.Scanned, Captured: &run.Captured, Skipped: &run.Skipped,
		PeopleCreated: &run.People, OrganizationsCreated: &run.Organizations, DedupeCandidates: &run.DedupeCands,
	}
	st.LastErrorClass = run.ErrorClass
	return st
}

func (h backfillHandlers) writeBackfillError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, apperrors.ErrNotFound):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusNotFound, Code: "connection_not_found",
			Detail: "No connected mailbox for this provider — connect it first.",
		})
	case errors.Is(err, capture.ErrWindowInvalid):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity, Code: codeWindowInvalid,
			Detail: "The window must be 3m, 6m or 12m.",
		})
	case errors.Is(err, capture.ErrBackfillRunning):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict, Code: "backfill_running",
			Detail: "A backfill is already running for this mailbox.",
		})
	case errors.Is(err, capture.ErrWindowNarrowing):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict, Code: "window_narrowing",
			Detail: "A wider window already ran; the window can only widen.",
		})
	case errors.Is(err, capture.ErrBackfillUnsupported):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity, Code: "connector_unsupported",
			Detail: "This provider cannot enumerate a mailbox backward from a date.",
		})
	case errors.Is(err, apperrors.ErrConflict):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict, Code: "not_running",
			Detail: "There is no running backfill to cancel.",
		})
	default:
		h.log.ErrorContext(r.Context(), "backfill", "err", err)
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusBadGateway, Code: "provider_unreachable",
			Detail: "The provider could not be reached for this operation.",
		})
	}
}

func writeBackfillJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	writeBackfillBody(w, v)
}

func writeBackfillBody(w http.ResponseWriter, v any) {
	//craft:ignore swallowed-errors terminal response encode; the client sees a broken body, retrying changes nothing
	_ = json.NewEncoder(w).Encode(v)
}
