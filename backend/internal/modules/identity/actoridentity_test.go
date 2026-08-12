// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Which human a call is written as, before any database is involved.
//
// The resolution order is the interesting part and it is decided entirely from
// the principal: an agent acting under somebody's authority writes as that
// person, and a principal with nobody behind it writes as nobody. Both are
// answered before the query, so this exercises the decision without a pool.
func TestTheActingHumanIsResolvedFromThePrincipal(t *testing.T) {
	human := ids.New[ids.UserKind]().UUID
	authority := ids.New[ids.UserKind]().UUID

	cases := []struct {
		name  string
		actor principal.Principal
		want  ids.UUID
	}{
		{
			name:  "a human writes as themselves",
			actor: principal.Principal{Type: principal.PrincipalHuman, UserID: human},
			want:  human,
		},
		{
			name: "an agent writes as the human whose authority it holds",
			actor: principal.Principal{
				Type:       principal.PrincipalAgent,
				OnBehalfOf: authority,
			},
			want: authority,
		},
		{
			name: "a human's own id outranks an on-behalf-of that is also present",
			actor: principal.Principal{
				Type:       principal.PrincipalHuman,
				UserID:     human,
				OnBehalfOf: authority,
			},
			want: human,
		},
		{
			name:  "the system principal writes as nobody",
			actor: principal.Principal{Type: principal.PrincipalSystem},
			want:  ids.Nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := principal.WithActor(context.Background(), c.actor)
			if got := actingHuman(ctx); got != c.want {
				t.Fatalf("actingHuman = %v, want %v", got, c.want)
			}
		})
	}
}

// A call carrying no principal at all resolves to nobody rather than panicking.
// It is the shape a background path with no request behind it has.
func TestACallWithNoPrincipalWritesAsNobody(t *testing.T) {
	if got := actingHuman(context.Background()); got != ids.Nil {
		t.Fatalf("actingHuman(no principal) = %v, want the nil id", got)
	}
}

// The draft is unsigned rather than refused when there is nobody to sign as.
// Reaching the database at all would be the defect: there is no row to read.
func TestNobodyToSignAsIsAnsweredWithoutTouchingTheDatabase(t *testing.T) {
	// A nil pool: a query would panic, so surviving this call proves none ran.
	service := &Service{}
	ctx := principal.WithActor(context.Background(),
		principal.Principal{Type: principal.PrincipalSystem})

	name, email, err := service.ActorIdentity(ctx)
	if err != nil {
		t.Fatalf("ActorIdentity errored for the system principal: %v", err)
	}
	if name != "" || email != "" {
		t.Fatalf("ActorIdentity = (%q, %q), want both empty", name, email)
	}
}
