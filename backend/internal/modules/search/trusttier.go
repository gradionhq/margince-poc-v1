// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"

// trustTierOf is the ONE place a hit's provenance tier is decided. Native mode
// (the only shipped mode) stores authoritative records; the overlay tiers
// (external/unverified) attach here when overlay adapters land, so the switch —
// not a literal at the call site — is where that evolution happens.
func trustTierOf(_ Hit) crmcontracts.SearchResultTrustTier {
	return crmcontracts.SearchResultTrustTierAuthoritative
}
