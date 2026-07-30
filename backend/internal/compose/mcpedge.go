// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The A2 hosted transport's edge on the api origin: the per-request
// authenticate closure the MCP handler runs, the deployment gate that decides
// whether the route exists at all, and the two guards in front of both — an
// Origin allowlist and the rate limits that bound every internet-facing edge
// of the connector, /mcp and the authorization server alike. The tool surface
// itself is srv.toolRegistry — the SAME registry the REST agent surface
// composes, so the two transports cannot differ in capability.
//
// What the rate limits here are, stated plainly: platform/ratelimit is an
// IN-PROCESS fixed-window counter, so every ceiling below is per replica — an
// N-replica deployment multiplies each of them by N. There is no shared store
// behind them, so there is also no dependency whose outage could fail closed;
// these buckets bound one binary's exposure and claim nothing more. Moving the
// same keys into Redis is what would make a ceiling installation-wide, and it
// changes no caller here.
//
// What the Origin guard is, stated plainly: an absent Origin is ALLOWED,
// because non-browser clients send none and refusing them would break every
// CLI client. What actually stops DNS rebinding is that every verb requires a
// Bearer a rebound page cannot attach — after a rebind the request is
// same-origin and carries no Origin at all, which the absent-allowed rule
// admits. The guard is defence in depth, not the rebinding defence.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/ratelimit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The MCP server identity reported in the initialize handshake — the same
// pair the stdio transport reports, because it is the same tool surface.
const (
	mcpServerName    = "margince-crm"
	mcpServerVersion = "0.1.0"
)

// errMissingBearer is the authenticate refusal for a request that carries no
// usable credential. It never reaches the client verbatim: the transport
// answers 401 + the RFC 9728 pointer, which is what a client acts on.
var errMissingBearer = errors.New("missing bearer token")

// mcpHandler builds the /mcp transport over the registry this Server already
// composed. It returns nil when the deployment gate is off — the caller then
// mounts no route, so turning the connector off removes the surface rather
// than guarding it.
//
// auth is PASSED IN, never constructed here: identity.Service is stateful —
// it caches the resolved singleton workspace in an atomic pointer and judges
// its time windows against an injectable clock — so a second instance would
// hold a second cache and, worse, its own time.Now, silently escaping a clock
// a test injected on the process's real service.
func (s *Server) mcpHandler(auth *identity.Service, log *slog.Logger) http.Handler {
	if !s.mcpConnectorEnabled {
		return nil
	}
	// An operator turning the gate on needs one line confirming the surface
	// came up, and how much of it: a mount that silently serves nothing is
	// indistinguishable from a mount that never happened.
	log.Info("mcp: hosted connector transport mounted", "path", "/mcp", "tools", len(s.toolRegistry.Specs()))
	return agents.NewHTTPHandler(s.toolRegistry, mcpAuthenticate(auth),
		agents.ResourceMetadataChallenge, mcpServerName, mcpServerVersion)
}

// mcpAuthenticate binds one request to its agent principal. It runs on EVERY
// exchange: the passport, the granting human's seat and their RBAC are all
// re-derived, so a revocation or demotion takes effect on the next call
// rather than after a reconnect.
func mcpAuthenticate(auth *identity.Service) func(*http.Request) (context.Context, error) {
	return func(r *http.Request) (context.Context, error) {
		wsID, err := auth.InstallationWorkspace(r.Context())
		if err != nil {
			return nil, err
		}
		ctx := principal.WithWorkspaceID(r.Context(), wsID.UUID)
		// bearerToken requires the scheme name and a non-empty credential
		// after it. A TrimPrefix-style read would accept a header that never
		// carried the prefix, turning an unrelated credential (or a Basic
		// header) into a passport lookup.
		bearer := bearerToken(r.Header.Get("Authorization"))
		if bearer == "" {
			return nil, errMissingBearer
		}
		agent, err := auth.AuthenticateAgent(ctx, bearer)
		if err != nil {
			return nil, err
		}
		return principal.WithCorrelationID(principal.WithActor(ctx, agent.Principal()), ids.NewV7()), nil
	}
}

// The authorization-server paths that carry a bucket of their own. Every
// other /oauth path falls to the per-IP default in oauthEdge, so a route the
// authorization server grows later arrives limited rather than unlimited.
const (
	oauthTokenPath    = "/oauth/token" //nolint:gosec // G101 false positive: this server's own token *endpoint path*, not a credential
	oauthRegisterPath = "/oauth/register"
)

// tokenFormPeek caps how much of a token request's body the edge reads to
// find client_id. A form-encoded authorization-code exchange is a few hundred
// bytes; anything past this cap is the handler's 400 to answer, not this
// edge's to guess at.
const tokenFormPeek = 8 << 10

