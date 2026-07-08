// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package database

import (
	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// registeredIDTypes lists one scalar and one slice specimen per entity
// id. Scalars encode/scan through the stdlib driver.Valuer/sql.Scanner
// fallbacks without registration; the registration exists for the
// SLICE forms — pgx can only encode []ids.PersonID as a uuid[] bind
// parameter (the `= ANY($1)` idiom) when the element's default OID is
// known.
//
//craft:ignore naked-any pgtype registration is inherently untyped: it takes specimen values of arbitrary Go types
var registeredIDTypes = []struct{ scalar, slice any }{
	{ids.WorkspaceID{}, []ids.WorkspaceID{}},
	{ids.UserID{}, []ids.UserID{}},
	{ids.TeamID{}, []ids.TeamID{}},
	{ids.PersonID{}, []ids.PersonID{}},
	{ids.OrganizationID{}, []ids.OrganizationID{}},
	{ids.LeadID{}, []ids.LeadID{}},
	{ids.DealID{}, []ids.DealID{}},
	{ids.PipelineID{}, []ids.PipelineID{}},
	{ids.StageID{}, []ids.StageID{}},
	{ids.OfferID{}, []ids.OfferID{}},
	{ids.ProductID{}, []ids.ProductID{}},
	{ids.ActivityID{}, []ids.ActivityID{}},
	{ids.SignalID{}, []ids.SignalID{}},
	{ids.ListID{}, []ids.ListID{}},
	{ids.TagID{}, []ids.TagID{}},
	{ids.SavedViewID{}, []ids.SavedViewID{}},
	{ids.ApprovalID{}, []ids.ApprovalID{}},
	{ids.AutomationID{}, []ids.AutomationID{}},
	{ids.PassportID{}, []ids.PassportID{}},
	{ids.PurposeID{}, []ids.PurposeID{}},
}

// RegisterIDTypes binds every typed entity id (and its slice) to the
// uuid / uuid[] wire types on one connection's type map. Wired into
// NewPool via AfterConnect, so every pooled connection speaks the
// typed ids from its first query.
func RegisterIDTypes(conn *pgx.Conn) {
	m := conn.TypeMap()
	for _, t := range registeredIDTypes {
		m.RegisterDefaultPgType(t.scalar, "uuid")
		m.RegisterDefaultPgType(t.slice, "_uuid")
	}
}
