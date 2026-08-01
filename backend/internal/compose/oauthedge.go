// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The A2 authorization server's edge on the api origin: the ceilings on every
// unauthenticated endpoint of the handshake — the passport mint, dynamic client
// registration, the human consent flow, and RFC 7009 revocation — plus the
// body-handling two of those keys depend on. The /mcp transport's own edge is
// mcpedge.go, and both draw from the ONE mcpLimiters set built there.
//
// Every arm below obeys the shape mcpLimiters describes, and it is a shape
// rather than a number because of what clientIP can be: behind the
// TLS-terminating front end this deployment documents, the peer address is one
// constant for every caller on earth, so a tight ceiling keyed on it is an
// outage a cheap flood buys. Each arm therefore meters a key the caller
// PRESENTS, paired with a high per-peer ceiling that a varying presented key
// cannot escape. Where an endpoint has no presented key at all (registration),
// that is said outright rather than papered over.

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"net/url"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// The authorization-server paths metered by name. Every other /oauth path falls
// to the default arm in oauthAdmits, so a route this router grows later arrives
// bounded rather than unlimited.
const (
	oauthTokenPath     = "/oauth/token" //nolint:gosec // G101 false positive: this server's own token *endpoint path*, not a credential
	oauthRegisterPath  = "/oauth/register"
	oauthAuthorizePath = "/oauth/authorize"
	oauthRevokePath    = "/oauth/revoke"
)

// The per-peer ceiling's groups: ONE limiter and one number, but a budget per
// endpoint group (peerCeilingKey), so no arm's flood is spendable against
// another's — revocation availability in particular must not be spendable by
// consent traffic, hostile or not.
const (
	peerGroupToken     = "token"
	peerGroupAuthorize = "authorize"
	peerGroupRevoke    = "revoke"
	peerGroupOther     = "other"
)

// tokenFormPeek caps how much of a form body the edge reads to find the field
// its bucket is keyed on. A form-encoded authorization-code exchange or
// revocation is a few hundred bytes; anything past this cap is the handler's
// 400 to answer, not this edge's to guess at.
const tokenFormPeek = 8 << 10