// mcpLimiters bounds the connector's edges. Keys matter more than numbers
// here: ALL claude.ai traffic arrives from one published egress range
// (160.79.104.0/21), so an IP-only bucket on the token endpoint is a single
// shared ceiling for an entire installation.
type mcpLimiters struct {
	perPassport *ratelimit.Limiter // 240/min — authenticated tool-call volume
	preAuth     *ratelimit.Limiter // 60/min per presented credential, per peer when none (preAuthKey)
	streams     *ratelimit.Limiter // 30/min per passport — stream-open churn
	token       *ratelimit.Limiter // 60/min per (client_id, IP) — the passport mint
	authorize   *ratelimit.Limiter // 60/min per IP — the consent form and the grant
	register    *ratelimit.Limiter // 10/hour per IP — dynamic client registration
}

func newMCPLimiters() mcpLimiters { return newMCPLimitersWithClock(time.Now) }

// newMCPLimitersWithClock takes the clock for the same reason
// ratelimit.NewWithClock offers it: a window boundary is a property a test
// asserts by advancing time rather than by sleeping against it, and the
// numbers such a test pins must be the numbers a deployment actually runs.
func newMCPLimitersWithClock(now func() time.Time) mcpLimiters {
	return mcpLimiters{
		perPassport: ratelimit.NewWithClock(240, time.Minute, now),
		preAuth:     ratelimit.NewWithClock(60, time.Minute, now),
		streams:     ratelimit.NewWithClock(30, time.Minute, now),
		token:       ratelimit.NewWithClock(60, time.Minute, now),
		authorize:   ratelimit.NewWithClock(60, time.Minute, now),
		register:    ratelimit.NewWithClock(10, time.Hour, now),
	}
}

// mcpEdge guards the /mcp transport: the Origin allowlist, then the three
// buckets that bound it. allowedOrigin is the CONFIGURED public origin, so a
// Host header cannot widen the allowlist by naming itself.
func mcpEdge(next http.Handler, lim mcpLimiters, allowedOrigin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !originAllowed(r.Header.Get("Origin"), allowedOrigin) {
			httperr.Write(w, r, &httperr.DetailedError{
				Status: http.StatusForbidden,
				Code:   "origin_not_allowed",
				Detail: "This Origin may not reach the MCP transport.",
			})
			return
		}
		credential := passportBucket(r)
		failures := preAuthKey(clientIP(r), credential)
		// Read BEFORE the transport authenticates, so a credential already
		// known not to work is refused without spending a store lookup on it.
		if lim.preAuth.Blocked(failures) {
			httperr.Write(w, r, apperrors.ErrBudgetExceeded)
			return
		}
		if credential != "" {
			// A GET is a stream open: cheap to ask for, expensive to hold, so
			// it gets its own tighter bucket. It is metered here, ahead of the
			// transport's method dispatch, so what bounds the churn is whether
			// the request was made — not what the transport answers it with.
			bucket := lim.perPassport
			if r.Method == http.MethodGet {
				bucket = lim.streams
			}
			if !bucket.Allow(credential) {
				httperr.Write(w, r, apperrors.ErrBudgetExceeded)
				return
			}
		}
		// The pre-auth bucket meters the OUTCOME, not the attempt: a
		// legitimate connector's served calls must spend no failure budget at
		// all.
		outcome := &authOutcome{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(outcome, r)
		if outcome.status == http.StatusUnauthorized {
			lim.preAuth.Record(failures)
		}
	})
}

// preAuthKey names WHOSE failure budget one request spends, and the choice is
// the whole security property of that budget: it must never be a key a
// legitimate client SHARES with an attacker. TLS terminates ahead of this
// process in production, so clientIP is the front end's own address for every
// request on the planet — an IP-keyed budget that gates all traffic is one
// bucket for the entire installation, and 60 forged bearers a minute would
// then answer every real connector's tools/call with a 429 before
// authentication ever ran.
//
// So a request that PRESENTS a credential is metered on that credential's
// digest, and only a request presenting none falls back to the peer address.
// A credential-less request can never be anything but a 401, so refusing
// those costs a client holding a working credential nothing.
//
// What this bounds: repeated presentation of a credential that does not work,
// and credential-less probing per peer. What it deliberately does not bound is
// one passport lookup per DISTINCT credential presented — a token nobody has
// seen before is indistinguishable from a valid one until it is looked up, and
// the only key that could gate it is the shared front-end address, i.e. the
// outage. That floor is cheap and bounded elsewhere: a bearer that is not
// passport-shaped is refused on its prefix without reaching the store
// (identity.AuthenticateAgent), and the lookup that remains is one indexed
// read on a unique hash.
//
// Both key shapes are FIXED length (a 64-char digest, or a peer address), and
// ratelimit sweeps expired windows, so the resident key set is bounded by the
// request rate within one window rather than growing with history.
func preAuthKey(ip, credential string) string {
	if credential == "" {
		return "peer:" + ip
	}
	return "credential:" + credential
}

