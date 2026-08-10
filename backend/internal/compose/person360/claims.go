// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

// The conversation claims behind the commitments card and the what-matters
// card (ADR-0097 D1).
//
// One read, both cards. They render different kinds of the same rows, so
// reading them twice would be two queries that could disagree about what this
// person said.

import (
	"context"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// claimsSection reads what was promised, asked and decided.
//
// The gate is the ACTIVITY read, not a gate of its own: every claim quotes a
// captured message, and a reader who may not open the message may not read the
// quote. The store's own query carries that predicate.
func (s *Service) claimsSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, out *crmcontracts.Person360) error {
	if err := requireRead(ctx, "activity"); err != nil {
		return err
	}
	claims, err := s.people.ClaimsForPerson(ctx, tx, personID, sectionCap)
	if err != nil {
		return err
	}
	out.Claims = &claims
	return nil
}
