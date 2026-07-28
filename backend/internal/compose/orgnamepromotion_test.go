// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// What makes two org-name proposals the same QUESTION — the rule a human's
// refusal is remembered by.
func TestRefusedNameKeyComparesTheClaimNotTheSpelling(t *testing.T) {
	org := ids.New[ids.OrganizationKind]()

	// A refusal recorded by today's code: it carries the normalized key.
	current, err := json.Marshal(orgNameProposal{
		OrganizationID: org, CurrentName: "Gitex",
		ProposedName: "Gitex Global GmbH", ProposedNameKey: "gitex global",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A refusal recorded BEFORE proposed_name_key existed: raw spelling only,
	// and a different spelling of the very same claim.
	legacy := json.RawMessage(`{"organization_id":"` + org.String() +
		`","current_name":"Gitex","proposed_name":"GITEX  GLOBAL  GmbH"}`)

	tests := []struct {
		name    string
		refused []json.RawMessage
		key     string
		want    bool
	}{
		{"today's refusal binds its own claim", []json.RawMessage{current}, "gitex global", true},
		{
			// The finding this test exists for: the legacy payload holds
			// dominantSpelling's pick, which moves as signatures accumulate.
			// Comparing spellings would forget the refusal the moment it did.
			name:    "a pre-upgrade refusal binds despite a spelling-only change",
			refused: []json.RawMessage{legacy}, key: "gitex global", want: true,
		},
		{"an unrelated claim is not refused", []json.RawMessage{current, legacy}, "acme", false},
		{"no refusals at all", nil, "gitex global", false},
		{
			// Defensive: a payload of some other shape is not this kind's
			// refusal, and must not panic or match by accident.
			name:    "a payload this kind cannot read is skipped",
			refused: []json.RawMessage{json.RawMessage(`["not","an","object"]`)}, key: "gitex global", want: false,
		},
		{
			// An empty key must never match an unreadable/absent name, or one
			// malformed refusal would suppress every later proposal.
			name:    "an empty key matches nothing",
			refused: []json.RawMessage{json.RawMessage(`{"proposed_name":""}`)}, key: "", want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := refusedNameKey(tc.refused, tc.key); got != tc.want {
				t.Errorf("refusedNameKey(_, %q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}
