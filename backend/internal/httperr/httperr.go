// Package httperr is the single sentinel→HTTP choke point
// (architecture/11 §1): handlers return errs sentinels and this mapper
// produces the RFC 7807 problem+json body with the contract's stable
// machine code. No handler hand-writes a status body.
package httperr

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

const problemTypeBase = "https://errors.gradion.com/"

type problem struct {
	Type    string         `json:"type"`
	Title   string         `json:"title"`
	Status  int            `json:"status"`
	Code    string         `json:"code"`
	Detail  string         `json:"detail,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// mapping is the fixed sentinel registry from interfaces.md §0. Adding an
// entry happens together with the interfaces.md change, never ad hoc.
var mapping = []struct {
	sentinel error
	status   int
	code     string
}{
	{apperrors.ErrNotFound, http.StatusNotFound, "not_found"},
	{apperrors.ErrVersionSkew, http.StatusConflict, "version_skew"},
	{apperrors.ErrConflict, http.StatusConflict, "conflict"},
	{apperrors.ErrPermissionDenied, http.StatusForbidden, "permission_denied"},
	{apperrors.ErrScopeExceeded, http.StatusForbidden, "scope_exceeds_grantor"},
	{apperrors.ErrRequiresApproval, http.StatusForbidden, "requires_approval"},
	{apperrors.ErrApprovalTokenInvalid, http.StatusForbidden, "approval_token_invalid"},
	{apperrors.ErrSeatTierInsufficient, http.StatusForbidden, "seat_tier_insufficient"},
	{apperrors.ErrAgentSurfaceRestricted, http.StatusForbidden, "agent_surface_restricted"},
	{apperrors.ErrConsentNotGranted, http.StatusConflict, "consent_not_granted"},
	{apperrors.ErrBudgetExceeded, http.StatusTooManyRequests, "rate_limited"},
	{apperrors.ErrModeNotOverlay, http.StatusNotFound, "mode_not_overlay"},
	{apperrors.ErrUnsupportedBySoR, http.StatusUnprocessableEntity, "unsupported_by_sor"},
	{apperrors.ErrIncumbentAlreadyConnected, http.StatusConflict, "incumbent_already_connected"},
	{apperrors.ErrOverlayFlipBlocked, http.StatusConflict, "overlay_flip_blocked"},
	{apperrors.ErrIncumbentBudgetExhausted, http.StatusServiceUnavailable, "incumbent_budget_exhausted"},
}

// Write maps err onto the wire. Unknown errors become an opaque 500 — the
// cause is logged server-side, never leaked to the client.
func Write(w http.ResponseWriter, r *http.Request, err error) {
	var withDetails *DetailedError
	if errors.As(err, &withDetails) {
		writeProblem(w, problem{
			Status:  withDetails.Status,
			Code:    withDetails.Code,
			Detail:  withDetails.Detail,
			Details: withDetails.Details,
		})
		return
	}

	for _, m := range mapping {
		if errors.Is(err, m.sentinel) {
			writeProblem(w, problem{Status: m.status, Code: m.code, Detail: err.Error()})
			return
		}
	}

	slog.ErrorContext(r.Context(), "unhandled error", "method", r.Method, "path", r.URL.Path, "err", err)
	writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "internal"})
}

// DetailedError carries a non-sentinel wire shape: validation errors
// (422 with field errors), duplicate conflicts (409 with existing_id),
// auth failures. Constructed by handlers, mapped here.
type DetailedError struct {
	Status  int
	Code    string
	Detail  string
	Details map[string]any
}

func (e *DetailedError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }

// Unauthorized is the shared 401.
func Unauthorized(w http.ResponseWriter, r *http.Request, detail string) {
	writeProblem(w, problem{Status: http.StatusUnauthorized, Code: "unauthorized", Detail: detail})
}

// NotImplemented marks a contract operation that exists on the surface
// but has no implementation yet — explicit 501, never a silent 404.
func NotImplemented(w http.ResponseWriter, r *http.Request, op string) {
	writeProblem(w, problem{
		Status: http.StatusNotImplemented,
		Code:   "not_implemented",
		Detail: fmt.Sprintf("operation %s is specified but not yet implemented", op),
	})
}

// Validation is the 422 shape with per-field errors.
func Validation(field, code, message string) *DetailedError {
	return &DetailedError{
		Status: http.StatusUnprocessableEntity,
		Code:   "validation_error",
		Detail: message,
		Details: map[string]any{
			"errors": []map[string]string{{"field": field, "code": code, "message": message}},
		},
	}
}

// Duplicate is the 409 dedupe shape. existingID is included only when
// known AND disclosable — a conflict with a row outside the caller's
// row scope answers 409 without the id.
func Duplicate(code, existingID string) *DetailedError {
	e := &DetailedError{
		Status: http.StatusConflict,
		Code:   code,
		Detail: "a live record with this key already exists",
	}
	if existingID != "" {
		e.Details = map[string]any{"existing_id": existingID}
	}
	return e
}

func writeProblem(w http.ResponseWriter, p problem) {
	if p.Type == "" {
		p.Type = problemTypeBase + p.Code
	}
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
