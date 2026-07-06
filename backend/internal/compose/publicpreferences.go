// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The anonymous preference-center edge (B-E11.32): /v1/public/preferences/*
// carries neither session nor workspace header, so this middleware —
// composed like the public-booking edge — resolves the token to its tenant,
// throttles the unauthenticated surface, and binds the workspace plus a
// system principal confined to the preference endpoints. Everything
// downstream (RBAC-gated consent store, audit attribution as
// actor_type=system) then works unchanged.

import (
	"net/http"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/ratelimit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const publicPreferencesPrefix = "/v1/public/preferences/"

// publicPreferenceLimiters mirror the booking edge: rate limiting is the
// only brake on an anonymous surface. Per-IP covers scripted scraping;
// per-token covers a flood aimed at one recipient's consent state. The
// one-click POST is the sensitive verb, so the per-token brake applies to
// mutations.
type publicPreferenceLimiters struct {
	perIP    *ratelimit.Limiter
	perToken *ratelimit.Limiter
}

func newPublicPreferenceLimiters() publicPreferenceLimiters {
	return publicPreferenceLimiters{
		perIP:    ratelimit.New(60, time.Minute),
		perToken: ratelimit.New(20, time.Minute),
	}
}

func publicPreferences(store *consent.Store, limits publicPreferenceLimiters) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, publicPreferencesPrefix) {
				next.ServeHTTP(w, r)
				return
			}
			token := strings.SplitN(strings.TrimPrefix(r.URL.Path, publicPreferencesPrefix), "/", 2)[0]
			if token == "" {
				httperr.Write(w, r, apperrors.ErrNotFound)
				return
			}
			if !limits.perIP.Allow(publicClientIP(r)) {
				httperr.Write(w, r, apperrors.ErrBudgetExceeded)
				return
			}
			if r.Method != http.MethodGet && !limits.perToken.Allow(token) {
				httperr.Write(w, r, apperrors.ErrBudgetExceeded)
				return
			}

			ref, err := store.ResolvePreferenceToken(r.Context(), token)
			if err != nil {
				// Unknown and revoked tokens read identically as absent — the
				// surface never becomes a consent-state oracle.
				httperr.Write(w, r, err)
				return
			}

			ctx := principal.WithWorkspaceID(r.Context(), ref.WorkspaceID)
			ctx = principal.WithActor(ctx, principal.Principal{
				Type: principal.PrincipalSystem,
				ID:   "system:public_preferences",
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
