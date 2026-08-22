// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// Narrowing the company page to one body of work.

import (
	"context"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// projectScope renders the body-of-work narrowing as one more WHERE term on
// the activity alias `a`, or nothing when the page is unscoped. Every
// activity-reading section goes through it — timeline, last touch, next
// steps, next meeting, the since-last-visit count — so the page cannot show
// one project's rows beside another project's date or task.
func (o AssembleOptions) projectScope(arg func(any) int) string {
	if o.ProjectID == nil {
		return ""
	}
	return " AND " + activities.ActivityWithinProject(arg(*o.ProjectID))
}

// admit gates the narrowing and reports it. The scope is a read of the
// project, checked before any section filters on it — and as the whole
// read's refusal, not a section's: a page narrowed to a project the caller
// may not see has no honest sections at all. The report beside it is what
// lets a surface say how much of the account the narrowing kept.
func (o AssembleOptions) admit(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, out *crmcontracts.Organization360) error {
	if o.ProjectID == nil {
		return nil
	}
	if err := activities.RequireProjectScope(ctx, tx, *o.ProjectID); err != nil {
		return err
	}
	scope, err := activities.ReadProjectScope(ctx, tx, *o.ProjectID, func(arg func(any) int) string {
		return activities.OrgLinkedActivityExists(arg(orgID))
	})
	if err != nil {
		return err
	}
	wired := scope.Wire()
	out.Scope = &wired
	return nil
}
