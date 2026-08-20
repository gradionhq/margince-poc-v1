// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The whoami seam: the agents module asks who a call acts for, and identity
// answers. Declared as a function in agents and implemented here for the same
// reason every cross-module edge is (ADR-0054) — agents never imports a
// sibling module.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
)

// actingIdentity reads the human behind the call.
//
// It carries no object gate, and that is identity's own posture rather than an
// omission here: the question is "who is the caller", which a caller may always
// know. What it cannot do is answer for anybody else — the principal decides
// the subject, never an argument.
func actingIdentity(pool *pgxpool.Pool) agents.IdentityReader {
	service := identity.NewService(pool)
	return func(ctx context.Context) (agents.ActingIdentity, error) {
		profile, err := service.ActorProfile(ctx)
		if err != nil {
			return agents.ActingIdentity{}, err
		}
		return agents.ActingIdentity{
			UserID:      profile.UserID,
			DisplayName: profile.DisplayName,
			Email:       profile.Email,
			Locale:      profile.Locale,
			Timezone:    profile.Timezone,
		}, nil
	}
}
