// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// Which acts an agent may perform on a Deal Room, held at the store rather than
// only at the transport.
//
// The contract already marks the human-only operations, and the REST gate
// refuses an agent principal on them. Both are real, and neither reaches a
// caller inside the process — a compose orchestration, or the buyer edge when it
// lands, would pass the transport gate by never meeting it. So the rule is
// asserted here, against the store, where a new caller cannot route around it.
//
// The division: shaping a DRAFT is auto-execute, because an agent writing a
// better welcome paragraph changes nothing an outside party can read. Anything
// that moves what a buyer can reach — publishing text to them, suspending or
// ending their access, or widening the window they hold — is a person's.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// fullyEmpoweredAgent carries every grant the object gate can ask for, so a
// refusal in these tests can only come from the human-only rule under test.
//
// An agent with no permissions would fail these tests identically whether the
// guard existed or not — the mistake that let four gates admit a buyer in an
// earlier slice, and the reason this fixture is maximal rather than minimal.
func fullyEmpoweredAgent() principal.Principal {
	return principal.Principal{
		Type:   principal.PrincipalAgent,
		ID:     "agent:" + ids.NewV7().String(),
		UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			RowScope: principal.RowScopeAll,
			Objects: map[string]principal.ObjectGrant{
				roomObject: {Create: true, Read: true, Update: true, Delete: true},
			},
		},
	}
}

func TestAnAgentMayNotMoveWhatABuyerCanReach(t *testing.T) {
	// A nil store is deliberate: every one of these must refuse BEFORE it opens
	// a transaction. A guard that ran after the database was reached would still
	// be a guard, but it would also be one a caller could trip over in a way
	// that leaves a half-open transaction behind.
	store := &Store{}
	ctx := principal.WithActor(context.Background(), fullyEmpoweredAgent())
	roomID := ids.From[ids.DealRoomKind](ids.NewV7())

	for _, tc := range []struct {
		act  string
		call func() error
	}{
		{"publish a release to the buyer", func() error {
			_, err := store.PublishRoom(ctx, roomID, nil)
			return err
		}},
		{"pause the buyer's access", func() error {
			_, err := store.PauseRoom(ctx, roomID)
			return err
		}},
		{"resume the buyer's access", func() error {
			_, err := store.ResumeRoom(ctx, roomID)
			return err
		}},
		{"close the room", func() error {
			_, err := store.CloseRoom(ctx, roomID)
			return err
		}},
		{"archive the room", func() error {
			_, err := store.ArchiveRoom(ctx, roomID, nil)
			return err
		}},
		{"widen the window the buyer holds", func() error {
			far := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
			_, err := store.SetExpiry(ctx, roomID, &far, nil)
			return err
		}},
		{"remove the expiry bound entirely", func() error {
			_, err := store.SetExpiry(ctx, roomID, nil, nil)
			return err
		}},
	} {
		t.Run(tc.act, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, apperrors.ErrPermissionDenied) {
				t.Errorf("an agent was allowed to %s: got %v, want ErrPermissionDenied", tc.act, err)
			}
		})
	}
}

func TestAnAgentMayStillShapeADraft(t *testing.T) {
	// The other half, and the one that makes the test above mean something. If
	// the store refused every agent write, the refusals above would prove only
	// that agents cannot touch Deal Rooms at all — which is not the rule, and
	// would hide a guard that had stopped discriminating.
	//
	// A nil store panics once the call gets past the authority checks, so
	// reaching the database IS the pass condition here: it says the agent was
	// admitted. Anything else means an authority gate turned it away.
	store := &Store{}
	ctx := principal.WithActor(context.Background(), fullyEmpoweredAgent())

	admitted := func() (got error) {
		defer func() {
			if r := recover(); r != nil {
				got = nil // reached the store: admitted, which is the point
			}
		}()
		_, err := store.UpdateRoom(ctx, ids.From[ids.DealRoomKind](ids.NewV7()), UpdateRoomInput{})
		return err
	}()

	if errors.Is(admitted, apperrors.ErrPermissionDenied) {
		t.Error("an agent was refused a draft edit; shaping unpublished text is auto-execute by design")
	}
}
