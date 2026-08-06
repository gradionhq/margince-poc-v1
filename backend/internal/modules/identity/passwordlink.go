// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// passwordLink is the ONE spelling of an emailed set-password deep link, and the
// reason it is a function rather than a string per caller is that the token's
// PLACEMENT in the URL is a security property — not a decision each caller gets
// to make. Both mails that use it carry a live single-use credential: the reset
// link for one hour, the invite link for seven days.
//
// The token rides in the FRAGMENT, which a BROWSER never puts on the wire. That
// closes the three legs a query string loses on: the credential cannot reach an
// access log, cannot be sent as a Referer on the SPA's same-origin /v1 calls, and
// cannot become a Cache Storage key — a service worker caching a navigation keys
// it on the full URL, query included.
//
// It is not an absolute guarantee, and the difference matters when reading the
// TTLs above. A click-tracking mail gateway (Safe Links, Proofpoint, Mimecast)
// re-serializes the whole URL — fragment and all — into its OWN query string, so
// the token lands in a third party's request line whichever form we choose. The
// fragment is the right default; the containment is single use plus the TTL.
//
// The shape is the app's own hash route rather than a bare `#token=` because
// `parseHash` (frontend app/router.tsx) reads the first hash segment as the screen
// name and strips a hash-local query: `#/reset-password?token=…` parses, while
// `#token=…` would make the token itself the screen name.
//
// `baseURL` arrives with any trailing slash already trimmed (see
// `Handlers.WithPasswordLinkBase`), so this concatenation cannot produce `//#/`.
func passwordLink(baseURL, rawToken string) string {
	return baseURL + "/#/reset-password?token=" + rawToken
}

// WithPasswordLinkBase injects the canonical external base set-password deep
// links are built on — the installation's public base URL. The trailing slash
// is trimmed HERE and only here: passwordLink concatenates onto this value and
// documents that guarantee, so a base ending in "/" would otherwise produce
// "//#/".
func (h Handlers) WithPasswordLinkBase(publicBaseURL string) Handlers {
	h.passwordLinkBaseURL = strings.TrimRight(publicBaseURL, "/")
	return h
}

// canSendPasswordLink reports whether this installation can MAIL a
// set-password link: it needs both the transport and a canonical base to build
// the link on. The two arrive separately, so they can disagree — and a mailer
// without a base would send a link built on an empty origin, which is a
// worse outcome than not offering recovery at all. cmd/api refuses to boot in
// that state, but the honest answer belongs here too rather than resting on one
// composition root remembering to check.
func (h Handlers) canSendPasswordLink() bool {
	return h.resetMailer != nil && h.passwordLinkBaseURL != ""
}

// canIssuePasswordLink reports whether THIS principal may issue member
// set-password links, which is what /me advertises. It is a caller capability
// and not a deployment-posture flag on purpose: /me answers every
// authenticated member, so a bare posture boolean would tell every rep whether
// the installation has an email channel. The conditions are exactly the ones a
// real call must clear, so the client never renders a control that can only
// fail — including the seat ceiling, which sits ABOVE RBAC: an admin on a
// licence read seat is refused every mutating method by serveAsHuman before
// their role is ever consulted, so advertising the action to them would offer a
// button that answers 403 whatever their role says.
func (h Handlers) canIssuePasswordLink(id Identity) bool {
	return id.hasRole(roleAdmin) &&
		principal.SeatType(id.SeatType).CanMutate() &&
		h.resetMailer == nil &&
		h.passwordLinkBaseURL != ""
}
