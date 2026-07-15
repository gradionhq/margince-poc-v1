// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestTrustTierOfNativeHitIsAuthoritative(t *testing.T) {
	got := trustTierOf(Hit{Type: "person"})
	if got != crmcontracts.SearchResultTrustTierAuthoritative {
		t.Fatalf("trustTierOf native hit = %q, want authoritative", got)
	}
}
