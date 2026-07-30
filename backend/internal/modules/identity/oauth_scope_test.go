// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// offline_access requests session lifetime, not access (§5.2): a client
// appends it to ask for a refresh token, but it must never become a
// passport scope — validScopes has no entry for it.

import (
	"slices"
	"testing"
)

func TestOfflineAccessIsAMarkerAndNeverAPassportScope(t *testing.T) {
	scopes, offline, err := parseOAuthScopes("read draft offline_access")
	if err != nil {
		t.Fatalf("offline_access must be accepted: %v", err)
	}
	if !offline {
		t.Error("offline_access must set the refresh marker")
	}
	// It requests session lifetime, not access. If it reached IssuePassport it
	// would be an unknown passport scope and validScopes would reject it.
	if slices.Contains(scopes, "offline_access") {
		t.Errorf("scopes = %v, must not carry offline_access", scopes)
	}
	if _, _, err := parseOAuthScopes("read bogus"); err == nil {
		t.Error("an unknown scope must still be refused")
	}
}
