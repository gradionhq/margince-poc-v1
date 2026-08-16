// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package pipelinetrace

// The two doors onto one ladder.
//
// Two, because an internal-only drop writes no activity row at all — the trace
// is the only record the message ever arrived — so an activity-keyed read cannot
// reach exactly the messages a member most often comes here to ask about.
//
// One payload, so the drawer a member opens from the record page and the one
// they open from the settings list are the same drawer. Two shapes would drift,
// and the second one would be the one nobody looked at.

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	trace "github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
)

// Handlers serves the pipeline trace.
type Handlers struct {
	assembler *Assembler
}

// NewHandlers builds the handler set.
func NewHandlers(a *Assembler) Handlers { return Handlers{assembler: a} }

// ReadActivityPipelineTrace implements GET /activities/{id}/pipeline.
func (h Handlers) ReadActivityPipelineTrace(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if h.assembler == nil {
		httperr.ServiceUnavailable(w, r,
			"this deployment composed no capture pipeline, so there is no trace to read")
		return
	}
	ladder, err := h.assembler.ByActivityID(r.Context(), ids.UUID(id))
	if err != nil {
		// capture's own mapping, not httperr.Write: the store can answer with a
		// sentinel this package does not own, and the sibling window read
		// already turns it into a sentence rather than a bare 500.
		capture.WriteTraceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, h.wire(ladder))
}

// ReadCaptureTracePipeline implements GET /capture/traces/{id}.
func (h Handlers) ReadCaptureTracePipeline(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if h.assembler == nil {
		httperr.ServiceUnavailable(w, r,
			"this deployment composed no capture pipeline, so there is no trace to read")
		return
	}
	ladder, err := h.assembler.ByTraceID(r.Context(), ids.UUID(id))
	if err != nil {
		capture.WriteTraceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, h.wire(ladder))
}

// wire renders the ladder onto the contract shape.
func (h Handlers) wire(l Ladder) crmcontracts.PipelineTrace {
	stages := make([]crmcontracts.PipelineStageRung, 0, len(l.Rungs))
	for _, rung := range l.Rungs {
		stages = append(stages, wireRung(rung, l.PayloadsEnabled))
	}
	out := crmcontracts.PipelineTrace{
		Connector:             &l.Connector,
		PayloadCaptureEnabled: l.PayloadsEnabled,
		RetentionHours:        trace.RetentionHours,
		Stages:                stages,
	}
	if l.ActivityID != nil {
		id := crmcontracts.Id(*l.ActivityID)
		out.ActivityId = &id
	}
	return out
}

// wireRung renders one rung, and is the last place the payload posture is
// enforced.
//
// The assembler already declines to copy content onto a rung the caller does not
// own, so this is the second of two gates rather than the only one — but it is
// the one that cannot be bypassed by a future rung builder forgetting the first.
func wireRung(rung Rung, payloads bool) crmcontracts.PipelineStageRung {
	out := crmcontracts.PipelineStageRung{
		Stage:       string(rung.Stage),
		Order:       rung.Order,
		SubjectKind: crmcontracts.PipelineStageRungSubjectKind(rung.SubjectKind),
		Status:      crmcontracts.PipelineStageRungStatus(rung.Status),
	}
	label := trace.StageLabel(rung.Stage)
	out.Label = &label
	if rung.Reason != "" {
		reason := string(rung.Reason)
		text := trace.ReasonText(rung.Stage, rung.Reason)
		out.Reason, out.ReasonText = &reason, &text
	}
	out.At = rung.At
	if payloads {
		out.Counterparty = optional(rung.Counterparty)
		out.Subject = optional(rung.Subject)
	}
	return out
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
