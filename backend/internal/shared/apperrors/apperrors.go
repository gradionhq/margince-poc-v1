// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package apperrors is the fixed error-sentinel registry from
// contract/interfaces.md §0. Callers branch with errors.Is; the HTTP and
// MCP choke-points own the mapping to wire shapes (RFC 7807 / tool errors)
// so no handler ever hand-writes a status body.
//
// Adding a sentinel is rare and lands in interfaces.md §0 in the same
// change, together with its HTTP and MCP mapping.
package apperrors

import "errors"

// FieldFault is implemented by a typed error that refuses a SPECIFIC input:
// it names the contract field the caller must change, the contract's stable
// machine code for the refusal, and what to fix.
//
// It exists because the verdict belongs to the error, not to the transport
// that happens to be carrying it. Every module used to spell its own typed
// refusals out again in an HTTP-side mapper (`writeStoreErr` and friends), so
// a refusal was a 422 naming the field on REST and — on the MCP tool surface,
// which reaches the same stores through the datasource seam and never runs
// those mappers — an unclassified error reported as an internal server fault
// with advice to retry. Implementing this instead makes the refusal legible
// wherever it travels, including to a surface that did not exist when the
// error type was written.
//
// A module opts in by adding the method; httperr's choke point does the rest.
// The narrower typed errors in shared (values.ParseError, storekit's list
// vocabularies, datasource's seam refusals) keep their own dedicated branches,
// which take precedence over this one.
type FieldFault interface {
	error
	// FieldFault returns the offending field's contract path, the contract's
	// machine code for this refusal, and a message saying what to fix. All
	// three reach the caller, so none of them may carry internal detail.
	FieldFault() (field, code, message string)
}

// Core sentinels — every store and handler in the system speaks these.
var (
	// ErrNotFound: no such resource in this workspace, or outside the
	// caller's RBAC scope (the two are indistinguishable by design).
	ErrNotFound = errors.New("not found")

	// ErrConflict: a state or dedupe conflict, e.g. the 409 duplicate-email
	// path (data-model §3.2).
	ErrConflict = errors.New("conflict")

	// ErrScopeExceeded: a tool or verb outside the Passport scope — an
	// agent may never exceed the granting human (403 scope_exceeds_grantor).
	ErrScopeExceeded = errors.New("scope exceeds grantor")

	// ErrPermissionDenied: object-level RBAC denial — the actor's role
	// grants no such action on this object type (403 permission_denied).
	// Distinct from ErrScopeExceeded (a Passport ceiling) and from a
	// row-scope miss, which answers ErrNotFound by design. Upstream
	// interfaces.md §0 has no sentinel for this case — tracked as
	// ../fable feedback/14; registered here pending the spec update.
	ErrPermissionDenied = errors.New("permission denied")

	// ErrRequiresApproval: a 🟡 confirm-first action was attempted without
	// a valid approval token; the action is staged, never executed.
	ErrRequiresApproval = errors.New("requires human approval")

	// ErrVersionSkew: optimistic-concurrency failure — the row's version
	// no longer matches If-Match (409 version_skew, ADR-0036).
	ErrVersionSkew = errors.New("version skew")

	// ErrBudgetExceeded: a session or agent quota ran out
	// (api-rate-limits §2; 429-equivalent).
	ErrBudgetExceeded = errors.New("budget exceeded")

	// ErrApprovalTokenInvalid: an approval token was expired, consumed, or
	// bound to a different workspace/passport/tool/diff (ADR-0036).
	ErrApprovalTokenInvalid = errors.New("approval token invalid")

	// ErrConsentNotGranted: an outbound action was suppressed because no
	// active, proven consent exists for the purpose (409 consent_not_granted).
	ErrConsentNotGranted = errors.New("consent not granted")

	// ErrSeatTierInsufficient: a read seat — or an agent acting for one —
	// attempted a mutate/send/approve/grant (403 seat_tier_insufficient,
	// A62/ADR-0047).
	ErrSeatTierInsufficient = errors.New("seat tier insufficient")
)

// Overlay sentinels — only reachable when workspace.sor_mode = overlay
// (interfaces.md §0, 03e). Registered now so the mapping table is complete;
// the overlay work package supplies the callers.
var (
	ErrModeNotOverlay            = errors.New("workspace is not in overlay mode")
	ErrUnsupportedBySoR          = errors.New("unsupported by system of record")
	ErrIncumbentAlreadyConnected = errors.New("incumbent already connected")
	ErrOverlayFlipBlocked        = errors.New("overlay flip preflight unsatisfied")
	ErrIncumbentBudgetExhausted  = errors.New("incumbent API budget exhausted")
)
