// Package apperrors is the fixed error-sentinel registry from
// contract/interfaces.md §0. Callers branch with errors.Is; the HTTP and
// MCP choke-points own the mapping to wire shapes (RFC 7807 / tool errors)
// so no handler ever hand-writes a status body.
//
// Adding a sentinel is rare and lands in interfaces.md §0 in the same
// change, together with its HTTP and MCP mapping.
package apperrors

import "errors"

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

	// ErrAgentSurfaceRestricted: an agent passport attempted a MUTATING REST
	// call. Agent mutations must flow through the governed MCP tool surface,
	// where scope ∧ tier ∧ the 🟡 approval gate apply (platform/auth); the
	// REST surface is read-only for passports so there is exactly one agent
	// mutation choke point (403 agent_surface_restricted). ADR-0013's
	// "same REST surface as everyone else" language predates the gate and
	// needs reconciling — tracked as ../fable feedback/18; registered here
	// pending the spec update. See ../decisions/0010.
	ErrAgentSurfaceRestricted = errors.New("agent surface restricted")
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
