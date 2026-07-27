// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

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
	"net"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
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
	{apperrors.ErrRequiresApproval, http.StatusForbidden, "approval_required"},
	{apperrors.ErrApprovalTokenInvalid, http.StatusForbidden, "approval_token_invalid"},
	{apperrors.ErrSeatTierInsufficient, http.StatusForbidden, "seat_tier_insufficient"},
	{apperrors.ErrConsentNotGranted, http.StatusConflict, "consent_not_granted"},
	{apperrors.ErrBudgetExceeded, http.StatusTooManyRequests, "rate_limited"},
	{apperrors.ErrModeNotOverlay, http.StatusNotFound, "mode_not_overlay"},
	{apperrors.ErrUnsupportedBySoR, http.StatusUnprocessableEntity, "unsupported_by_sor"},
	{apperrors.ErrIncumbentAlreadyConnected, http.StatusConflict, "incumbent_already_connected"},
	{apperrors.ErrOverlayFlipBlocked, http.StatusConflict, "overlay_flip_blocked"},
	{apperrors.ErrIncumbentBudgetExhausted, http.StatusServiceUnavailable, "incumbent_budget_exhausted"},
}

// clientInputValidation maps the typed errors that mean "the CALLER got the
// request wrong" onto their contract validation shape — each carrying its own
// field and machine code, so the client is told which input to fix rather than
// being handed a generic 422. It reports false for anything else, leaving the
// sentinel table (and ultimately the opaque 500) to answer.
//
// They are collected in one place because they share a single decision: none
// of them is a server fault, so none of them may reach the unhandled-error
// branch — a client mistake answered as a 500 sends the caller looking for an
// outage that is not there.
func clientInputValidation(err error) (error, bool) {
	// The keyset cursor is client input: a token that fails to decode is
	// the caller's fault, same 422 shape as every other bad query input.
	var badCursor *storekit.MalformedCursorError
	if errors.As(err, &badCursor) {
		return Validation("cursor", "malformed_cursor", "cursor is not a valid page token"), true
	}

	// A cursor that decodes but was minted under a different sort carries
	// the contract's dedicated code — the caller re-issues the query
	// without the cursor (or under the sort it was minted with).
	var cursorMismatch *storekit.CursorSortMismatchError
	if errors.As(err, &cursorMismatch) {
		return Validation("cursor", "cursor_param_mismatch",
			"cursor was minted under a different sort; re-issue the query without the cursor"), true
	}

	// The list vocabularies' typed refusals (data-model §13.5): a sort
	// spec or filter leaf outside the resource's closed vocabulary carries
	// its own field and machine code — one wire mapping, like the cursor's.
	var badSort *storekit.SortError
	if errors.As(err, &badSort) {
		return Validation("sort", badSort.Code, badSort.Message), true
	}
	var badPredicate *storekit.PredicateError
	if errors.As(err, &badPredicate) {
		return Validation(badPredicate.Field, badPredicate.Code, badPredicate.Message), true
	}

	// A value object refused to parse: client input in the wrong format,
	// carrying its own field and machine code — the parse-don't-validate
	// seam's single wire mapping.
	var badValue *values.ParseError
	if errors.As(err, &badValue) {
		return Validation(badValue.Field, badValue.Code, badValue.Message), true
	}

	// A write payload the datasource seam refused to decode — an unknown or
	// misspelled field, or a value of the wrong type. datasource's own doc
	// states it maps to 422 on every surface; this is the branch that makes
	// that true for HTTP rather than letting it fall through to the 500 a
	// pure client mistake must never answer with.
	//
	// The cause names which field and why, matching what httperr.Decode
	// already puts on the wire for the same class from the native path; the
	// sentence after it is what the caller should DO, which a decoder message
	// on its own never says.
	var badFields *datasource.FieldDecodeError
	if errors.As(err, &badFields) {
		return Validation("fields", "invalid_field",
			badFields.Cause.Error()+" — check the field names and value types against this operation's request schema"), true
	}

	// An entity_type no provider on this installation serves. Same obligation
	// as the decode branch above: the seam documents 422 on every surface, and
	// naming a type the installation does not serve is a client mistake, which
	// must never answer 500. The seam's message already carries the known
	// vocabulary, so it is the actionable half on its own.
	var unservedEntity *datasource.UnsupportedEntityError
	if errors.As(err, &unservedEntity) {
		return Validation("entity_type", "unsupported_entity_type", unservedEntity.Error()), true
	}

	return nil, false
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

	if v, ok := clientInputValidation(err); ok {
		Write(w, r, v)
		return
	}

	for _, m := range mapping {
		if errors.Is(err, m.sentinel) {
			detail := err.Error()
			// A sentinel wrapped around an infrastructure failure must not
			// carry that failure's text onto the wire (SQL fragments,
			// addresses). The client gets the sentinel's canonical detail;
			// the full cause goes to the server log, like any 500 would.
			if infrastructureCause(err) {
				slog.ErrorContext(r.Context(), "sentinel wrapped an infrastructure error",
					"method", r.Method, "path", r.URL.Path, "err", err)
				detail = m.sentinel.Error()
			}
			writeProblem(w, problem{Status: m.status, Code: m.code, Detail: detail})
			return
		}
	}

	slog.ErrorContext(r.Context(), "unhandled error", "method", r.Method, "path", r.URL.Path, "err", err)
	writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "internal"})
}

// infrastructureCause reports whether err's chain contains a raw
// infrastructure failure (Postgres, network) whose message is meant for
// operators, not clients.
func infrastructureCause(err error) bool {
	var pgErr *pgconn.PgError
	var netErr net.Error
	return errors.As(err, &pgErr) || errors.As(err, &netErr)
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

// ServiceUnavailable is the shared 503 for availability states — the
// installation cannot serve (e.g. not yet bootstrapped), which is an
// operator condition, never an authentication failure.
func ServiceUnavailable(w http.ResponseWriter, r *http.Request, detail string) {
	writeProblem(w, problem{Status: http.StatusServiceUnavailable, Code: "service_unavailable", Detail: detail})
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
	//craft:ignore swallowed-errors the status line is already on the wire — an encode failure here has no recovery path and no channel back to the client
	_ = json.NewEncoder(w).Encode(p)
}
