// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import "testing"

func TestCompanyReadReplyAcceptsReviewableChangesAndOnlyKnownSources(t *testing.T) {
	known := map[string]struct{}{"S1": {}}
	valid := `{"message":"I found the registered name.","proposed_changes":[{"field":"legal_name","value":"Acme GmbH","reason":"The legal notice states it."}],"source_ids":["S1"]}`
	if err := validateCompanyReadReply(valid, known); err != nil {
		t.Fatalf("valid reply rejected: %v", err)
	}

	unknown := `{"message":"I found it.","proposed_changes":[],"source_ids":["S9"]}`
	if err := validateCompanyReadReply(unknown, known); err == nil {
		t.Fatal("reply citing a URL outside the dossier was accepted")
	}

	unsupported := `{"message":"I can change it.","proposed_changes":[{"field":"website","value":"evil.example","reason":"requested"}],"source_ids":[]}`
	if err := validateCompanyReadReply(unsupported, known); err == nil {
		t.Fatal("reply proposing a field outside the onboarding vocabulary was accepted")
	}
}