// oauthEdge bounds the authorization server's internet-facing endpoints. It
// wraps the session middleware rather than sitting inside it, so a refusal
// costs a map lookup instead of a session read.
func oauthEdge(next http.Handler, lim mcpLimiters) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !oauthAdmits(r, lim) {
			httperr.Write(w, r, apperrors.ErrBudgetExceeded)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// oauthAdmits meters one authorization-server request against every ceiling
// that applies to it.
//
// Every arm assigns both ceilings to locals before the &&, because both are
// Allow — they COUNT — and short-circuiting would leave one ceiling unmetered on
// the requests the other one admitted.
func oauthAdmits(r *http.Request, lim mcpLimiters) bool {
	ip := httpserver.ClientIP(r)
	switch r.URL.Path {
	case oauthTokenPath:
		// Per (client_id, IP) and never IP alone: all of claude.ai arrives from
		// one published egress range, so an IP-only bucket here would be one
		// ceiling for the whole installation and one busy client would lock out
		// every other. But the caller CHOOSES its client_id, so that bucket
		// alone is no ceiling either — a fresh random value bought a fresh
		// 60/min allowance on the endpoint that mints passports. The per-peer
		// ceiling is what a varying client_id cannot escape; at 600/min it sits
		// an order of magnitude above real handshake traffic (a few exchanges
		// per client per minute), so what it costs us is that a flood sustained
		// at 600/min denies the mint while it lasts. Keying per real client
		// behind a shared front end is the front end's job, which clientIP's own
		// doc names.
		perClient := lim.token.Allow(tokenBucketKey(peekFormField(r, "client_id"), ip))
		perPeer := lim.peerCeiling.Allow(peerCeilingKey(peerGroupToken, ip))
		return perClient && perPeer
	case oauthAuthorizePath:
		// Consent is a human flow, so the session a request presents is a key per
		// real human. Without that key an unauthenticated flood at one request per
		// second denied every human in the installation the ability to approve or
		// re-approve a connection. A request presenting no session shares the
		// fallback bucket instead, and on this path that is not only a refusal any
		// more: the authorize GET answers a session-less human with a redirect to
		// sign in. So a flood there costs a human who has not signed in YET the
		// start of their flow (they sign in through the app and re-enter), and
		// costs a signed-in human nothing — their key is their own.
		// The per-peer ceiling is what a varying cookie cannot escape, and 60
		// consent requests a minute is far past what one human generates.
		perHuman := lim.authorize.Allow(presentedKey("session", ip, sessionDigest(r)))
		perPeer := lim.peerCeiling.Allow(peerCeilingKey(peerGroupAuthorize, ip))
		return perHuman && perPeer
	case oauthRevokePath:
		// Revocation is the kill switch, so it carries ceilings of its OWN
		// rather than sharing the consent flow's: a flood of consent requests
		// must not be able to stop a client killing its own connection. The key
		// is the token presented for revocation, so what is bounded is
		// repetition of one token — a client revokes each token once — and a
		// request carrying no token is the handler's 400 either way.
		perToken := lim.revoke.Allow(presentedKey("token", ip, digestOf(peekFormField(r, "token"))))
		perPeer := lim.peerCeiling.Allow(peerCeilingKey(peerGroupRevoke, ip))
		return perToken && perPeer
	case oauthRegisterPath:
		// Dynamic client registration is anonymous by construction: the
		// client_id does not exist yet, and every field of the body is
		// attacker-chosen, so there is NO key here that a legitimate
		// registration does not share with whoever copies its body. This arm
		// therefore has one ceiling and the honest work is in its shape — high
		// enough that real traffic never reaches it (Claude registers one client
		// per fresh connection, and an installation does not make 60 of those in
		// a minute), and windowed by the MINUTE so a denial cannot outlive the
		// flood that caused it. The 10/hour this replaces meant ten
		// unauthenticated requests bought an hour of installation-wide
		// registration outage, and Claude's fresh-client-per-connection
		// behaviour made ten the legitimate ceiling too.
		//
		// What it still costs us: an attacker sustaining 60 registrations a
		// minute keeps the endpoint shut for as long as they sustain it, and
		// creates up to 60 oauth_client rows a minute per replica while doing
		// so. Nothing else bounds that row growth today — the per-workspace
		// registration cap and the admin bulk-delete of never-used clients that
		// DESIGN §5.3/§5.5 name are both Phase 2 surfaces.
		return lim.register.Allow(ip)
	default:
		// A path this router grows later arrives bounded by the ceiling no
		// attacker-chosen key escapes — and bounded in its own group, so an
		// unlisted endpoint's flood spends neither consent's budget nor
		// revocation's. A tighter key on what that endpoint's callers present is
		// the job of whoever adds it deliberately.
		return lim.peerCeiling.Allow(peerCeilingKey(peerGroupOther, ip))
	}
}

// peerCeilingKey namespaces the shared per-peer ceiling by endpoint group. One
// limiter serves every arm, and the group in the key is what keeps one arm's
// flood out of another arm's budget.
func peerCeilingKey(group, ip string) string { return group + "|" + ip }

// tokenBucketKey bounds the per-client half of the token endpoint's key. A DCR
// client_id is a 43-char base64url string, but what arrives on the wire is
// whatever the caller sent — up to tokenFormPeek — and a limiter retains each
// key for up to two windows, so an unbounded key turns the limiter meant to
// bound this endpoint into a memory sink reachable by an unauthenticated caller.
// The digest is fixed length and collision-resistant, so the bucket is still per
// client. A request whose client_id cannot be read shares one bucket per IP,
// which is a ceiling, not a bypass.
func tokenBucketKey(clientID, ip string) string {
	return digestOf(clientID) + "|" + ip
}

// sessionDigest reads the browser session the consent flow authenticates with.
// This edge sits outside the session middleware, so the cookie is all it can
// see; identity.SessionCookieName is the ONE spelling of that cookie's name, so
// the key here cannot drift from the credential the middleware reads. An absent
// cookie is the answer "presented nothing", which presentedKey meters per peer.
func sessionDigest(r *http.Request) string {
	cookie, err := r.Cookie(identity.SessionCookieName)
	if err != nil {
		return ""
	}
	return digestOf(cookie.Value)
}

// isFormURLEncoded reports whether a Content-Type names the OAuth token
// endpoint's media type, the way net/http itself reads it: the type is
// case-INSENSITIVE and parameters (`; charset=utf-8`) are legal and ignored.
//
// A prefix match on the raw header put `Application/X-WWW-Form-Urlencoded`
// into the empty-client bucket while the handler happily parsed its body. The
// limiter and the handler must classify the same request identically, or a
// shared peer's 60 legitimate-but-differently-cased requests spend a bucket
// that then 429s innocent clients.
func isFormURLEncoded(header string) bool {
	mediaType, _, err := mime.ParseMediaType(header)
	return err == nil && mediaType == "application/x-www-form-urlencoded"
}

// peekFormField reads one field out of a form-encoded body WITHOUT consuming
// it: ParseForm drains r.Body, and the handler behind this edge parses the same
// body again, so whatever is read here is put back in front of the unread
// remainder. The handler therefore still sees the request the client actually
// sent — including the oversized or unreadable body it must answer 400 for.
func peekFormField(r *http.Request, field string) string {
	if !isFormURLEncoded(r.Header.Get("Content-Type")) {
		return ""
	}
	read, err := io.ReadAll(io.LimitReader(r.Body, tokenFormPeek+1))
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(read), r.Body))
	if err != nil || len(read) > tokenFormPeek {
		// A body this edge cannot read whole is one the handler will refuse
		// anyway; the bucket falls back to its per-peer arm rather than
		// inventing a refusal of its own here.
		return ""
	}
	form, err := url.ParseQuery(string(read))
	if err != nil {
		return ""
	}
	return form.Get(field)
}
