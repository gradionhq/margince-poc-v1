// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Reading one organization: the three entry points a caller can arrive
// through, and the one row read they all share.
//
// Split from organization.go because that file had grown past the 500-line
// cap carrying the create, the update, the replace-sets and this at once. The
// read is a concept of its own: it is where the computed fields are attached,
// where the archived filter is honoured, and where every column the record
// carries is scanned in one place.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// GetOrganization reads one organization under the caller's own gates: the
// object grant first, then row visibility, then the row itself.
func (s *Store) GetOrganization(ctx context.Context, id ids.OrganizationID, archived storekit.ArchivedFilter) (crmcontracts.Organization, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return crmcontracts.Organization{}, err
	}
	active, err := s.activeColumns(ctx, "organization")
	if err != nil {
		return crmcontracts.Organization{}, err
	}
	var out crmcontracts.Organization
	err = s.tx(ctx, func(tx pgx.Tx) (err error) {
		out, err = getOrganizationInTx(ctx, tx, id, archived, active)
		return err
	})
	return out, err
}

// GetOrganizationTx is GetOrganization for a caller that already opened a
// transaction — the composite record read, which must see every one of its
// sections at the same instant and cannot afford a second connection per
// section. Same gates in the same order; only the transaction is borrowed.
func (s *Store) GetOrganizationTx(ctx context.Context, tx pgx.Tx, id ids.OrganizationID, archived storekit.ArchivedFilter) (crmcontracts.Organization, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return crmcontracts.Organization{}, err
	}
	active, err := s.activeColumns(ctx, "organization")
	if err != nil {
		return crmcontracts.Organization{}, err
	}
	return getOrganizationInTx(ctx, tx, id, archived, active)
}

// getOrganizationInTx is the shared body of the store-opened and
// caller-opened organization reads.
func getOrganizationInTx(ctx context.Context, tx pgx.Tx, id ids.OrganizationID,
	archived storekit.ArchivedFilter, active []fieldcatalog.Column,
) (crmcontracts.Organization, error) {
	if err := auth.EnsureVisible(ctx, tx, "organization", id.UUID); err != nil {
		return crmcontracts.Organization{}, err
	}
	out, err := readOrganization(ctx, tx, id, archived, active)
	if err != nil {
		return crmcontracts.Organization{}, err
	}
	// STATE-4: the gate is a pure permission check (no query), so a
	// caller whose role lacks computed_field:read never pays for the
	// rollup read below, and out.ComputedFields stays its nil zero
	// value — omitempty then drops the key entirely on marshal (T1).
	if computedFieldsVisible(ctx) {
		minor, dealCount, err := openPipelineRollup(ctx, tx, id)
		if err != nil {
			return crmcontracts.Organization{}, fmt.Errorf("read open pipeline rollup: %w", err)
		}
		rows := organizationComputedFields(minor, dealCount)
		out.ComputedFields = &rows
	}
	return out, nil
}

func readOrganization(ctx context.Context, tx pgx.Tx, id ids.OrganizationID, archived storekit.ArchivedFilter, active []fieldcatalog.Column) (crmcontracts.Organization, error) {
	q := `SELECT ` + orgColumns + storekit.SelectSuffix(active) + ` FROM organization WHERE id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND archived_at IS NULL`
	}
	o, err := scanOrganization(tx.QueryRow(ctx, q, id), active)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Organization{}, apperrors.ErrNotFound
	}
	if err != nil {
		return crmcontracts.Organization{}, err
	}
	orgs := []crmcontracts.Organization{o}
	if err := attachOrgDomains(ctx, tx, orgs); err != nil {
		return crmcontracts.Organization{}, err
	}
	if err := attachOrgRelationshipTypes(ctx, tx, orgs); err != nil {
		return crmcontracts.Organization{}, err
	}
	return orgs[0], nil
}

// scanOrganization scans core + active custom columns; extra receives
// any trailing expressions the caller's SELECT appended (the sorted
// list's cursor key).
func scanOrganization(row pgx.Row, active []fieldcatalog.Column, extra ...any) (crmcontracts.Organization, error) {
	var o crmcontracts.Organization
	var id, wsID ids.UUID
	var ownerID, parentID, mergedInto *ids.UUID
	var classification, lifecycle string
	var relevance *int16
	var addr crmcontracts.Address
	var logoObjectKey *string
	var linkedinURL *string
	var version int64

	dests := []any{
		&id, &wsID, &o.DisplayName, &o.LegalName, &o.Description, &o.Industry, &o.SizeBand, &ownerID,
		&addr.Line1, &addr.Line2, &addr.City, &addr.Region, &addr.PostalCode, &addr.Country,
		&classification, &lifecycle, &relevance, &parentID, &mergedInto, &logoObjectKey, &linkedinURL, &o.Source, &o.CapturedBy,
		&version, &o.CreatedAt, &o.UpdatedAt, &o.ArchivedAt, &o.IsAnchor,
	}
	cf := storekit.ScanDests(active)
	if err := row.Scan(append(append(dests, cf...), extra...)...); err != nil {
		return o, err
	}
	if values := storekit.ExtractValues(active, cf); len(values) > 0 {
		o.AdditionalProperties = values
	}

	o.Id = openapi_types.UUID(id)
	o.WorkspaceId = openapi_types.UUID(wsID)
	o.OwnerId = uuidPtr(ownerID)
	o.ParentOrgId = uuidPtr(parentID)
	o.MergedIntoId = uuidPtr(mergedInto)
	cls := crmcontracts.OrganizationClassification(classification)
	o.Classification = &cls
	lc := crmcontracts.OrganizationLifecycle(lifecycle)
	o.Lifecycle = &lc
	o.LogoUrl = LogoURL(id, logoObjectKey)
	o.LinkedinUrl = linkedinURL
	if a := addressOrNil(addr); a != nil {
		o.Address = a
	}
	o.Version = &version
	return o, nil
}
