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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/ratelimit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The MCP server identity reported in the initialize handshake.
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
func (s *Server) mcpHandler(pool *pgxpool.Pool, auth *identity.Service, log *slog.Logger) http.Handler {
	if !s.mcpConnectorEnabled {
		return nil
	}
	// An operator turning the gate on needs one line confirming the surface
	// came up, and how much of it: a mount that silently serves nothing is
	// indistinguishable from a mount that never happened.
	log.Info("mcp: hosted connector transport mounted", "path", "/mcp", "tools", len(s.toolRegistry.Specs()))
	// The consent flow leaves this origin and has to come back to it: the
	// authorize GET redirects a human's browser to the SPA route below, which
	// POSTs the decision back to /oauth/authorize. Nothing here can verify that
	// anything answers that route — the api serves no SPA — and an ingress that
	// sends it elsewhere 404s a human mid-consent, so the target is named where an
	// operator can compare it against what their front end actually routes.
	log.Info("mcp: consent screen redirect target", "location", identity.ConsentScreenPath)
	return agents.NewHTTPHandler(s.toolRegistry, mcpAuthenticate(auth),
		agents.ResourceMetadataChallenge, mcpServerName, mcpServerVersion, log,
		// The cross-module edge: composing the query vocabulary is the search
		// module's job and publishing it is the transport's, and neither
		// reaches for the other (ADR-0054 §3). It is wired here, once — and
		// the custom-field half rides the same fieldcatalog seam every record
		// store does, so a workspace's own columns are askable without this
		// edge knowing anything about them.
		//
		// The schema reader is what keeps the published vocabulary honest: a
		// field the contract declares and no table holds is not askable, so
		// what this document advertises is what a plan can be answered from.
		// Without it the vocabulary is the contract's, which is WIDER — and a
		// wider vocabulary published here would refuse at execution what it
		// advertised at discovery.
		//
		// queryVocabulary is the SAME construction query_workspace's executor
		// validates against (queryseam.go), which is what makes "what this
		// document advertises" and "what a plan can be answered from" the same
		// sentence rather than two that have to be kept in step.
		// TWO providers now, fanned into the one the transport takes: the
		// vocabulary above, and the interactive views the tool surface serves. The
		// views come second, so a URI collision resolves to the vocabulary — see
		// composeResources for why the order is stated rather than incidental, and
		// TestTheProductionProvidersPublishDisjointSchemes for the gate that makes
		// it moot — which is the one that reaches BOTH of these, unlike the
		// duplicate sweep, which only sees the view catalogue.
		agents.WithResourceProvider(composeResources(
			mcpResourceProviders(search.NewQuerySchemaResource(queryVocabulary(pool)), s.appViews)...,
		)),
		// And the OTHER half of the same promise. A tool's UI.ResourceURI is a
		// constant baked at registration; whether that document arrived is a
		// runtime fact only the view provider knows. Wiring both from one value
		// is what makes "a tool never names a view the server does not serve"
		// true per request rather than only at build time.
		agents.WithHeldViews(s.heldView()),
		// The Tasks extension, which is why a confirm-first call no longer dead-ends
		// for a client that can hold a handle. The store is composed here for the
		// same reason the claim store is: agent_task rows are this transport's own
		// operational state, and modules/agents owns no SQL.
		// A plain service, like every other staging-side construction here: the
		// per-kind effects belong to the DECIDE path, and a task neither decides
		// nor triggers one. What it does is read a decision and then take the
		// ordinary redemption route through Registry.Invoke.
		agents.WithTaskStore(toolTasks(pool), approvalsAdapter{svc: approvals.NewService(pool)}))
}

// mcpAuthenticate binds one request to its agent principal. It runs on EVERY
// exchange: the passport, the granting human's seat and their RBAC are all
// re-derived, so a revocation or demotion takes effect on the next call
// rather than after a reconnect.
func mcpAuthenticate(auth *identity.Service) func(*http.Request) (context.Context, error) {
	return func(r *http.Request) (context.Context, error) {
		wsID, err := auth.InstallationWorkspace(r.Context())
		if err != nil {
			// Resolving WHICH installation this is has nothing to do with the
			// credential the caller presented, so no failure of it may be
			// reported as a credential problem — not an unbootstrapped
			// database, not an unreachable one.
			return nil, fmt.Errorf("mcp: resolving the installation: %w: %w", err, agents.ErrAuthUnavailable)
		}
		ctx := principal.WithWorkspaceID(r.Context(), wsID.UUID)
		// bearerToken requires the scheme name and a non-empty credential
		// after it. A TrimPrefix-style read would accept a header that never
		// carried the prefix, turning an unrelated credential (or a Basic
		// header) into a passport lookup.
		bearer := httpserver.BearerToken(r.Header.Get("Authorization"))
		if bearer == "" {
			return nil, errMissingBearer
		}
		agent, err := auth.AuthenticateAgent(ctx, bearer)
		if err != nil {
			// ErrNotFound is the ONLY definitive verdict on the credential —
			// the token is unknown, revoked, or its grant/client/human is gone
			// (existence-hiding collapses all of those into one answer). Any
			// other error means the lookup never reached a verdict, so it must
			// not be dressed up as one.
			if !errors.Is(err, apperrors.ErrNotFound) {
				return nil, fmt.Errorf("mcp: verifying the passport: %w: %w", err, agents.ErrAuthUnavailable)
			}
			return nil, err
		}
		return principal.WithCorrelationID(principal.WithActor(ctx, agent.Principal()), ids.NewV7()), nil
	}
}

