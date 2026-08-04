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
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
	// The detail names which field and why through the SAME restatement the
	// native body decode uses, so both paths say one thing about one mistake,
	// and it ends with what the caller should DO — which a decoder message on
	// its own never says. Whatever it had to withhold is logged by Write.
	var badFields *datasource.FieldDecodeError
	if errors.As(err, &badFields) {
		detail, _ := fieldDecodeRefusal(badFields.Cause)
		return Validation("fields", "invalid_field", detail), true
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

	// A module's own typed refusal, carrying its verdict on the error itself
	// (apperrors.FieldFault). LAST on purpose: every branch above names the
	// same shape more precisely for a type this one would also match, so the
	// specific mapping wins and this is the general fallback.
	//
	// This is what lets a module-owned refusal answer identically on a surface
	// that never runs that module's HTTP mapper — the MCP tool surface reaches
	// the same stores through the datasource seam, and used to report every one
	// of these as an internal fault with advice to retry.
	return moduleDeclaredFault(err)
}

// moduleDeclaredFault answers the errors that carry their OWN verdict, through
// one of apperrors' three fault interfaces. Split from clientInputValidation
// because the two halves answer different questions: that one knows a fixed set
// of shared types by name, while this one knows no types at all — a module opts
// in by implementing a method, and this reads whatever it declared.
// maxFaultText bounds each value a module-declared fault contributes to a
// caller-facing body. The interfaces are implemented across eight modules and
// their docs ask for no internal detail, but a boundary that only asks is a
// boundary that leaks the first time someone passes a constraint name or a
// wrapped driver message through. Long enough for a real explanation, short
// enough that nothing large rides out.
const maxFaultText = 300

// boundFaultText caps one caller-facing value from a module-declared fault.
func boundFaultText(s string) string {
	if len(s) <= maxFaultText {
		return s
	}
	cut := maxFaultText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func moduleDeclaredFault(err error) (error, bool) {
	// The plural first: a type that names several bad inputs has nothing
	// useful to say as a single field, so asking it for one would discard the
	// rest of what it knows.
	var fieldFaults apperrors.FieldFaults
	if errors.As(err, &fieldFaults) {
		refusals := fieldFaults.FieldFaults()
		fields := make([]FieldError, 0, len(refusals))
		for _, r := range refusals {
			fields = append(fields, FieldError{
				Field:   boundFaultText(r.Field),
				Code:    boundFaultText(r.Code),
				Message: boundFaultText(r.Message),
			})
		}
		return &DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "validation_error",
			Detail: fieldFaults.Error(),
			Fields: fields,
		}, true
	}

	var fieldFault apperrors.FieldFault
	if errors.As(err, &fieldFault) {
		field, code, message := fieldFault.FieldFault()
		return Validation(boundFaultText(field), boundFaultText(code), boundFaultText(message)), true
	}

	// A refusal with no field to name: it still must classify, or the MCP
	// surface reports a governed condition as an internal fault — but it
	// carries NO per-field entry, because inventing one would point the caller
	// at an input that is not theirs to change.
	var messageFault apperrors.MessageFault
	if errors.As(err, &messageFault) {
		code, message := messageFault.MessageFault()
		return &DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   boundFaultText(code),
			Detail: boundFaultText(message),
		}, true
	}

	return nil, false
}

// Fault is the surface-independent verdict on an error: the status the
// contract answers with, its stable machine code, and the detail that is safe
// to put in front of a caller. Write renders it as RFC 7807 for the REST
// surface; the MCP tool dispatcher renders the same verdict as prose an agent
// can act on. The status is the only HTTP-shaped part, and Transient reads it
// so that no other surface has to.
type Fault struct {
	Status  int
	Code    string
	Detail  string
	Details map[string]any

	// Fields is the per-field breakdown of a validation refusal, empty for
	// every other class. It names the inputs the caller must change.
	Fields []FieldError

	// InfraCause is the raw infrastructure failure (Postgres, network) that a
	// sentinel wrapped. Detail never carries its text — SQL fragments and
	// addresses are operator reading, not caller reading — so the surface
	// logs this instead of showing it.
	InfraCause error
}

// transientCodes are the refusals that clear ON THEIR OWN: a rate limit whose
// window elapses, a metered budget that refills. Membership is by CODE and not
// by status range, because the range lies — a 503 is equally the shape of
// "not bootstrapped yet" or "misconfigured", which no amount of retrying
// resolves and which an operator must act on. Telling an agent to wait for one
// of those spends its whole step budget on a call that was already settled.
var transientCodes = map[string]struct{}{
	"rate_limited":               {},
	"incumbent_budget_exhausted": {},
}

