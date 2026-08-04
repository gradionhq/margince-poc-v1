// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

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
// `Handlers.WithPasswordReset`), so this concatenation cannot produce `//#/`.
func passwordLink(baseURL, rawToken string) string {
	return baseURL + "/#/reset-password?token=" + rawToken
}
