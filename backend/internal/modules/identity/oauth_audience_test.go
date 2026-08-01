// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The RFC 8707 audience rules, side by side: redemption is strict about an
// omitted resource, renewal is not. The two live next to each other here
// because the ONE row where they differ is the whole reason there are two.

import "testing"

func TestAudienceRulesAgreeExceptOnAnOmittedResourceAtRenewal(t *testing.T) {
	const canonical = "https://crm.example.com/mcp"
	boundToCanonical := canonical
	boundToAnOldValue := "https://crm.old.example.com/mcp"

	for name, tc := range map[string]struct {
		presented        string
		bound            *string
		wantAtRedemption bool
		wantAtRenewal    bool
	}{
		"nothing presented, nothing bound": {
			presented: "", bound: nil, wantAtRedemption: true, wantAtRenewal: true,
		},
		"the canonical value against an unbound grant": {
			presented: canonical, bound: nil, wantAtRedemption: true, wantAtRenewal: true,
		},
		"a foreign audience against an unbound grant": {
			presented: "https://attacker.example/mcp", bound: nil, wantAtRedemption: false, wantAtRenewal: false,
		},
		"the canonical value against the grant it is bound to": {
			presented: canonical, bound: &boundToCanonical, wantAtRedemption: true, wantAtRenewal: true,
		},
		"a foreign audience against a bound grant": {
			presented: "https://attacker.example/mcp", bound: &boundToCanonical, wantAtRedemption: false, wantAtRenewal: false,
		},
		// The one row that differs: an omitted resource is a refusal at
		// redemption (the client is seconds from a human) and the grant's own
		// audience at renewal (the client may be a month past one).
		"nothing presented against a bound grant": {
			presented: "", bound: &boundToCanonical, wantAtRedemption: false, wantAtRenewal: true,
		},
		// A binding that no longer names the configured endpoint must not be
		// honoured just because the client repeats the new canonical value.
		"the canonical value against a grant bound to a reconfigured resource": {
			presented: canonical, bound: &boundToAnOldValue, wantAtRedemption: false, wantAtRenewal: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := audienceMatches(tc.presented, canonical, tc.bound); got != tc.wantAtRedemption {
				t.Errorf("audienceMatches(%q, bound=%v) = %v, want %v", tc.presented, tc.bound, got, tc.wantAtRedemption)
			}
			if got := refreshAudienceMatches(tc.presented, canonical, tc.bound); got != tc.wantAtRenewal {
				t.Errorf("refreshAudienceMatches(%q, bound=%v) = %v, want %v", tc.presented, tc.bound, got, tc.wantAtRenewal)
			}
		})
	}
}

// An unset canonical value (no --public-base-url) must fail closed rather than
// admit whatever the caller presents — on both paths.
func TestNoConfiguredResourceRefusesEveryPresentedAudience(t *testing.T) {
	if audienceMatches("https://attacker.example/mcp", "", nil) {
		t.Error("redemption admitted a presented audience with no canonical value configured")
	}
	if refreshAudienceMatches("https://attacker.example/mcp", "", nil) {
		t.Error("renewal admitted a presented audience with no canonical value configured")
	}
}
