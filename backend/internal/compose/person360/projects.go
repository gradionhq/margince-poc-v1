// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

// The projects section: the bodies of work this person is part of — a live
// stakeholder seat, or a project of the company they work for today.

import (
	"context"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (s *Service) projectsSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, out *crmcontracts.Person360) error {
	if err := requireRead(ctx, "project"); err != nil {
		return err
	}
	projects, err := s.deals.ListProjectsForPersonTx(ctx, tx, personID)
	if err != nil {
		return err
	}
	out.Projects = &projects
	return nil
}
