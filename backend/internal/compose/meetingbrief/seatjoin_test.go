// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func asReader(objects map[string]principal.ObjectGrant) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test", UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"}, Objects: objects, RowScope: principal.RowScopeAll,
		},
	})
}

// The seat edge decorates a row this statement does not select, so a refusal
// becomes a JOIN predicate that never matches rather than an error. The room
// still lists everyone who was in it; only the deal_role goes.
//
// FALSE rather than an error, and not a dropped attendee: failing would deny a
// brief the caller may otherwise see in full, and dropping the attendee would
// make a withheld ROLE look like an absent PERSON.
func TestTheSeatJoinNeverMatchesWithoutTheEdgeGrant(t *testing.T) {
	ctx := asReader(map[string]principal.ObjectGrant{
		"activity": {Read: true}, "person": {Read: true}, "deal": {Read: true},
	})
	predicate, err := seatJoinPredicate(ctx, "r", func(any) int { return 1 })
	if err != nil {
		t.Fatalf("seatJoinPredicate(no edge grant) = %v, want the never-matches predicate", err)
	}
	if predicate != "FALSE" {
		t.Errorf("seatJoinPredicate(no edge grant) = %q, want FALSE — the role is withheld by matching "+
			"no seat, which reads exactly as an attendee who holds none", predicate)
	}
}

// The positive control: with the grant the predicate narrows rather than
// blanking, so the refusal above is attributable to the grant and not to a
// function that refuses everyone.
func TestTheSeatJoinNarrowsWithTheEdgeGrant(t *testing.T) {
	ctx := asReader(map[string]principal.ObjectGrant{"relationship": {Read: true}})
	predicate, err := seatJoinPredicate(ctx, "r", func(any) int { return 1 })
	if err != nil {
		t.Fatalf("seatJoinPredicate(edge grant) = %v, want a predicate", err)
	}
	if predicate == "FALSE" || predicate == "" {
		t.Errorf("seatJoinPredicate(edge grant) = %q, want the endpoint conjunction", predicate)
	}
}
