// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The deal's field masks, applied where the row meets the wire. A rep reads
// every deal in the workspace; the amount of one that is not theirs to change
// is withheld — null, and named in masked_fields so the reader can tell it
// from an amount nobody entered.

import (
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// dealMaskableFields are the columns a mask may name on a deal, and how each
// is withheld. A mask naming a column not listed here is inert: withholding
// is a deliberate act per field, not a reflective one over the struct.
var dealMaskableFields = map[string]func(*crmcontracts.Deal){
	// The money pair goes together: a currency beside a withheld amount
	// would read as a priced deal with its figure missing.
	"amount_minor": func(d *crmcontracts.Deal) { d.AmountMinor, d.Currency = nil, nil },
	"currency":     func(d *crmcontracts.Deal) { d.Currency = nil },
	// The three references. They are withheld by the same mechanism as a role
	// mask because the reader needs the same thing from them: a null they can
	// tell from an empty field. Which rows they are withheld ON is a different
	// question, answered per row by unreadableReferences.
	filterOrganizationID: func(d *crmcontracts.Deal) { d.OrganizationId = nil },
	filterProjectID:      func(d *crmcontracts.Deal) { d.ProjectId = nil },
	filterPartnerOrgID:   func(d *crmcontracts.Deal) { d.PartnerOrgId = nil },
}

// withheldFields is the ordered set of columns withheld from ONE row. Ordered
// because masked_fields goes on the wire, and a client diffing two reads of
// the same deal should not see the list reshuffle under it.
type withheldFields []string

func (w *withheldFields) add(field string) {
	if !slices.Contains(*w, field) {
		*w = append(*w, field)
	}
}

// applyTo withholds every named field from the row and records the names on
// it. A name with no withhold func in dealMaskableFields is dropped rather
// than reported: naming a field in masked_fields while still sending its value
// is a worse answer than either half alone.
func (w withheldFields) applyTo(d *crmcontracts.Deal) {
	named := make([]string, 0, len(w))
	for _, field := range w {
		withhold, known := dealMaskableFields[field]
		if !known {
			continue
		}
		withhold(d)
		named = append(named, field)
	}
	if len(named) > 0 {
		d.MaskedFields = &named
	}
}

// maskDeals withholds, per row, what this reader may not have: the columns
// their ROLE masks, and the references naming a record they could not open.
// Both end the same way — the field goes out null and masked_fields names it —
// so both are collected per row and applied once.
func maskDeals(ctx context.Context, tx pgx.Tx, deals []crmcontracts.Deal) error {
	withheld := make([]withheldFields, len(deals))
	if err := roleMaskedFields(ctx, tx, deals, withheld); err != nil {
		return err
	}
	if err := unreadableReferences(ctx, tx, deals, withheld); err != nil {
		return err
	}
	for i := range deals {
		withheld[i].applyTo(&deals[i])
	}
	return nil
}

// roleMaskedFields collects the columns the caller's role withholds on each
// row. One statement answers which rows of the page the caller could change;
// the masks conditioned on write authority lift on those.
func roleMaskedFields(ctx context.Context, tx pgx.Tx, deals []crmcontracts.Deal, withheld []withheldFields) error {
	p, err := storekit.Actor(ctx)
	if err != nil {
		return err
	}
	// Cheap exit for the common case — no mask on deals at all.
	if len(auth.MaskedFields(p, "deal", false)) == 0 {
		return nil
	}
	rowIDs := make([]ids.UUID, 0, len(deals))
	for _, d := range deals {
		rowIDs = append(rowIDs, ids.UUID(d.Id))
	}
	writable, err := auth.WritableSubset(ctx, tx, "deal", rowIDs)
	if err != nil {
		return err
	}
	for i := range deals {
		for _, field := range auth.MaskedFields(p, "deal", writable[ids.UUID(deals[i].Id)]) {
			withheld[i].add(field)
		}
	}
	return nil
}

// unreadableReferences withholds a deal's links to records the caller could
// not open. Every seat of the workspace reads every deal — a deal is customer
// identity — but the records it POINTS AT are not: an organization can be
// capture-private to the colleague who captured it, and a project keeps its own
// own/team row scope. Handing the id back regardless would make the deal an
// existence oracle over rows the reader's own organization and project reads
// would refuse.
//
// The write path has enforced exactly this rule all along: applyDealLinkPatches
// gates all three references with auth.EnsureLinkTarget before setting them.
// The system already agrees you may not NAME an organization you cannot see;
// this is the half that never asked when handing one back.
//
// ONE statement per referenced table for the whole page, never a probe per row.
func unreadableReferences(ctx context.Context, tx pgx.Tx, deals []crmcontracts.Deal, withheld []withheldFields) error {
	orgIDs := make([]ids.UUID, 0, 2*len(deals))
	projectIDs := make([]ids.UUID, 0, len(deals))
	for _, d := range deals {
		// partner_org_id points at the same table as organization_id, so one
		// organization query answers both arms.
		for _, ref := range []*openapi_types.UUID{d.OrganizationId, d.PartnerOrgId} {
			if ref != nil {
				orgIDs = append(orgIDs, ids.UUID(*ref))
			}
		}
		if d.ProjectId != nil {
			projectIDs = append(projectIDs, ids.UUID(*d.ProjectId))
		}
	}
	// VisibleSubset answers an empty list without a round trip, so a page that
	// names no organization or no project pays for neither.
	visibleOrgs, err := auth.VisibleSubset(ctx, tx, "organization", orgIDs)
	if err != nil {
		return err
	}
	visibleProjects, err := auth.VisibleSubset(ctx, tx, "project", projectIDs)
	if err != nil {
		return err
	}
	for i := range deals {
		d := deals[i]
		if d.OrganizationId != nil && !visibleOrgs[ids.UUID(*d.OrganizationId)] {
			withheld[i].add(filterOrganizationID)
		}
		if d.PartnerOrgId != nil && !visibleOrgs[ids.UUID(*d.PartnerOrgId)] {
			withheld[i].add(filterPartnerOrgID)
		}
		if d.ProjectId != nil && !visibleProjects[ids.UUID(*d.ProjectId)] {
			withheld[i].add(filterProjectID)
		}
	}
	return nil
}

// refuseMaskedSort refuses a sort over a column the caller's role masks on
// any row: ordering by a value is reading it, and a page ordered by amounts
// the caller may not see would disclose them through the order.
func refuseMaskedSort(ctx context.Context, sort *string) error {
	if sort == nil || *sort == "" {
		return nil
	}
	field := strings.TrimPrefix(strings.TrimSpace(*sort), "-")
	if _, maskable := dealMaskableFields[field]; !maskable {
		return nil
	}
	masked, err := auth.MasksAnyRowOf(ctx, "deal", field)
	if err != nil {
		return err
	}
	if masked {
		return &values.ParseError{
			Field: "sort", Code: "field_masked",
			Message: "sort by " + field + " is not available: your role does not read it on every deal",
		}
	}
	return nil
}
