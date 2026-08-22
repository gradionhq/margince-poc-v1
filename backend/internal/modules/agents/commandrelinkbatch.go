// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The two batch relinks, as commands both doors resolve. They are the single
// relink at scale: the same destination question decides the tier
// (destinationTieredCall), the same vocabulary guards the destination
// (requireLinkTarget), and the owning store performs the same guarded write
// per row. What differs is the subject: neither names ONE record an approval
// could pin, so — like a report run — the staged card carries a sentence
// saying what moves and binds to no row.

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// RelinkThreadCommand is one conversation re-association, whichever door asked
// for it: the thread key and the record every writable member is filed under.
type RelinkThreadCommand struct {
	ThreadKey  string
	EntityType string
	EntityID   ids.UUID
}

// NewRelinkThreadCall binds one thread move to the resolver that answers for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewRelinkThreadCall(cmd RelinkThreadCommand) GovernedCall {
	return destinationTieredCall{
		GovernedCall: bind[RelinkThreadCommand](relinkThreadResolver{}, cmd),
		entityType:   cmd.EntityType,
	}
}

type relinkThreadResolver struct{}

// Subject names NO record, and says why: the rows that change are every member
// of the thread the caller may write, decided inside the store's transaction,
// so there is no one row an approval could bind to or pin. The sentence still
// carries the thread and the destination, which is what a human decides on.
func (relinkThreadResolver) Subject(_ context.Context, cmd RelinkThreadCommand) (StageInfo, error) {
	return StageInfo{Summary: fmt.Sprintf("Re-associate the conversation %q to %s %s",
		cmd.ThreadKey, cmd.EntityType, cmd.EntityID)}, nil
}

// Guards refuses a destination type that is not a link target and a blank
// thread key — the two refusals the store makes before it opens a transaction,
// asked here before a human is.
func (relinkThreadResolver) Guards(_ context.Context, cmd RelinkThreadCommand) error {
	if cmd.ThreadKey == "" {
		return &BadArgsError{Cause: fmt.Errorf("thread_key names the conversation to move; it cannot be blank")}
	}
	return requireLinkTarget(cmd.EntityType)
}

// RelinkActivitiesCommand is one named-set re-association, whichever door
// asked for it.
type RelinkActivitiesCommand struct {
	ActivityIDs []ids.UUID
	EntityType  string
	EntityID    ids.UUID
}

// NewRelinkActivitiesCall binds one named-set move to the resolver that
// answers for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewRelinkActivitiesCall(cmd RelinkActivitiesCommand) GovernedCall {
	return destinationTieredCall{
		GovernedCall: bind[RelinkActivitiesCommand](relinkActivitiesResolver{}, cmd),
		entityType:   cmd.EntityType,
	}
}

type relinkActivitiesResolver struct{}

// Subject names NO record for the reason relinkThreadResolver gives: the
// approval governs a set, and a set has no version to pin. The count is in the
// sentence because that is the scale a human is asked to release.
func (relinkActivitiesResolver) Subject(_ context.Context, cmd RelinkActivitiesCommand) (StageInfo, error) {
	return StageInfo{Summary: fmt.Sprintf("Re-associate %d activities to %s %s",
		len(cmd.ActivityIDs), cmd.EntityType, cmd.EntityID)}, nil
}

// Guards refuses the destination vocabulary and an empty or oversized set —
// the store's own bound, asked before a human spends an approval on a call
// the store would refuse.
func (relinkActivitiesResolver) Guards(_ context.Context, cmd RelinkActivitiesCommand) error {
	if len(cmd.ActivityIDs) == 0 || len(cmd.ActivityIDs) > maxRelinkActivities {
		return &BadArgsError{Cause: fmt.Errorf("activity_ids names between 1 and %d activities; this call names %d",
			maxRelinkActivities, len(cmd.ActivityIDs))}
	}
	return requireLinkTarget(cmd.EntityType)
}

// maxRelinkActivities mirrors the contract's bound on relink-bulk, so a set
// the store would refuse is refused before it reaches the store.
const maxRelinkActivities = 500
