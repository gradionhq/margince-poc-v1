// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The anonymous booking edge (feedback/14): /v1/public/booking/* carries
// neither session nor workspace header, so this middleware — composed
// between the session middleware and the contract router — resolves the
// slug to its tenant, throttles the unauthenticated surface, and binds
// the workspace plus a system principal confined to the two public
// endpoints (whose responses are free/busy slots and {start,end} only).
// Everything downstream (idempotency claims, RBAC-gated stores, audit
// attribution as actor_type=system) then works unchanged.

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/ratelimit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const publicBookingPrefix = "/v1/public/booking/"

// publicBookingLimits: the page is anonymous and rate limiting is its
// only brake (api-rate-limits-and-abuse.md §1.3). Per-IP covers scripted
// scraping of one client; per-slug covers a distributed flood aimed at
// one host's calendar. In-process, clock-injected — the login-limiter
// scope (single-binary PoC; a multi-replica deployment moves the keys to
// Redis without changing callers).
type publicBookingLimiters struct {
	perIP   *ratelimit.Limiter // 60/min per client IP, both endpoints
	perSlug *ratelimit.Limiter // 20/min per slug, bookings only
}

func newPublicBookingLimiters() publicBookingLimiters {
	return publicBookingLimiters{
		perIP:   ratelimit.New(60, time.Minute),
		perSlug: ratelimit.New(20, time.Minute),
	}
}

func publicBooking(store *activities.Store, limits publicBookingLimiters) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, publicBookingPrefix) {
				next.ServeHTTP(w, r)
				return
			}
			slug := strings.SplitN(strings.TrimPrefix(r.URL.Path, publicBookingPrefix), "/", 2)[0]
			if slug == "" {
				httperr.Write(w, r, apperrors.ErrNotFound)
				return
			}
			if !limits.perIP.Allow(publicClientIP(r)) {
				httperr.Write(w, r, apperrors.ErrBudgetExceeded)
				return
			}
			if r.Method == http.MethodPost && !limits.perSlug.Allow(slug) {
				httperr.Write(w, r, apperrors.ErrBudgetExceeded)
				return
			}

			page, err := store.ResolveBookingPage(r.Context(), slug)
			if err != nil {
				// Unknown and revoked slugs read identically as absent.
				httperr.Write(w, r, err)
				return
			}

			ctx := principal.WithWorkspaceID(r.Context(), page.WorkspaceID)
			ctx = principal.WithActor(ctx, principal.Principal{
				Type: principal.PrincipalSystem,
				ID:   "system:public_booking",
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// publicClientIP mirrors the login throttle's key: the direct peer. A
// deployment fronted by a proxy terminates rate limiting there (or
// extends this to a TRUSTED Forwarded header — never trusted blindly).
func publicClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
