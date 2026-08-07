// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The organization list read: the shared listPage runner bound to the
// organization table — DM-VOCAB-2 sort vocabulary, the shared filter
// chain plus the classification filter, and the organization row scan +
// domain attachment.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// organizationEntity is the organization's auth object and table name.
const organizationEntity = "organization"

// orgNameColumn is the organization's display column — the quick-find
// target and the DM-VOCAB-2 name sort key.
const orgNameColumn = "display_name"

// ListOrganizationsInput carries the organization list's contract
// parameters.
type ListOrganizationsInput struct {
	// IncludeAnchor admits the installation's own company (ADR-0082/A127).
	IncludeAnchor bool
	Cursor        *string
	Limit         *int
	Query         *string
	OwnerID       *ids.UserID
	// Classification is RETIRED with the column (ADR-0079/A124) and reaches no
	// wire parameter; Lifecycle and RelationshipType replace it.
	Classification   *string
	Lifecycle        *string
	RelationshipType *string
	IncludeArchived  bool
	// CapturedByKind filters on the captured_by prefix (ADR-0075/A121 §3a).
	CapturedByKind *string
	// AiWritten filters on whether an AI wrote into the record (§3a).
	AiWritten *bool
	// Sort is the contract's sort spec, validated against the core
	// vocabulary below plus the workspace's active cf_ columns.
	Sort *string
	// CustomFilters carries the request's cf_* query parameters —
	// equality matches against active custom columns (storekit listquery).
	CustomFilters map[string]string
}

// organizationListFields is the organization list's core sortable
// vocabulary — exactly the data-model §13.5 DM-VOCAB-2 set; active cf_
// columns join it per request.
var organizationListFields = map[string]string{
	"created_at":  storekit.KindTimestamp,
	"updated_at":  storekit.KindTimestamp,
	orgNameColumn: fieldcatalog.TypeText,
	ownerIDColumn: storekit.KindUUID,
}

// ListOrganizations is the row-scoped organization list read:
// quick-find, owner, classification and custom-field filters, keyset
// pagination under the validated sort.
func (s *Store) ListOrganizations(ctx context.Context, in ListOrganizationsInput) ([]crmcontracts.Organization, storekit.Page, error) {
	shared := listFilters{
		IncludeArchived: in.IncludeArchived,
		CapturedByKind:  in.CapturedByKind,
		AiWritten:       in.AiWritten,
		entity:          organizationEntity,
		OwnerID:         in.OwnerID,
		Query:           in.Query,
		Cursor:          in.Cursor,
		CustomFilters:   in.CustomFilters,
		nameColumn:      orgNameColumn,
	}
	return listPage(ctx, s, in.Sort, in.Limit, listPageSpec[crmcontracts.Organization]{
		entity:  organizationEntity,
		columns: orgColumns,
		fields:  organizationListFields,
		filters: func(active []fieldcatalog.Column, sorted *storekit.ListSort, arg func(any) int) ([]string, error) {
			where, err := shared.clauses(active, sorted, arg)
			if err != nil {
				return nil, err
			}
			// The installation's own company is not one of the accounts this
			// list answers about, so it is excluded unless asked for
			// (ADR-0082/A127). Appended here beside the other organization-only
			// filters rather than in the shared set: the anchor is a fact about
			// organizations, and no person or deal has one.
			if !in.IncludeAnchor {
				where = append(where, "NOT is_anchor")
			}
			if in.Classification != nil {
				where = append(where, storekit.SQLf("classification = $%d", arg(*in.Classification)))
			}
			// A value outside the enum is a client mistake, not a selection
			// that happens to match nothing: answering 200 with an empty page
			// tells the reader this account list is empty when the question
			// was never one the contract accepts. Validated HERE, inside the
			// store, so it lands after listPage's auth.Require rather than
			// before it.
			if in.Lifecycle != nil {
				if !crmcontracts.ListOrganizationsParamsLifecycle(*in.Lifecycle).Valid() {
					return nil, httperr.Validation("lifecycle", "not_a_known_value",
						"filter by one of the account stages the contract defines, or leave the parameter off")
				}
				where = append(where, storekit.SQLf("lifecycle = $%d", arg(*in.Lifecycle)))
			}
			if in.RelationshipType != nil {
				if !crmcontracts.ListOrganizationsParamsRelationshipType(*in.RelationshipType).Valid() {
					return nil, httperr.Validation("relationship_type", "not_a_known_value",
						"filter by one of the relationship types the contract defines, or leave the parameter off")
				}
				// EXISTS, not a join: an account carries several types and a
				// join would return it once per matching row, which the keyset
				// cursor would then page over as if they were distinct records.
				where = append(where, storekit.SQLf(`EXISTS (
					SELECT 1 FROM organization_relationship_type rt
					WHERE rt.organization_id = organization.id
					  AND rt.relationship_type = $%d AND rt.archived_at IS NULL)`,
					arg(*in.RelationshipType)))
			}
			return where, nil
		},
		scan: scanOrganizationPage,
		attach: func(ctx context.Context, tx pgx.Tx, orgs []crmcontracts.Organization) error {
			if err := attachOrgDomains(ctx, tx, orgs); err != nil {
				return err
			}
			return attachOrgRelationshipTypes(ctx, tx, orgs)
		},
		cursorKey: func(last crmcontracts.Organization) (time.Time, ids.UUID) {
			return last.CreatedAt, ids.UUID(last.Id)
		},
	})
}

// scanOrganizationPage drains one list query's rows: each organization
// plus, under a non-default sort, the row's cursor key (the trailing
// __cursor_key column CursorKeySuffix appended).
func scanOrganizationPage(rows pgx.Rows, active []fieldcatalog.Column, sorted *storekit.ListSort) ([]crmcontracts.Organization, []*string, error) {
	var orgs []crmcontracts.Organization
	var cursorKeys []*string
	for rows.Next() {
		var key *string
		extra := []any{}
		if sorted != nil {
			extra = append(extra, &key)
		}
		o, err := scanOrganization(rows, active, extra...)
		if err != nil {
			return nil, nil, err
		}
		orgs = append(orgs, o)
		cursorKeys = append(cursorKeys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return orgs, cursorKeys, nil
}
