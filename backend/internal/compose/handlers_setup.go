// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/ratelimit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// The claim surface (ADR-0105). It sits on the operational mux beside the
// health probes rather than under /v1, for the reason that shapes everything
// else here: /v1 is fronted by the session middleware, which resolves the
// singleton organization first and answers 503 when there is none. An endpoint
// whose entire purpose is to run when no organization exists cannot live behind
// a gate that requires one.
//
// It is unauthenticated because there is nobody to authenticate yet — the setup
// token is the credential, and without it the route creates nothing.

// setupStatusResponse tells a caller whether this installation is waiting to be
// claimed. It discloses no token and no organization detail: a stranger already
// learns as much from any request that answers 503.
// setupLimiter throttles the pre-tenant edge per client IP, the way every other
// unauthenticated edge on this mux does. Without it an anonymous caller opens a
// transaction and (before the short-circuit in ClaimInstallation) queued on the
// installation advisory lock on every request — a pool connection held for
// free, and the operator's one legitimate claim contending in the same queue.
//
// 20/min is below the booking edge's 60: a human claims an installation once,
// and a client retrying more than that is not the case being served.
func newSetupLimiter() *ratelimit.Limiter { return ratelimit.New(20, time.Minute) }

// setupClaimResponse names the organization a claim created, so the caller
// can go straight to signing in rather than probing for it.
type setupClaimResponse struct {
	WorkspaceID string `json:"workspace_id"`
}

type setupStatusResponse struct {
	Claimable bool `json:"claimable"`
}

// Field names are snake_case: these routes are hand-written and outside
// crm.yaml, so they follow the convention the linter enforces on hand-written
// Go rather than the camelCase the generated contract types carry.
type setupClaimRequest struct {
	SetupToken       string `json:"setup_token"`
	OrganizationName string `json:"organization_name"`
	Timezone         string `json:"timezone"`
	BaseCurrency     string `json:"base_currency"`
	AdminEmail       string `json:"admin_email"`
	AdminName        string `json:"admin_name"`
	AdminPassword    string `json:"admin_password"`
}

// setupStatus answers whether a claim is possible.
func setupStatus(svc *identity.Service, limit *ratelimit.Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !limit.Allow(httpserver.ClientIP(r)) {
			httperr.Write(w, r, apperrors.ErrBudgetExceeded)
			return
		}
		outstanding, err := svc.SetupTokenOutstanding(r.Context())
		if err != nil {
			httperr.Write(w, r, err)
			return
		}
		httperr.WriteJSON(w, http.StatusOK, setupStatusResponse{Claimable: outstanding})
	}
}

// setupClaim creates the organization and its first admin from a claim.
func setupClaim(svc *identity.Service, pool *pgxpool.Pool, seeds deployconfig.Seeds, limit *ratelimit.Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !limit.Allow(httpserver.ClientIP(r)) {
			httperr.Write(w, r, apperrors.ErrBudgetExceeded)
			return
		}
		var in setupClaimRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			httperr.Write(w, r, httperr.Validation("body", "malformed", "the claim body is not the expected JSON object"))
			return
		}
		if in.SetupToken == "" {
			// Refused here rather than by the consume path, which would
			// otherwise report a missing token as a mismatched one.
			httperr.Unauthorized(w, r, "a setup token is required to claim this installation")
			return
		}

		wsID, err := svc.ClaimInstallation(r.Context(), in.SetupToken, identity.InstallationBootstrap{
			OrganizationName: in.OrganizationName,
			BaseCurrency:     in.BaseCurrency,
			Timezone:         in.Timezone,
			AdminEmail:       in.AdminEmail,
			AdminName:        in.AdminName,
			AdminPassword:    in.AdminPassword,
		}, configuredSeed(seeds, deals.NewHandlers(InstallationDB(pool), DealsInstallation())))
		switch {
		case errors.Is(err, identity.ErrAlreadyProvisioned):
			// The true reason, not a token failure: a caller holding a valid
			// token deserves it, and that an installation is provisioned is
			// already visible from any other request.
			httperr.Write(w, r, fmt.Errorf("%w: this installation already has an organization; a claim is possible exactly once", apperrors.ErrConflict))
			return
		case errors.Is(err, identity.ErrSetupTokenMismatch):
			// Deliberately one answer for "wrong token" and "no token
			// outstanding": distinguishing them tells an unauthenticated
			// caller whether guessing is worth their time.
			httperr.Unauthorized(w, r, "the setup token is not valid for this installation")
			return
		case err != nil:
			httperr.Write(w, r, err)
			return
		}
		httperr.WriteJSON(w, http.StatusCreated, setupClaimResponse{WorkspaceID: wsID.String()})
	}
}
