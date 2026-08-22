// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httperr

// The ready-made refusals: the handful of responses a surface writes directly
// rather than by classifying an error it was handed.
//
// Split from httperr.go at the 500-line cap. The two halves answer different
// questions — that one asks "what does THIS error mean on the wire", this one
// is the shorthand for a refusal a handler already knows the shape of — so the
// seam is a concept rather than a line count.

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

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
		Fields: []FieldError{{Field: field, Code: code, Message: message}},
	}
}

// RequireBodyID refuses a required body id the caller simply omitted, naming the
// wire field. Nil when the id is present.
//
// The defect it closes is the generator's: oapi-codegen renders a REQUIRED body
// id as a non-pointer UUID, and encoding/json leaves an absent key at the zero
// value with no error. So "required" in the contract is a claim only this check
// makes true — and what made it worth a named helper is where the zero value
// LANDS. It reaches a lookup or a link-target probe, matches no row, and comes
// back as a bare not-found: the caller is told a record it never mentioned does
// not exist, on a request whose real fault was an absent key.
//
// It lives here rather than per-module because Classify matches *DetailedError
// before anything else, so one call answers a 422 naming the field on REST AND
// the same field-named sentence on the MCP tool surface — which never runs a
// module's HTTP mapper. A module error type per caller would be a second
// spelling of a rule whose wire output is byte-identical.
//
// The id arrives as ids.UUID: this package deliberately does not import the
// generated contracts, so the openapi_types.UUID conversion happens at the call
// site, where the contract type legally lives.
func RequireBodyID(field string, id ids.UUID) error {
	if id.IsZero() {
		return Validation(field, "required", field+" is required")
	}
	return nil
}

// fieldDetails renders the per-field breakdown into the contract's
// `details.errors` body shape. Rendering it here, from Fields, is what keeps
// the typed list and the wire list the same list.
const fieldErrorsKey = "errors"

func fieldDetails(fields []FieldError) map[string]any {
	errs := make([]map[string]string, 0, len(fields))
	for _, f := range fields {
		entry := map[string]string{"field": f.Field, "code": f.Code}
		// A multi-field validator may have no per-entry prose (the code IS the
		// reason). Omitting the key beats shipping "message": "", which reads
		// as an explanation that came out blank.
		if f.Message != "" {
			entry["message"] = f.Message
		}
		errs = append(errs, entry)
	}
	return map[string]any{fieldErrorsKey: errs}
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
