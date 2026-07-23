// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestRequireHumanRejectsOnlyAgents(t *testing.T) {
	// The agent (Passport) principal is refused whatever its authority — the
	// human-only sheet must never answer an agent bearer, even one minted by
	// an admin with the object grant.
	agentCtx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:test",
	})
	if err := RequireHuman(agentCtx); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("RequireHuman(agent) = %v, want ErrPermissionDenied", err)
	}

	// Human, connector and system are not agents and pass.
	for _, typ := range []principal.PrincipalType{
		principal.PrincipalHuman, principal.PrincipalConnector, principal.PrincipalSystem,
	} {
		ctx := principal.WithActor(context.Background(), principal.Principal{Type: typ, ID: "id"})
		if err := RequireHuman(ctx); err != nil {
			t.Errorf("RequireHuman(%s) = %v, want nil", typ, err)
		}
	}
}

func TestRequireHumanNeedsAnActor(t *testing.T) {
	// A missing actor is a programming error (middleware always binds one),
	// surfaced as an error rather than a silent pass.
	if err := RequireHuman(context.Background()); err == nil {
		t.Fatal("RequireHuman(no actor) = nil, want an error")
	}
}
