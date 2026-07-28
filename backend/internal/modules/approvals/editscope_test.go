// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The edit-scope rule as a table: an edit corrects what a staged action SAYS,
// never which record it applies to.
func TestAssertSameEntityRefsPinsEveryRecordTheProposalNames(t *testing.T) {
	const (
		mine   = "8bf1b0f2-6c2c-4a1a-9a0e-1c9b2a3d4e5f"
		theirs = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
		alice  = "2b3c4d5e-6f7a-4b8c-9d0e-1f2a3b4c5d6e"
	)

	tests := []struct {
		name        string
		original    string
		edited      string
		wantChanged []string
	}{
		{
			name:     "editing the content a human is meant to correct is allowed",
			original: `{"organization_id":"` + mine + `","proposed_name":"Acme","persons":["` + alice + `"]}`,
			edited:   `{"organization_id":"` + mine + `","proposed_name":"Acme GmbH","persons":["` + alice + `"]}`,
		},
		{
			name:     "a payload naming no record at all is entirely editable",
			original: `{"stage":"proposal","note":"agent version"}`,
			edited:   `{"stage":"won","note":"human version","reason":"signed"}`,
		},
		{
			name:        "repointing the target at another record is refused",
			original:    `{"organization_id":"` + mine + `","proposed_name":"Acme"}`,
			edited:      `{"organization_id":"` + theirs + `","proposed_name":"Acme"}`,
			wantChanged: []string{"/organization_id"},
		},
		{
			name:        "dropping the reference is refused too — an absent id resolves to nothing the gate checked",
			original:    `{"organization_id":"` + mine + `","proposed_name":"Acme"}`,
			edited:      `{"proposed_name":"Acme"}`,
			wantChanged: []string{"/organization_id"},
		},
		{
			name:        "introducing a reference the staging never carried is refused",
			original:    `{"proposed_name":"Acme"}`,
			edited:      `{"proposed_name":"Acme","owner_id":"` + theirs + `"}`,
			wantChanged: []string{"/owner_id"},
		},
		{
			name:        "a reference nested in a list is pinned like a top-level one",
			original:    `{"persons":["` + alice + `"]}`,
			edited:      `{"persons":["` + theirs + `"]}`,
			wantChanged: []string{"/persons/[0]"},
		},
		{
			name:        "a reference nested in an object is pinned like a top-level one",
			original:    `{"link":{"activity_id":"` + alice + `"}}`,
			edited:      `{"link":{"activity_id":"` + theirs + `"}}`,
			wantChanged: []string{"/link/activity_id"},
		},
		// The editor chooses the key names, so it can try to spell a nested
		// path as one flat key and have the two read as the same location.
		// They must not: the reference would move out of where the effect
		// reads it while this check saw nothing change.
		{
			name:        "a flat key spelling a nested path does not collide with it",
			original:    `{"link":{"activity_id":"` + alice + `"}}`,
			edited:      `{"link/activity_id":"` + alice + `"}`,
			wantChanged: []string{"/link/activity_id", "/link~1activity_id"},
		},
		{
			name:        "an object key spelling an array index does not collide with it",
			original:    `{"persons":["` + alice + `"]}`,
			edited:      `{"persons":{"[0]":"` + alice + `"}}`,
			wantChanged: []string{"/persons/[0]", "/persons/~20]"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := assertSameEntityRefs(json.RawMessage(tc.original), json.RawMessage(tc.edited))
			if len(tc.wantChanged) == 0 {
				if err != nil {
					t.Fatalf("edit refused: %v — a human must stay free to correct the action's content", err)
				}
				return
			}
			var retargeted *RetargetedEditError
			if !errors.As(err, &retargeted) {
				t.Fatalf("edit accepted (err = %v), want RetargetedEditError naming %v", err, tc.wantChanged)
			}
			if strings.Join(retargeted.Paths, ",") != strings.Join(tc.wantChanged, ",") {
				t.Errorf("refused paths = %v, want %v", retargeted.Paths, tc.wantChanged)
			}
		})
	}
}

// The refusal message names the field, so an operator reading a 422 can tell a
// typo from an attempt to re-aim the approval.
func TestRetargetedEditErrorNamesTheOffendingPaths(t *testing.T) {
	err := &RetargetedEditError{Paths: []string{"/organization_id", "/owner_id"}}
	msg := err.Error()
	for _, want := range []string{"/organization_id", "/owner_id"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not name %q", msg, want)
		}
	}
}
