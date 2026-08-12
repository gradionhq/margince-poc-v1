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
	if err := assertSameCallIdentity(rest(staged), rest(other)); err == nil {
		t.Fatal("an edit that moved the call from one deal to another was accepted; the approving " +
			"human judged the first record and the effect would land on the second")
	}
}

// Content stays editable — that is what ADR-0036 §4 is for. A staging whose body
// changes while its call identity holds is exactly the correction a human is
// invited to make.
func TestAnEditMayStillCorrectTheBodyOfARestStagedCall(t *testing.T) {
	deal, path := ids.NewV7(), "/v1/deals/"
	before := json.RawMessage(`{"operation":"advanceDeal","path":"` + path + deal.String() +
		`/advance","body":{"note":"as discussed"}}`)
	after := json.RawMessage(`{"operation":"advanceDeal","path":"` + path + deal.String() +
		`/advance","body":{"note":"as agreed on the call"}}`)
	if err := assertSameCallIdentity(before, after); err != nil {
		t.Errorf("a body correction was refused as a retarget: %v", err)
	}
}

// A tool staging carries neither member. It must pass rather than fail closed here,
// or every MCP-staged approval becomes uneditable.
func TestAToolStagingHasNoCallIdentityToPin(t *testing.T) {
	before := json.RawMessage(`{"deal_id":"` + ids.NewV7().String() + `","note":"a"}`)
	after := json.RawMessage(`{"deal_id":"` + ids.NewV7().String() + `","note":"b"}`)
	if err := assertSameCallIdentity(before, after); err != nil {
		t.Errorf("a tool staging with no operation or path was treated as retargeted: %v", err)
	}
}

// Dropping the member is a change, not an absence: an edit that deletes `path`
// leaves a payload the redemption re-derives its own path for, which is the same
// re-aiming by another route.
func TestDroppingTheCallIdentityIsARetarget(t *testing.T) {
	before := json.RawMessage(`{"operation":"advanceDeal","path":"/v1/deals/x/advance","body":{}}`)
	after := json.RawMessage(`{"operation":"advanceDeal","body":{}}`)
	if err := assertSameCallIdentity(before, after); err == nil {
		t.Error("an edit that removed the staged path was accepted")
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
