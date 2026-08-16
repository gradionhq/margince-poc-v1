// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Admission: how a request becomes a principal, and the caps that are applied
// before RBAC ever runs.
//
// Three doors, one per kind of caller — an agent presenting a passport, a human
// presenting a session, and the consent entry point that serves either. What
// they share is the order: authenticate, then apply the ceilings that hold
// whatever a role would otherwise grant (the seat tier, and an account still
// using a password its operator chose), then hand on.

import (
	"context"
	"errors"
	"net/http"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// serveAsAgent admits a passport bearer under the agent principal. ctx is
// the workspace-resolved context; it lands on the request exactly once,
// at the hand-off to next.
func (h Handlers) serveAsAgent(ctx context.Context, w http.ResponseWriter, r *http.Request, next http.Handler, bearer string) {
	agent, err := h.svc.AuthenticateAgent(ctx, bearer)
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			httperr.Unauthorized(w, r, "passport expired, revoked or unknown")
			return
		}
		httperr.Write(w, r, err)
		return
	}
	if !isMutating(r.Method) && !agent.Scopes.Has(principal.ScopeRead) {
		httperr.Write(w, r, apperrors.ErrScopeExceeded)
		return
	}
	next.ServeHTTP(w, r.WithContext(principal.WithActor(ctx, agent.Principal())))
}

// serveAsHuman resolves the session cookie to a human principal and
// enforces the seat ceiling before the request reaches RBAC. ctx is the
// workspace-resolved context; it lands on the request exactly once, at
// the hand-off to next.
func (h Handlers) serveAsHuman(ctx context.Context, w http.ResponseWriter, r *http.Request, next http.Handler) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		httperr.Unauthorized(w, r, "missing session cookie")
		return
	}
	id, err := h.svc.Authenticate(ctx, cookie.Value)
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			httperr.Unauthorized(w, r, "session expired or revoked")
			return
		}
		httperr.Write(w, r, err)
		return
	}

	// The seat ceiling is a licensing cap enforced before RBAC
	// (A62/ADR-0047): a read seat may read but never mutate over REST,
	// whatever its role grants. Method-based — the contract has no
	// mutating GET.
	if id.SeatType == string(principal.SeatRead) && isMutating(r.Method) && !isOwnCredentialRequest(r) {
		httperr.Write(w, r, apperrors.ErrSeatTierInsufficient)
		return
	}

	// An account still on the password an OPERATOR chose reaches exactly one
	// route: the one that replaces it. Enforced HERE rather than by the client
	// honouring a field in the login response, because a gate a client can
	// decline to apply is not a gate — and this is the whole substance of the
	// requirement, not a hint to the UI.
	//
	// Reads are refused too. The account is authenticated but not yet the
	// person's own: until they choose a credential, everything it can see is
	// still visible to whoever holds the one the operator typed into a file.
	if id.MustChangePassword && !isOwnCredentialRequest(r) {
		httperr.Write(w, r, forcedRotationRefusal())
		return
	}

	next.ServeHTTP(w, r.WithContext(withHumanPrincipal(ctx, id)))
}

// withHumanPrincipal binds one authenticated human onto the context: the
// identity the module's own handlers read and the kernel principal every store
// gate reads. One spelling for both hand-offs below, so a session admitted by
// either arrives as the same principal.
func withHumanPrincipal(ctx context.Context, id Identity) context.Context {
	return principal.WithActor(withIdentity(ctx, id), principal.Principal{
		Type:        principal.PrincipalHuman,
		ID:          "human:" + id.UserID.String(),
		UserID:      id.UserID.UUID,
		TeamIDs:     rawTeamIDs(id.Teams),
		SeatType:    principal.SeatType(id.SeatType),
		Permissions: id.Permissions,
	})
}

// serveAsOptionalHuman serves the consent entry point (isConsentEntry,
// middleware.go) with whatever session the browser has, including none: a
// signed-in human reaches the handler as themselves, and a human who is not
// signed in reaches it as nobody — which is the case the handler answers with a
// redirect to the login screen.
//
// "Not signed in" deliberately covers an expired or revoked session too. The
// human's situation is identical (they must sign in again) and so is the answer,
// while a 401 would strand exactly the human whose consent screen sat open too
// long. Nothing is admitted by it: this route hands an unidentified caller a
// redirect and nothing else, and the seat ceiling has no bearing on a GET.
//
// A session that cannot be RESOLVED — a database failure, not a dead session —
// is still an error. Reporting it as "not signed in" would send a human into a
// login loop against an installation that cannot authenticate anyone.
func (h Handlers) serveAsOptionalHuman(ctx context.Context, w http.ResponseWriter, r *http.Request, next http.Handler) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		next.ServeHTTP(w, r.WithContext(ctx))
		return
	}
	id, err := h.svc.Authenticate(ctx, cookie.Value)
	if errors.Is(err, apperrors.ErrNotFound) {
		next.ServeHTTP(w, r.WithContext(ctx))
		return
	}
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	// The forced rotation binds here too. This door is a GET, so the seat
	// ceiling has no bearing on it, but arming a consent nonce for an account
	// still on an operator's password would start a grant that the POST
	// decision then refuses — a dead end at the approve click, on behalf of a
	// credential its holder never chose. Falling through as "not signed in"
	// would be worse: it sends them to a login they can complete and land back
	// here from, forever.
	if id.MustChangePassword {
		httperr.Write(w, r, forcedRotationRefusal())
		return
	}
	next.ServeHTTP(w, r.WithContext(withHumanPrincipal(ctx, id)))
}

// isMutating is the transport-level write test the agent and read-seat
// ceilings share: everything that is not a safe read method mutates. The
// contract exposes no read-over-POST endpoint (searches are GET), so the
// method alone is authoritative here.
//
// The method decides only whether a call mutates, never WHICH cap it
// spends: a mutation's cap is declared per operation in the contract
// (`x-mcp-tool.scope`) and admitted in the ADR-0055 gate, so a `send` or
// an `enrich` over REST is not reachable on `write` just because it
// arrived as a POST.
func isMutating(method string) bool {
	return method != http.MethodGet && method != http.MethodHead
}