// Transient reports whether repeating the same call unchanged could succeed
// later. Every other classified fault is settled: something must change (the
// arguments, a human's decision, an operator's) before a retry means anything.
func (f Fault) Transient() bool {
	_, ok := transientCodes[f.Code]
	return ok
}

// Classify is the ONE decision tree behind the error taxonomy: what err means
// to a caller, independent of the surface that caller reached us through. It
// reports false only for an error outside the taxonomy — a genuine server
// fault, whose text never crosses to a client.
//
// Every surface classifies HERE. The REST mapper and the MCP tool dispatcher
// each used to carry their own copy of this judgement, and the copies
// disagreed: a malformed argument was a 422 naming the offending field on
// REST, and "the tool failed for an internal reason; retry" on MCP — advice
// that could never succeed, for a mistake the caller could have fixed had we
// named it. One tree and two renderers means the surfaces cannot drift apart
// again.
func Classify(err error) (Fault, bool) {
	var withDetails *DetailedError
	if errors.As(err, &withDetails) {
		return Fault{
			Status:  withDetails.Status,
			Code:    withDetails.Code,
			Detail:  withDetails.Detail,
			Details: withDetails.Details,
			Fields:  withDetails.Fields,
		}, true
	}

	if v, ok := clientInputValidation(err); ok {
		return Classify(v)
	}

	for _, m := range mapping {
		if errors.Is(err, m.sentinel) {
			f := Fault{Status: m.status, Code: m.code, Detail: err.Error()}
			// A sentinel wrapped around an infrastructure failure must not
			// carry that failure's text to a caller. It gets the sentinel's
			// canonical detail; the full cause goes to the surface's log.
			if infrastructureCause(err) {
				f.Detail = m.sentinel.Error()
				f.InfraCause = err
			}
			return f, true
		}
	}

	return Fault{}, false
}

// Write maps err onto the wire. Unknown errors become an opaque 500 — the
// cause is logged server-side, never leaked to the client.
func Write(w http.ResponseWriter, r *http.Request, err error) {
	fault, ok := Classify(err)
	if !ok {
		slog.ErrorContext(r.Context(), "unhandled error", "method", r.Method, "path", r.URL.Path, "err", err)
		writeProblem(w, problem{Status: http.StatusInternalServerError, Code: "internal"})
		return
	}
	if fault.InfraCause != nil {
		slog.ErrorContext(r.Context(), "sentinel wrapped an infrastructure error",
			"method", r.Method, "path", r.URL.Path, "err", fault.InfraCause)
	}
	// A refusal that masked a library's own sentence still owes the operator
	// that sentence, exactly as the native body decode logs its unnamed shapes.
	if withheld := withheldFieldDecodeCause(err); withheld != nil {
		slog.WarnContext(r.Context(), "unnamed field-decode failure",
			"method", r.Method, "path", r.URL.Path, "err", withheld)
	}
	// Fields renders INTO details rather than over it: both are public on
	// DetailedError, so a handler may legitimately carry extra structured
	// context alongside a per-field breakdown, and dropping that context on
	// the floor would be a silent loss of exactly the kind this change exists
	// to stop.
	details := fault.Details
	if len(fault.Fields) > 0 {
		merged := make(map[string]any, len(details)+1)
		for k, v := range details {
			merged[k] = v
		}
		merged[fieldErrorsKey] = fieldDetails(fault.Fields)[fieldErrorsKey]
		details = merged
	}
	writeProblem(w, problem{
		Status:  fault.Status,
		Code:    fault.Code,
		Detail:  fault.Detail,
		Details: details,
	})
}

// infrastructureCause reports whether err's chain contains a raw
// infrastructure failure (Postgres, network) whose message is meant for
// operators, not clients.
func infrastructureCause(err error) bool {
	var pgErr *pgconn.PgError
	var netErr net.Error
	return errors.As(err, &pgErr) || errors.As(err, &netErr)
}

// FieldError is one per-field refusal inside a validation fault: which input
// the caller got wrong, the contract's stable code for it, and what to fix.
// It is typed rather than left in the Details bag because it is the ACTIONABLE
// half of a 422 — the surfaces render it (REST into the problem body, MCP into
// the sentence an agent reads), and a surface cannot render what it has to
// guess the shape of.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// DetailedError carries a non-sentinel wire shape: validation errors
// (422 with field errors), duplicate conflicts (409 with existing_id),
// auth failures. Constructed by handlers, mapped here.
type DetailedError struct {
	Status  int
	Code    string
	Detail  string
	Details map[string]any

	// Fields is the per-field breakdown of a validation refusal. It is the
	// single source for that breakdown: the wire body is rendered FROM it, so
	// a surface reading Fields and a client reading the body can never be
	// looking at two different lists.
	Fields []FieldError
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
