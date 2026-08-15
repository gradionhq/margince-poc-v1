// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The /capture/activity transport. compose embeds TraceHandlers so these
// methods shadow the generated 501 stubs; a role that composes no trace store
// leaves the field zero and both operations answer an honest 503 rather than
// nil-dereferencing.
//
// The gate is in the STORE, not here: ListWorkspace takes it, ListMine has none
// to take. This layer decodes, maps and writes.

import (
	"errors"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TraceHandlers is the trace read transport.
type TraceHandlers struct {
	store *TraceStore
	// payloadCapture reports the deployment's capture.trace_payloads posture on
	// every answer, so a client can tell "the operator did not enable this" from
	// "this row has no payload". It is read from the composed config rather than
	// inferred from the rows: a window in which every message happened to have no
	// subject would otherwise look like the posture was off.
	payloadCapture bool
}

// NewTraceHandlers wires the transport over a trace store.
func NewTraceHandlers(store *TraceStore, payloadCapture bool) TraceHandlers {
	return TraceHandlers{store: store, payloadCapture: payloadCapture}
}

// ListMyCaptureActivity answers for the caller's own connections.
func (h TraceHandlers) ListMyCaptureActivity(w http.ResponseWriter, r *http.Request,
	params crmcontracts.ListMyCaptureActivityParams,
) {
	h.answer(w, r, func() (TraceWindow, error) {
		return h.store.ListMine(r.Context(), params.Cursor, params.Limit)
	})
}

// ListWorkspaceCaptureActivity answers for the workspace's shared channels.
func (h TraceHandlers) ListWorkspaceCaptureActivity(w http.ResponseWriter, r *http.Request,
	params crmcontracts.ListWorkspaceCaptureActivityParams,
) {
	h.answer(w, r, func() (TraceWindow, error) {
		return h.store.ListWorkspace(r.Context(), params.Cursor, params.Limit)
	})
}

// answer runs one read and writes it, so the two operations differ only in
// which read they name.
func (h TraceHandlers) answer(w http.ResponseWriter, r *http.Request, read func() (TraceWindow, error)) {
	if h.store == nil {
		httperr.ServiceUnavailable(w, r,
			"this deployment composed no capture pipeline, so there is no capture activity to read")
		return
	}
	window, err := read()
	if err != nil {
		h.writeTraceErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, traceResponse(window, h.payloadCapture))
}

// writeTraceErr maps this surface's own refusal onto the wire. Everything else
// is already an apperrors sentinel and travels unchanged.
func (h TraceHandlers) writeTraceErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errNoCallingMember) {
		// Not 403: the caller is not being denied their own traffic — there is
		// simply no member on this invocation to have any.
		httperr.ServiceUnavailable(w, r,
			"this read answers for the calling member, and this invocation has none")
		return
	}
	httperr.Write(w, r, err)
}

// traceResponse maps the store's answer onto the contract's shape.
func traceResponse(window TraceWindow, payloadCapture bool) crmcontracts.CaptureActivityResponse {
	entries := make([]crmcontracts.CaptureTraceEntry, 0, len(window.Entries))
	for _, row := range window.Entries {
		entries = append(entries, traceEntryResponse(row))
	}
	return crmcontracts.CaptureActivityResponse{
		Funnel:                traceFunnelResponse(window.Funnel),
		Data:                  entries,
		Page:                  crmcontracts.PageInfo{NextCursor: nullableString(window.Next)},
		PayloadCaptureEnabled: payloadCapture,
		WindowHours:           TraceWindowHours,
	}
}

func traceFunnelResponse(funnel map[string]int) crmcontracts.CaptureActivityFunnel {
	out := crmcontracts.CaptureActivityFunnel{}
	for outcome, n := range funnel {
		count := n
		switch TraceOutcome(outcome) {
		case TraceCaptured:
			out.Captured = &count
		case TraceInternal:
			out.Internal = &count
		case TraceSuppressed:
			out.Suppressed = &count
		case TraceDeferred:
			out.Deferred = &count
		case TraceFault:
			out.Fault = &count
		}
	}
	return out
}

func traceEntryResponse(row TraceRow) crmcontracts.CaptureTraceEntry {
	entry := crmcontracts.CaptureTraceEntry{
		Id:           openapi_types.UUID(row.ID),
		Connector:    row.Connector,
		Outcome:      crmcontracts.CaptureTraceEntryOutcome(row.Outcome),
		Reason:       nullableString(row.Reason),
		ActivityId:   traceUUID(row.ActivityID),
		Counterparty: nullableString(row.Counterparty),
		Subject:      nullableString(row.Subject),
		OccurredAt:   row.OccurredAt,
	}
	if row.Resolution != nil {
		entry.Resolution = &crmcontracts.CaptureTraceResolution{
			Status:     crmcontracts.CaptureTraceResolutionStatus(row.Resolution.Status),
			Kind:       nullableString(row.Resolution.Kind),
			ResolvedAt: row.Resolution.ResolvedAt,
		}
	}
	return entry
}

// traceUUID renders an optional id for the wire.
func traceUUID(id *ids.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	out := openapi_types.UUID(*id)
	return &out
}

// nullableString renders an absent value as a JSON null rather than an empty
// string: "" and "there is none" are different answers, and a client rendering
// a reason must be able to tell them apart.
func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
