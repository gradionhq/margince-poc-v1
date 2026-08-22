// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// The lifecycle rules, stated as tests because they are product decisions a
// reader cannot recover from the SQL: closing keeps a buyer reading, pausing
// keeps credentials valid, and publishing is refused only where a buyer is no
// longer meant to receive anything.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// dealRoomStates reads the state vocabulary out of the contract rather than
// restating it, so a tenth state added upstream reaches this test instead of
// defaulting silently to publishable. crm.yaml is the authority the migration's
// CHECK and the generated enum both follow.
func dealRoomStates(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "crm.yaml"))
	if err != nil {
		t.Fatalf("reading the contract to derive the state vocabulary: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas struct {
				DealRoomState struct {
					Enum []string `yaml:"enum"`
				} `yaml:"DealRoomState"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the contract: %v", err)
	}
	states := doc.Components.Schemas.DealRoomState.Enum
	if len(states) == 0 {
		t.Fatal("DealRoomState declares no enum — this test is reading the wrong shape")
	}
	return states
}

func TestPublishIsRefusedOnlyWhereABuyerIsDoneReceiving(t *testing.T) {
	// What publishing from each state must do. Every state the contract declares
	// needs a verdict here; the check below fails on one this table forgot.
	verdicts := map[string]bool{
		stateDraft:   true,
		"building":   true,
		"ready":      true,
		"publishing": true,
		stateLive:    true,
		// Publishing a paused room deliberately resumes it: a seller who
		// publishes plainly means the buyer to see the result.
		statePaused:   true,
		stateClosed:   false,
		"expired":     false,
		stateArchived: false,
	}

	for _, state := range dealRoomStates(t) {
		want, judged := verdicts[state]
		if !judged {
			t.Errorf("the contract declares state %q and this test has no verdict for it — "+
				"decide whether a buyer may still receive a release in that state", state)
			continue
		}
		t.Run(state, func(t *testing.T) {
			if got := publishable(state); got != want {
				t.Errorf("publishable(%q) = %v, want %v", state, got, want)
			}
		})
	}
}

func TestARefusedLifecycleMoveNamesBothTheStateAndTheRule(t *testing.T) {
	// A bare 409 leaves the caller guessing which of nine states they are in,
	// which is exactly what they cannot see from outside.
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"publish", notPublishable(stateClosed), "deal_room_not_publishable"},
		{"pause", notPausable(stateDraft), "deal_room_not_pausable"},
		{"resume", notPaused(stateLive), "deal_room_not_paused"},
		{"close", notClosable(stateDraft), "deal_room_not_closable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.err, apperrors.ErrConflict) {
				t.Errorf("a refused move must map to 409; got %v", tc.err)
			}
			var fault interface{ MessageFault() (string, string) }
			if !errors.As(tc.err, &fault) {
				t.Fatalf("a refused move must carry a client-branchable code; got %T", tc.err)
			}
			code, message := fault.MessageFault()
			if code != tc.code {
				t.Errorf("code = %q, want %q", code, tc.code)
			}
			if message == "" {
				t.Error("the message must say what the room is and what the move needed")
			}
		})
	}
}

func TestASecondRoomOnOneDealSaysHowToFreeIt(t *testing.T) {
	// The unique index refuses the second room; the caller's next move is
	// unguessable unless the refusal names it.
	if !errors.Is(errRoomAlreadyOpen, apperrors.ErrConflict) {
		t.Errorf("a duplicate room must map to 409; got %v", errRoomAlreadyOpen)
	}
	_, message := errRoomAlreadyOpen.MessageFault()
	if !strings.Contains(message, "archive") {
		t.Errorf("the refusal must name archiving as the way out; got %q", message)
	}
}

func TestAnUpdateEventPublishesFieldNamesAndNotTheirText(t *testing.T) {
	// Room text is unpublished editorial content until a human publishes it.
	// Putting the draft on the bus would hand every subscriber the words before
	// the buyer has them, past the gate that exists to stop exactly that.
	p := storekit.NewPatch()
	p.Set("title", "old title", "new title")
	p.Set("welcome_message", "old welcome", "secret unpublished draft")

	fields := changedFields(p)
	want := []string{"title", "welcome_message"}
	if len(fields) != len(want) {
		t.Fatalf("changedFields = %v, want %v", fields, want)
	}
	for i, name := range want {
		if fields[i] != name {
			t.Errorf("changedFields[%d] = %q, want %q (sorted, so a subscriber diffing two events is not reading map order)", i, fields[i], name)
		}
	}
	for _, name := range fields {
		if strings.Contains(name, "secret") || strings.Contains(name, "draft") {
			t.Errorf("changedFields leaked draft text: %q", name)
		}
	}
}