// oauthEdge bounds the authorization server's internet-facing endpoints. It
// wraps the session middleware rather than sitting inside it, so a refusal
// costs a map lookup instead of a session read.
func oauthEdge(next http.Handler, lim mcpLimiters) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		// The default bucket covers /oauth/authorize — the GET consent form
		// and the POST grant share it, being two halves of one human flow —
		// and every other path this router grows.
		bucket, key := lim.authorize, ip
		switch r.URL.Path {
		case oauthTokenPath:
			// (client_id, IP), never IP alone: one published egress range for
			// all of claude.ai means an IP-only bucket here is one ceiling for
			// the whole installation, where one busy client would lock out
			// every other. A request whose client_id cannot be read shares the
			// bucket of its IP, which is the previous behaviour, not a bypass.
			bucket, key = lim.token, tokenClientID(r)+"|"+ip
		case oauthRegisterPath:
			bucket = lim.register
		}
		if !bucket.Allow(key) {
			httperr.Write(w, r, apperrors.ErrBudgetExceeded)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed decides the Origin guard. Absent is allowed — see the file
// comment: non-browser clients send none, and the Bearer every verb demands
// is what a rebound page cannot produce. Loopback is allowed so a split dev
// stack (SPA on :5173, api on :8080) reaches the transport from the browser.
func originAllowed(origin, allowedOrigin string) bool {
	if origin == "" {
		return true
	}
	if allowedOrigin != "" && strings.EqualFold(origin, allowedOrigin) {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || net.ParseIP(host).IsLoopback()
}

// mcpOriginOf reduces the configured MCP resource URL to the scheme+host the
// Origin guard compares against: an Origin header carries no path, so leaving
// "/mcp" on the allowlisted value would make every browser request mismatch.
// An unparseable value leaves the allowlist empty, which admits loopback and
// absent Origins only — and the same malformed value already breaks the
// discovery document that advertises it, so it fails visibly there.
func mcpOriginOf(resource string) string {
	u, err := url.Parse(resource)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// passportBucket keys the authenticated buckets on a digest of the presented
// credential rather than on the passport id: the id is known only after
// authentication, and re-deriving it here would pay the whole authentication
// cost twice on the hottest path. A passport row's token_hash is only ever
// INSERTed, never updated (identity/passport.go), so one digest resolves to at
// most one passport — but a CONNECTION is a SEQUENCE of passports, since every
// refresh rotation mints a fresh one. What these ceilings bound is therefore
// one credential, not one connector and not one human; the per-connection
// ceiling is the refresh chain's own, at the token endpoint. The digest —
// never the credential — is what becomes a long-lived map key.
func passportBucket(r *http.Request) string {
	bearer := bearerToken(r.Header.Get("Authorization"))
	if bearer == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(bearer))
	return hex.EncodeToString(sum[:])
}

// tokenClientID reads client_id out of a token request WITHOUT consuming the
// body: ParseForm drains r.Body, and the handler behind this edge parses the
// same body again, so whatever is read here is put back in front of the
// unread remainder. The handler therefore still sees the request the client
// actually sent — including the oversized or unreadable body it must answer
// 400 for.
func tokenClientID(r *http.Request) string {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return ""
	}
	read, err := io.ReadAll(io.LimitReader(r.Body, tokenFormPeek+1))
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(read), r.Body))
	if err != nil || len(read) > tokenFormPeek {
		// A body this edge cannot read whole is one the handler will refuse
		// anyway; the bucket falls back to its IP half rather than inventing
		// a refusal of its own here.
		return ""
	}
	form, err := url.ParseQuery(string(read))
	if err != nil {
		return ""
	}
	return form.Get("client_id")
}

// authOutcome captures the status the wrapped handler answered, which is what
// lets the pre-auth bucket count failures instead of attempts.
type authOutcome struct {
	http.ResponseWriter
	status int
}

func (o *authOutcome) WriteHeader(status int) {
	o.status = status
	o.ResponseWriter.WriteHeader(status)
}

// Unwrap keeps http.NewResponseController reaching the real connection: the
// transport extends the write deadline for slow tool calls (and a later phase
// flushes an SSE stream), and an embedded-only wrapper swallows both silently.
func (o *authOutcome) Unwrap() http.ResponseWriter { return o.ResponseWriter }
