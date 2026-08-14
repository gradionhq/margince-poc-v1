// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// WithScheduleTimer returns handlers that can defer a send. Compose calls this;
// without it a scheduling request refuses rather than accept a moment nothing
// will wake at.
func (h Handlers) WithScheduleTimer(timer ScheduleTimer) Handlers {
	h.timer = timer
	return h
}

// ListScheduledSends answers with the caller's own waiting messages.
func (h Handlers) ListScheduledSends(w http.ResponseWriter, r *http.Request, params crmcontracts.ListScheduledSendsParams) {
	status := ""
	if params.Status != nil {
		status = string(*params.Status)
	}
	rows, err := h.store.ListScheduledSends(r.Context(), status)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	out := make([]crmcontracts.ScheduledSend, 0, len(rows))
	for _, row := range rows {
		out = append(out, scheduledSendResponse(row))
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}

// GetScheduledSend answers with one of the caller's waiting messages.
func (h Handlers) GetScheduledSend(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	row, err := h.store.GetScheduledSend(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, scheduledSendResponse(row))
}

// RescheduleScheduledSend moves a waiting message to a different moment.
func (h Handlers) RescheduleScheduledSend(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.RescheduleScheduledSendParams) {
	var req crmcontracts.RescheduleSendRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	version, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	if version == nil {
		// Required rather than optional: two surfaces moving one message must
		// not silently resolve to whichever wrote last.
		httperr.Write(w, r, httperr.Validation("If-Match",
			"missing_if_match", "moving a scheduled send needs the version you last read"))
		return
	}
	row, err := h.store.RescheduleScheduledSend(r.Context(), ids.UUID(id), SendSchedule{
		At: req.ScheduledAt,
		TZ: req.ScheduledTz,
	}, *version, h.timer)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, scheduledSendResponse(row))
}

// CancelScheduledSend withdraws a waiting message.
func (h Handlers) CancelScheduledSend(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if err := h.store.CancelScheduledSend(r.Context(), ids.UUID(id)); err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// scheduledSendResponse renders one row onto the wire.
func scheduledSendResponse(row ScheduledSend) crmcontracts.ScheduledSend {
	out := crmcontracts.ScheduledSend{
		Id:          openapi_types.UUID(row.ID),
		Status:      crmcontracts.ScheduledSendStatus(row.Status),
		ScheduledAt: row.ScheduledAt,
		ScheduledTz: row.ScheduledTZ,
		Subject:     row.Subject,
		To:          emailList(row.Recipients),
		Version:     row.Version,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if cc := emailList(row.Cc); len(cc) > 0 {
		out.Cc = &cc
	}
	if bcc := emailList(row.Bcc); len(bcc) > 0 {
		out.Bcc = &bcc
	}
	if row.Body != "" {
		body := row.Body
		out.Body = &body
	}
	if row.Anchor.UUID != (ids.UUID{}) {
		anchor := openapi_types.UUID(row.Anchor.UUID)
		out.AnchorActivityId = &anchor
	}
	if row.ActivityID != (ids.UUID{}) {
		activityID := openapi_types.UUID(row.ActivityID)
		out.ActivityId = &activityID
	}
	if row.HeldReason != "" {
		reason := crmcontracts.ScheduledSendHeldReason(row.HeldReason)
		out.HeldReason = &reason
	}
	return out
}

// emailList renders stored addresses onto the wire's email type.
func emailList(addresses []string) []openapi_types.Email {
	out := make([]openapi_types.Email, 0, len(addresses))
	for _, addr := range addresses {
		out = append(out, openapi_types.Email(addr))
	}
	return out
}

// scheduleFrom reads the optional scheduling fields both send requests carry.
// Nil means send now, which is what every caller before ADR-0104 meant.
//
// One spelling for both surfaces, for the same reason sendInputFrom is: a
// second copy would eventually validate one of them differently, and "send
// later" would mean something subtly different depending on which button the
// rep pressed.
func scheduleFrom(at *time.Time, tz *string) (*SendSchedule, error) {
	if at == nil && tz == nil {
		// Neither field sent: send now, which is what every caller before
		// ADR-0104 meant and what a composer that never shows the control
		// still means. A nil schedule is the ANSWER here, not a missing one.
		return nil, nil //nolint:nilnil // "send now" IS the answer for an optional schedule, not a missing value.
	}
	if at == nil {
		return nil, &InvalidScheduleError{Field: fieldScheduledAt, Reason: "is required when a zone is given"}
	}
	if tz == nil {
		return nil, &InvalidScheduleError{Field: fieldScheduledTZ, Reason: "is required when a moment is given"}
	}
	return &SendSchedule{At: *at, TZ: *tz}, nil
}
