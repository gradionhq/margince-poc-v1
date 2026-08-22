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

// FieldRefusal is one entry in a multi-field refusal.
type FieldRefusal struct {
	Field   string
	Code    string
	Message string
}

// FieldFaults is FieldFault's plural: a refusal that names SEVERAL bad inputs
// at once, which a schema validator naturally produces. Collapsing such an
// error into one field would hide the rest, so it reports them all and the
// choke point renders every entry.
//
// A type implements one or the other, never both. The plural is checked first,
// because a type carrying a list has nothing useful to say as a single field.
type FieldFaults interface {
	error
	FieldFaults() []FieldRefusal
}

// MessageFault is for a refusal that is legitimately the caller's business to
// know about but names NO input the caller can change: an authority or
// configuration state (no send-capable mailbox), or server-side data the
// workspace has not loaded (no FX rate for a currency pair).
//
// It exists because FieldFault was applied to those cases first, and pointing
// at a field is worse than pointing at nothing when the field is not an
// argument of the operation: an agent told to fix `from` on a send that has no
// `from` argument has been handed a task it cannot perform, and will either
// retry unchanged or invent a value. Naming the condition and saying a human
// must act on it is the honest answer.
//
// A type implements exactly one of the three fault forms.
type MessageFault interface {
	error
	// MessageFault returns the contract's machine code for the condition and a
	// message saying who has to do what. Neither may carry internal detail.
	MessageFault() (code, message string)
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

	// ErrSeatLimitReached: this INSTALLATION has no licensed full seat left,
	// so a seat may not be created (403 seat_limit_reached).
	//
	// Distinct from ErrSeatTierInsufficient, which is about the CALLER's own
	// seat and never clears by an admin's action, and from ErrBudgetExceeded,
	// which is metered and refills on its own — nothing about this one clears
	// with time, and telling a caller to retry would be advice that can only
	// ever fail. It is the licensed ceiling (A36/ADR-0029), not a quota.
	ErrSeatLimitReached = errors.New("seat limit reached")
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

// ErrBaseCurrencyLocked is the currency-substrate sentinel (interfaces.md §0,
// A130/ADR-0085). Every closed
// deal freezes a conversion rate AGAINST the workspace base currency, so once
// one exists the base can no longer change without silently reinterpreting what
// those deals were worth. Distinct from ErrVersionSkew, which means somebody
// else changed the row, and from ErrPermissionDenied, which means you may never.
var ErrBaseCurrencyLocked = errors.New("base currency is locked by frozen conversion rates")

// ErrRetentionHold is the statutory-restriction sentinel (interfaces.md §0,
// A165/ADR-0114, A167/ADR-0116): a delete or mutation of a record held under
// a retention obligation, refused for EVERY role including admin because the
// immutability is a data-layer guarantee no seat clears (DEPACK-AC-5a). It
// says nobody may, until a date — distinct from ErrPermissionDenied (you may
// not, another principal might) and ErrVersionSkew (somebody else changed the
// row) — so a caller must never retry it; the one way past it is the audited
// release, which is its own operation.
var ErrRetentionHold = errors.New("record is held under a statutory retention obligation")

// ErrProviderUnusable says a request to an outside service produced no usable
// answer: it could not be reached, it refused, or what it sent back could not
// be read.
//
// The three are one condition FOR THE CALLER, which is why they share a
// sentinel. None of them says anything about the subject — the address, the
// account, the document — so none may be recorded as a fact about it, and all
// of them are worth asking again. The distinction that matters is against a
// service that ANSWERED: "this address is not a place" is a fact, recorded
// once and never re-asked, and conflating that with a failed request is how a
// job either retries a settled question forever or gives up on a transient one.
//
// It is core rather than module-local because the caller's DECISION is core:
// jobs.FaultContext publishes a classified sentence only for a sentinel this
// package declares. Left unclassified, the failure reached the job row as
// "the diagnosis is in the process log" — true, and useless once the process
// has restarted, which is exactly when an operator goes looking.
var ErrProviderUnusable = errors.New("the provider returned no usable answer")
