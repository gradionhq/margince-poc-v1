// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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
func setupClaim(svc *identity.Service, pool *pgxpool.Pool, seeds deployconfig.Seeds, limit *ratelimit.Limiter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !limit.Allow(httpserver.ClientIP(r)) {
			httperr.Write(w, r, apperrors.ErrBudgetExceeded)
			return
		}
		// The house decoder, not a hand-rolled one: it caps the body, refuses an
		// unknown or merely case-folded key, restates a parse error the caller
		// can act on while withholding anything else, and refuses trailing
		// content. Every one of those matters more here than elsewhere — this
		// body creates the root account, so a key quietly ignored is a field
		// quietly defaulted.
		var in setupClaimRequest
		if !httperr.Decode(w, r, &in) {
			return
		}
		if in.SetupToken == "" {
			// Refused here rather than by the consume path, which would
			// otherwise report a missing token as a mismatched one.
			httperr.Unauthorized(w, r, "a setup token is required to claim this installation")
			return
		}

		// The seed's own discards, merged below. ClaimInstallation ASSIGNS the
		// identity ones, so a shared slice would lose these.
		var seedDiscards []string
		wsID, discarded, err := svc.ClaimInstallation(r.Context(), in.SetupToken, identity.InstallationBootstrap{
			OrganizationName: in.OrganizationName,
			BaseCurrency:     in.BaseCurrency,
			Timezone:         in.Timezone,
			AdminEmail:       in.AdminEmail,
			AdminName:        in.AdminName,
			AdminPassword:    in.AdminPassword,
		}, configuredSeed(seeds, deals.NewHandlers(InstallationDB(pool), DealsInstallation()), &seedDiscards))
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
		// The claim refuses a provisioned installation, but `setting` is not
		// tenant-scoped: a claim over a database whose previous workspace was
		// archived creates a new one beside settings rows that survived, and the
		// identity the human just typed into the claim form is discarded. They
		// see the old name in the UI and have no way to tell why (#863).
		discarded = append(discarded, seedDiscards...)
		if len(discarded) > 0 {
			log.Warn("the claim kept the values already stored and discarded what was submitted; a previous installation's settings survived because they are not scoped to a workspace",
				"discarded_keys", strings.Join(discarded, ", "), "workspace_id", wsID.String())
		}
		httperr.WriteJSON(w, http.StatusCreated, setupClaimResponse{WorkspaceID: wsID.String()})
	}
}
