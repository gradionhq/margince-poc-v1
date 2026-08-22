// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The Deal Room buyer edge: /v1/public/rooms/* carries no seat session. This
// middleware, composed beside the booking and preference edges, throttles the
// surface and — for the operations that need one — resolves the Bearer to a
// room session and binds it together with a BUYER principal.
//
// What it does NOT do is bind a system principal. The other two public edges
// do, because their handlers go on to call RBAC-gated stores that a system
// principal passes. A buyer must not pass those gates: platform/auth refuses
// the buyer kind outright, so the only store methods that answer a buyer are
// the Deal Room's session-scoped ones. The three anonymous operations carry no
// actor at all; the store binds the one the credential names once it is known.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/dealrooms"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/ratelimit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const publicDealRoomPrefix = "/v1/public/rooms/"

// The three operations that carry no session. Everything else under the prefix
// presents a Bearer.
var publicDealRoomAnonymous = map[string]bool{
	"peek":         true,
	"exchange":     true,
	"link-request": true,
}

// publicDealRoomLimiters: per-IP is the brake on everything; the link request
// gets a second, tighter one because each accepted call can put a mail on the
// wire, and a shared ceiling so a distributed flood cannot turn the relay into
// a spam source whatever the per-IP figure admits.
type publicDealRoomLimiters struct {
	perIP         *ratelimit.Limiter
	linkPerIP     *ratelimit.Limiter
	linkPerEmail  *ratelimit.Limiter
	linkShared    *ratelimit.Limiter
	perSessionMut *ratelimit.Limiter
}

func newPublicDealRoomLimiters() publicDealRoomLimiters {
	return publicDealRoomLimiters{
		perIP:     ratelimit.New(60, time.Minute),
		linkPerIP: ratelimit.New(3, time.Minute),
		// Per ADDRESS, whatever the source: a reissue retires the buyer's
		// standing credential, so one address may be reissued a few times an
		// hour and no more, or a distributed caller could keep a buyer's link
		// perpetually retired.
		linkPerEmail:  ratelimit.New(3, time.Hour),
		linkShared:    ratelimit.New(10, time.Minute),
		perSessionMut: ratelimit.New(20, time.Minute),
	}
}

// linkSharedKey is the one bucket every link request shares.
const linkSharedKey = "deal_room_link_request"

func publicDealRoom(store *dealrooms.Store, limits publicDealRoomLimiters) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, publicDealRoomPrefix) {
				next.ServeHTTP(w, r)
				return
			}
			operation := strings.SplitN(strings.TrimPrefix(r.URL.Path, publicDealRoomPrefix), "/", 2)[0]
			if !limits.perIP.Allow(httpserver.ClientIP(r)) {
				httperr.Write(w, r, apperrors.ErrBudgetExceeded)
				return
			}
			if publicDealRoomAnonymous[operation] {
				if operation == "link-request" && !linkRequestAdmitted(limits, r) {
					httperr.Write(w, r, apperrors.ErrBudgetExceeded)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			sess, ok := resolveBuyerSession(store, w, r)
			if !ok {
				return
			}
			if r.Method != http.MethodGet && !limits.perSessionMut.Allow(sess.ID.String()) {
				httperr.Write(w, r, apperrors.ErrBudgetExceeded)
				return
			}
			ctx := dealrooms.WithSession(r.Context(), sess)
			ctx = principal.WithActor(ctx, dealrooms.BuyerPrincipal(sess.ParticipantID))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// linkRequestAdmitted takes the link-request buckets in order of cost: the
// per-IP one first so a single source is refused before it eats into the
// ceiling everyone shares, the per-address one last because reading it means
// reading the body.
func linkRequestAdmitted(limits publicDealRoomLimiters, r *http.Request) bool {
	if !limits.linkPerIP.Allow(httpserver.ClientIP(r)) {
		return false
	}
	if !limits.linkShared.Allow(linkSharedKey) {
		return false
	}
	email, ok := peekLinkRequestEmail(r)
	if !ok {
		// Unparseable: let the handler answer the 422. Nothing is reissued.
		return true
	}
	return limits.linkPerEmail.Allow(email)
}

// peekLinkRequestEmail reads the address out of the body without consuming it
// for the handler, lowercased so the bucket matches the stored spelling.
func peekLinkRequestEmail(r *http.Request) (string, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, linkRequestBodyLimit))
	if err != nil {
		return "", false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Email == "" {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(body.Email)), true
}

// linkRequestBodyLimit bounds what the edge will read to find the address: an
// address and a little JSON around it.
const linkRequestBodyLimit = 4 << 10

// resolveBuyerSession reads the Bearer and resolves it, writing the one 401
// every failure shares. A missing header, a malformed one, an unknown token and
// a revoked one are indistinguishable from outside.
func resolveBuyerSession(store *dealrooms.Store, w http.ResponseWriter, r *http.Request) (dealrooms.Session, bool) {
	const detail = "this link no longer admits you: ask for a new one"
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	token = strings.TrimSpace(token)
	if !ok || token == "" {
		httperr.Unauthorized(w, r, detail)
		return dealrooms.Session{}, false
	}
	sess, err := store.ResolveSession(r.Context(), token)
	if errors.Is(err, dealrooms.ErrSessionRefused) {
		httperr.Unauthorized(w, r, detail)
		return dealrooms.Session{}, false
	}
	if err != nil {
		// A database fault is not a refusal, and must not read as one: a buyer
		// told their link is dead will ask for a new one that is not needed.
		httperr.Write(w, r, err)
		return dealrooms.Session{}, false
	}
	return sess, true
}