// mcpLimiters bounds the connector's edges — the /mcp transport here and the
// authorization server in oauthedge.go, from ONE set, because they are two
// halves of one internet-facing surface.
//
// Keys matter more than numbers. Behind the TLS-terminating front end this
// deployment documents (clientIP), the peer address is ONE constant for every
// caller on earth, so every ceiling comes in a PAIR: a tight bucket keyed on
// what the caller presented — which no attacker can spend on a legitimate
// caller's behalf — and a high per-peer ceiling that a varying presented key
// cannot escape. Neither is a bound on its own, and the per-peer half is set an
// order of magnitude above real traffic so it bites only under a deliberate
// flood.
type mcpLimiters struct {
	perPassport *ratelimit.Limiter // 240/min per credential — authenticated tool-call volume
	preAuth     *ratelimit.Limiter // 60/min per presented credential, per peer when none — failures only
	preAuthPeer *ratelimit.Limiter // 600/min per peer — the failure ceiling a varying credential cannot escape
	streams     *ratelimit.Limiter // 30/min per credential — stream-open churn
	token       *ratelimit.Limiter // 60/min per (client_id digest, IP) — the passport mint
	authorize   *ratelimit.Limiter // 60/min per presented session, per peer when none — consent
	revoke      *ratelimit.Limiter // 60/min per presented token, per peer when none — RFC 7009
	register    *ratelimit.Limiter // 60/min per peer — dynamic client registration, which has no per-caller key
	peerCeiling *ratelimit.Limiter // 600/min per (endpoint group, peer) — the shared ceiling no chosen key escapes
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
		preAuthPeer: ratelimit.NewWithClock(600, time.Minute, now),
		streams:     ratelimit.NewWithClock(30, time.Minute, now),
		token:       ratelimit.NewWithClock(60, time.Minute, now),
		authorize:   ratelimit.NewWithClock(60, time.Minute, now),
		revoke:      ratelimit.NewWithClock(60, time.Minute, now),
		register:    ratelimit.NewWithClock(60, time.Minute, now),
		peerCeiling: ratelimit.NewWithClock(600, time.Minute, now),
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
		peer := httpserver.ClientIP(r)
		failures := presentedKey("credential", peer, credential)
		// Read BEFORE the transport authenticates, so a credential already
		// known not to work is refused without spending a store lookup on it.
		//
		// TWO failure ceilings, for the reason every pair on this surface
		// exists: the tight one is keyed on what the caller presented, and the
		// caller chooses that, so a grinder that varies the bearer would meet no
		// ceiling at all — each forged value buying its own fresh 60/min and one
		// indexed passport lookup per request. The per-peer ceiling is what a
		// varying credential cannot escape. Both count FAILURES only (below), so
		// neither is spent by a connector whose calls are served.
		//
		// What the peer ceiling costs us, stated: an attacker sustaining 600
		// failed pre-auth attempts a minute from the front end's address does
		// deny the transport for the remainder of that window. It is set there
		// because no working connector produces 401s at all and a client whose
		// credential just died produces a handful, so real traffic never
		// approaches it — while a grinder now meets a bound at 600 lookups a
		// minute instead of none.
		if lim.preAuth.Blocked(failures) || lim.preAuthPeer.Blocked(peer) {
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
			lim.preAuthPeer.Record(peer)
		}
	})
}

// presentedKey names WHOSE budget one unauthenticated request spends, and that
// choice is the whole security property of the budget: the key must never be one
// a legitimate caller SHARES with an attacker. TLS terminates ahead of this
// process in production, so clientIP is the front end's own address for every
// request on the planet — a budget keyed there and consulted ahead of ALL
// traffic is one bucket for the entire installation, and a cheap flood then
// answers every real caller with a 429 before authentication ever runs.
//
// So a request that PRESENTS something is metered on a digest of it — the bearer
// on /mcp, the browser session on the consent form, the token to kill on
// /oauth/revoke — and only a request presenting NOTHING falls back to the peer
// address. What that fallback can cost is bounded by what a presentation-less
// request can obtain: a refusal on /mcp and on /oauth/revoke, and on the consent
// GET a redirect to sign in — so spending it never touches a caller holding the
// real thing, and at worst delays a human who has not signed in yet. kind
// namespaces the two arms so a key read out of a limiter says which one it is.
//
// What no presented key can bound is the FIRST use of each distinct
// presentation: a bearer nobody has seen is indistinguishable from a valid one
// until it is looked up, and a caller that varies it therefore never meets this
// ceiling. That is what the per-peer ceiling paired with it at every call site
// bounds — neither half is a bound alone.
//
// Both key shapes are FIXED length (a 64-char digest, or a peer address), and
// ratelimit sweeps expired windows, so the resident key set is bounded by the
// request rate within one window rather than growing with history.
func presentedKey(kind, ip, presented string) string {
	if presented == "" {
		return "peer:" + kind + ":" + ip
	}
	return kind + ":" + presented
}

// digestOf turns a presented secret into a limiter key: fixed length, so a
// caller cannot choose the key's size and turn a limiter into its own memory
// sink, and a digest rather than the secret, so nothing long-lived holds a live
// credential. An empty presentation stays empty — presentedKey has to be able to
// tell "presented nothing" from "presented something".
func digestOf(presented string) string {
	if presented == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(presented))
	return hex.EncodeToString(sum[:])
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
	return digestOf(httpserver.BearerToken(r.Header.Get("Authorization")))
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
