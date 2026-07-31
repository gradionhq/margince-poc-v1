// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The RFC 8707 audience rules, for the two moments a credential is handed over
// against one: REDEEMING an authorization code and RENEWING a refresh token.
// They differ in exactly one place, deliberately, and that difference is the
// whole reason they are spelled here together rather than inline at each call
// site — a reader has to be able to see the asymmetry to judge it. Split out of
// oauth.go so each file stays one concept.

// audienceMatches is the RFC 8707 rule for a credential being redeemed or
// renewed, spelled once: two independent checks, not one compound test.
//
// A PRESENTED resource must always name this installation's canonical
// endpoint, checked unconditionally — so a client that omitted resource at
// authorize (the accepted older-client path, stored NULL) cannot smuggle a
// foreign audience through later by presenting it only at the token endpoint.
// Separately, an authorization that WAS bound to a resource requires the
// presented value to match that binding, so a stale grant cannot outlive a
// reconfigured resource. Unbound therefore means "the canonical resource",
// never "any resource".
//
// An unset canonical value (no --public-base-url) can never equal a presented
// one, so this fails closed rather than treating "no canonical value" as
// "matches everything".
func audienceMatches(presented, canonical string, bound *string) bool {
	if presented != "" && presented != canonical {
		return false
	}
	return bound == nil || presented == *bound
}

// refreshAudienceMatches is the same rule for a RENEWAL, and it differs from
// redemption in exactly one place, on purpose.
//
// A PRESENTED resource is judged identically: it must name this
// installation's canonical endpoint, and must also match the binding a grant
// carries, so neither a foreign audience nor a grant that outlived a
// reconfigured resource gets through. An ABSENT resource, though, means "the
// audience this grant is already bound to" rather than a refusal — and that is
// where the two rules part.
//
// The asymmetry is a risk judgement, not a shrug. Naming no audience is not a
// client asking for a foreign one, so renewing against the grant's own
// recorded resource hands out no authority the consent did not already carry.
// Refusing it, on the other hand, would kill a working connection 30 days
// after anyone was watching — the exact failure this whole feature exists to
// prevent — for any client that omits the parameter on refresh. A code
// exchange has no such trap: it happens seconds after a human approved it, in
// a flow a client developer is watching, so redemption stays strict.
func refreshAudienceMatches(presented, canonical string, bound *string) bool {
	if presented == "" {
		return true
	}
	return audienceMatches(presented, canonical, bound)
}
