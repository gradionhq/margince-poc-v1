// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// The mapping both transports validate a to-do through, and the completion
// patch that decides which columns move together.
//
// These carry judgments a reader cannot recover from the types: which sides
// exist, what an omitted field means, and the rule that a done item always names
// exactly one actor.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestATaskRefusesASideNobodyOwes(t *testing.T) {
	// The schema CHECK would refuse it too, but as a 500 naming a table. Naming
	// the closed set here is the difference between a caller learning what is
	// legal and a caller learning that something broke.
	for _, side := range []string{"", "both", "Seller", "margince", "  seller  "} {
		t.Run(side, func(t *testing.T) {
			_, err := createTaskInput(crmcontracts.CreateDealRoomTaskRequest{
				Side:   side,
				Title:  "Send the signed order form",
				Source: "manual",
			})
			var fault interface {
				FieldFault() (string, string, string)
			}
			if !errors.As(err, &fault) {
				t.Fatalf("%q must be refused as a field fault, got %v", side, err)
			}
			if field, _, _ := fault.FieldFault(); field != "side" {
				t.Errorf("the refusal must name side, got %q", field)
			}
		})
	}
}

func TestATaskAcceptsEitherSide(t *testing.T) {
	// The discriminating half of the test above: a refusal test alone would pass
	// against a validator that refused everything.
	for _, side := range []string{sideSeller, sideBuyer} {
		t.Run(side, func(t *testing.T) {
			in, err := createTaskInput(crmcontracts.CreateDealRoomTaskRequest{
				Side:   side,
				Title:  "Send the signed order form",
				Source: "manual",
			})
			if err != nil {
				t.Fatalf("%q must be accepted: %v", side, err)
			}
			if in.Side != side {
				t.Errorf("Side = %q, want %q", in.Side, side)
			}
		})
	}
}

func TestATaskRefusesWordingNobodyCouldAct(t *testing.T) {
	for _, tc := range []struct{ name, title string }{
		{"empty", ""},
		{"blank", "   "},
		{"overlong", strings.Repeat("t", titleLimit+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := createTaskInput(crmcontracts.CreateDealRoomTaskRequest{
				Side:   sideSeller,
				Title:  tc.title,
				Source: "manual",
			})
			var fault interface {
				FieldFault() (string, string, string)
			}
			if !errors.As(err, &fault) {
				t.Fatalf("%q must be refused as a field fault, got %v", tc.title, err)
			}
			if field, _, _ := fault.FieldFault(); field != columnTitle {
				t.Errorf("the refusal must name title, got %q", field)
			}
		})
	}
}

func TestATaskTrimsTheWordingItAccepts(t *testing.T) {
	in, err := createTaskInput(crmcontracts.CreateDealRoomTaskRequest{
		Side:   sideBuyer,
		Title:  "  Return the redline  ",
		Source: "manual",
	})
	if err != nil {
		t.Fatalf("a well-formed item must be accepted: %v", err)
	}
	if in.Title != "Return the redline" {
		t.Errorf("Title = %q, want it trimmed", in.Title)
	}
}

func TestAnOmittedFieldLeavesATaskAlone(t *testing.T) {
	// An empty patch is not a request to blank the row. Every field being nil has
	// to survive validation and reach the store as nil, or a caller correcting
	// one field would silently clear the rest.
	in, err := updateTaskInput(crmcontracts.UpdateDealRoomTaskRequest{}, nil)
	if err != nil {
		t.Fatalf("an empty patch must be accepted: %v", err)
	}
	if in.Side != nil || in.Title != nil || in.Position != nil || in.Done != nil {
		t.Errorf("an empty patch must carry no changes, got %+v", in)
	}
}

func TestACorrectionValidatesTheSideAndWordingToo(t *testing.T) {
	// The patch path is where a bad value does the most harm, because it replaces
	// wording the other side may already have read.
	bad := "neither"
	if _, err := updateTaskInput(crmcontracts.UpdateDealRoomTaskRequest{Side: &bad}, nil); err == nil {
		t.Error("an unknown side must be refused on the patch path too")
	}
	blank := "   "
	if _, err := updateTaskInput(crmcontracts.UpdateDealRoomTaskRequest{Title: &blank}, nil); err == nil {
		t.Error("blank wording must be refused on the patch path too")
	}
}

func TestTickingAnItemNamesWhoDidIt(t *testing.T) {
	// The completion CHECK refuses a done row that names nobody, so the patch has
	// to carry an actor. Without this the constraint answers instead, as a 500.
	actor := ids.NewV7()
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type:   principal.PrincipalHuman,
		UserID: actor,
	})
	p, err := completionPatch(ctx, true, crmcontracts.DealRoomTask{})
	if err != nil {
		t.Fatalf("ticking an item must be accepted: %v", err)
	}
	after := p.After()
	if after["done_by_user_id"] != actor {
		t.Errorf("done_by_user_id = %v, want the acting user %v", after["done_by_user_id"], actor)
	}
	if after["done_at"] == nil {
		t.Error("done_at must be stamped")
	}
	if after["done_by_participant_id"] != nil {
		t.Error("a seller-side completion must not name a participant")
	}
}

func TestTickingAnItemRefusesAnActorlessCaller(t *testing.T) {
	// A principal with no user id cannot be recorded as having done the work, and
	// a to-do list that says work was finished without saying by whom answers
	// neither side's question.
	if _, err := completionPatch(context.Background(), true, crmcontracts.DealRoomTask{}); err == nil {
		t.Fatal("a completion carrying no user must be refused")
	}
}

func TestUnTickingAnItemClearsWhoDidIt(t *testing.T) {
	// Re-opening an item leaves it owed again. Keeping the old attribution would
	// claim somebody finished work that is now outstanding.
	done := time.Now().UTC()
	user := openapi_types.UUID(ids.NewV7())
	p, err := completionPatch(context.Background(), false, crmcontracts.DealRoomTask{
		Done: true, DoneAt: &done, DoneByUserId: &user,
	})
	if err != nil {
		t.Fatalf("re-opening an item must be accepted: %v", err)
	}
	after := p.After()
	for _, column := range []string{"done_at", "done_by_user_id", "done_by_participant_id"} {
		// Presence is asserted separately from the value: a column the patch never
		// mentions reads back as nil exactly like one it clears, so checking the
		// value alone would pass against a patch that left the row done.
		got, written := after[column]
		if !written {
			t.Errorf("%s must be written to, or the row stays attributed", column)
			continue
		}
		if got != nil {
			t.Errorf("%s = %v, want it cleared", column, got)
		}
	}
}

func TestOnlyAFinishedRoomRefusesItsToDoList(t *testing.T) {
	// The room states a to-do list stays workable in, asserted as a pair: a test
	// that only proved refusal would pass against a rule that refused every room.
	for _, state := range []string{"draft", "building", "ready", "publishing", "live", "paused"} {
		if !publishable(state) {
			t.Errorf("a %s room must still keep its to-do list", state)
		}
	}
	for _, state := range []string{"closed", "expired", "archived"} {
		if publishable(state) {
			t.Errorf("a %s room's to-do list must be a record, not work", state)
		}
	}
}
