// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// writeSetupJSON answers on the operational mux, which has no contract
// serializer of its own — these two routes are outside crm.yaml for the same
// reason /healthz is.
func writeSetupJSON[T setupStatusResponse | setupClaimResponse](w http.ResponseWriter, status int, body T) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	//craft:ignore swallowed-errors the status line is already written, so a failed encode cannot become an error response; the client sees a truncated body and the server's write error surfaces in the access log
	_ = json.NewEncoder(w).Encode(body)
}

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
func setupStatus(svc *identity.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		outstanding, err := svc.SetupTokenOutstanding(r.Context())
		if err != nil {
			httperr.Write(w, r, err)
			return
		}
		writeSetupJSON(w, http.StatusOK, setupStatusResponse{Claimable: outstanding})
	}
}

// setupClaim creates the organization and its first admin from a claim.
func setupClaim(svc *identity.Service, pool *pgxpool.Pool, seeds deployconfig.Seeds) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		writeSetupJSON(w, http.StatusCreated, setupClaimResponse{WorkspaceID: wsID.String()})
	}
}
