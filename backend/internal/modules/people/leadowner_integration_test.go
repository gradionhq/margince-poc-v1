// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// A lead's owner is exactly what the create names — no actor fallback. The
// funnel's queue state is real: an omitted owner stays NULL for routing or a
// claim, and a named owner is stored as named. Persons/orgs/deals keep the
// storekit.OwnerOrActor default; this pair pins the lead exception so a
// refactor "unifying" the creates cannot quietly bring the fallback back.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestCreateLeadWithoutOwnerStaysUnassigned(t *testing.T) {
	e := setupPromoteConsent(t)

	created, wasCreated, err := e.store.CreateLead(e.ctx, CreateLeadInput{
		FullName: strPtr("Queue Lead"), Source: "manual",
	})
	if err != nil || !wasCreated {
		t.Fatalf("create lead: created=%v err=%v", wasCreated, err)
	}
	if created.OwnerId != nil {
		t.Fatalf("owner_id = %v, want NULL — an omitted owner is the queue, not the caller", created.OwnerId)
	}

	ownerID := ids.From[ids.UserKind](e.user)
	owned, wasCreated, err := e.store.CreateLead(e.ctx, CreateLeadInput{
		FullName: strPtr("Chosen Lead"), Source: "manual", OwnerID: &ownerID,
	})
	if err != nil || !wasCreated {
		t.Fatalf("create owned lead: created=%v err=%v", wasCreated, err)
	}
	if owned.OwnerId == nil || ids.UUID(*owned.OwnerId) != e.user {
		t.Fatalf("owner_id = %v, want the named owner %s", owned.OwnerId, e.user)
	}
}

// An agent's create keeps the on-behalf-of fallback: the claim verb is
// human-only and ownerless rows refuse writes, so a NULL owner would strand
// the very lead the agent just filed.
func TestAgentCreateLeadKeepsTheOnBehalfOwner(t *testing.T) {
	e := setupPromoteConsent(t)
	agentCtx := principal.WithActor(e.ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:sdr", UserID: e.user,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects: map[string]principal.ObjectGrant{
				"lead": {Create: true, Read: true, Update: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	created, wasCreated, err := e.store.CreateLead(agentCtx, CreateLeadInput{
		FullName: strPtr("Agent Filed"), Source: "manual",
	})
	if err != nil || !wasCreated {
		t.Fatalf("agent create lead: created=%v err=%v", wasCreated, err)
	}
	if created.OwnerId == nil || ids.UUID(*created.OwnerId) != e.user {
		t.Fatalf("owner_id = %v, want the granting human %s — an agent's lead must stay workable by that agent", created.OwnerId, e.user)
	}
}
