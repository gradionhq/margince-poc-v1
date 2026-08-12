// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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

// A REST staging carries its record inside the request PATH, not as a bare field.
// entityRefs collects only strings that parse wholly as a UUID, so the id in
// "/v1/deals/<uuid>/advance" is invisible to it — and an edit that rewrites the
// path therefore looks like a content correction while it re-aims the effect at a
// different record. The version pin still re-reads the ORIGINAL target, so nothing
// downstream notices either.
func TestAnEditMayNotRepointTheRecordNamedInARestPath(t *testing.T) {
	staged := ids.NewV7()
	other := ids.NewV7()
	// toStageID is fixed across both calls so the ONLY thing that differs
	// between the staged and edited payload is the record named in the path.
	// A body id that varied too would let assertSameEntityRefs reject the
	// edit for catching THAT change, which would prove nothing about
	// whether it can see the one hidden inside the path.
	toStageID := ids.NewV7().String()
	rest := func(id ids.UUID) json.RawMessage {
		return json.RawMessage(`{"operation":"advanceDeal","path":"/v1/deals/` + id.String() +
			`/advance","body":{"to_stage_id":"` + toStageID + `"}}`)
	}
	retargeted := requireRetargeted(t, assertSameCallIdentity(rest(staged), rest(other)),
		"an edit that moved the call from one deal to another was accepted; the approving "+
			"human judged the first record and the effect would land on the second")
	if strings.Join(retargeted.Paths, ",") != "/path" {
		t.Errorf("refused paths = %v, want [/path]", retargeted.Paths)
	}
}

// requireRetargeted fails the test with msg if err is not a *RetargetedEditError,
// and returns it otherwise — shared by every call-identity case below that also
// needs to inspect WHICH member the refusal names.
func requireRetargeted(t *testing.T, err error, msg string) *RetargetedEditError {
	t.Helper()
	var retargeted *RetargetedEditError
	if !errors.As(err, &retargeted) {
		t.Fatalf("%s (err = %v)", msg, err)
	}
	return retargeted
}

// The call-identity rule as a table: an edit may rewrite `body`, never any
// other top-level member of a REST staging — pinned by EXCLUDING body rather
// than by naming "operation" and "path", so a member the canonical call adds
// tomorrow (an If-Match or Idempotency-Key header, sibling of body) is pinned
// by construction rather than by someone remembering to list it.
func TestAssertSameCallIdentityPinsEveryMemberOfTheStagedCallExceptBody(t *testing.T) {
	const dealPath = `/v1/deals/11111111-1111-4111-8111-111111111111/advance`

	tests := []struct {
		name        string
		original    string
		edited      string
		wantChanged []string
	}{
		{
			name:     "content stays editable — that is what ADR-0036 §4 is for",
			original: `{"operation":"advance_deal","path":"` + dealPath + `","body":{"note":"as discussed"}}`,
			edited:   `{"operation":"advance_deal","path":"` + dealPath + `","body":{"note":"as agreed on the call"}}`,
		},
		{
			// A tool staging carries neither operation nor path. It must pass
			// rather than fail closed here, or every MCP-staged approval
			// becomes uneditable — entityRefs governs its content instead.
			name:     "a tool staging (no operation or path) has no call identity to pin",
			original: `{"deal_id":"11111111-1111-4111-8111-111111111111","note":"a"}`,
			edited:   `{"deal_id":"11111111-1111-4111-8111-111111111111","note":"b"}`,
		},
		{
			name:        "repointing path is refused",
			original:    `{"operation":"advance_deal","path":"` + dealPath + `","body":{}}`,
			edited:      `{"operation":"advance_deal","path":"/v1/deals/other/advance","body":{}}`,
			wantChanged: []string{"/path"},
		},
		{
			// operation names the call as much as path names the record — a
			// staging table-driven only on path would leave this arm of the
			// same guard unexercised.
			name:        "repointing operation alone is refused",
			original:    `{"operation":"advance_deal","path":"` + dealPath + `","body":{}}`,
			edited:      `{"operation":"disqualify_lead","path":"` + dealPath + `","body":{}}`,
			wantChanged: []string{"/operation"},
		},
		{
			// Dropping a member is a change, not an absence: an edit that
			// deletes `path` leaves a payload the redemption re-derives its
			// own path for, which is the same re-aiming by another route.
			name:        "dropping path is a retarget, not an absence",
			original:    `{"operation":"advance_deal","path":"` + dealPath + `","body":{}}`,
			edited:      `{"operation":"advance_deal","body":{}}`,
			wantChanged: []string{"/path"},
		},
		{
			// A member the canonical call does not carry TODAY (If-Match is
			// the next task's addition) is still pinned: deny-by-default
			// means this needs no update when that member is added.
			name:        "an unrecognized top-level member is pinned exactly like operation and path",
			original:    `{"operation":"advance_deal","path":"` + dealPath + `","if_match":"7","body":{}}`,
			edited:      `{"operation":"advance_deal","path":"` + dealPath + `","if_match":"9","body":{}}`,
			wantChanged: []string{"/if_match"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := assertSameCallIdentity(json.RawMessage(tc.original), json.RawMessage(tc.edited))
			if len(tc.wantChanged) == 0 {
				if err != nil {
					t.Fatalf("edit refused: %v — a human must stay free to correct the action's content", err)
				}
				return
			}
			retargeted := requireRetargeted(t, err, "edit accepted, want RetargetedEditError naming "+strings.Join(tc.wantChanged, ","))
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
