// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// What a company IS to us (PO-DDL-4b, ADR-0079/A124) — the multi-valued half
// of the split that retired organization.classification.
//
// It is multi-valued because a company is legitimately several things at once:
// the partner program (A38/ADR-0030) is built on companies that are
// simultaneously partners and customers, and an agency is often a reseller and
// a client. The single enum could hold one of those and silently erased the
// rest.
//
// The rows are an owned child of the organization, written only through the
// organization's own gated paths — the patch below and the partner upsert —
// which is the visibility decision the schema fitness gate records.

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// relationshipTypePartner is the one type bound to another row's existence.
const relationshipTypePartner = string(crmcontracts.OrganizationRelationshipTypesPartner)

// validRelationshipTypes is the closed vocabulary, checked before the database
// sees it so a bad value is a 422 naming the field rather than a CHECK
// violation surfacing as a 500.
var validRelationshipTypes = map[string]bool{
	string(crmcontracts.OrganizationRelationshipTypesCustomer):         true,
	string(crmcontracts.OrganizationRelationshipTypesPartner):          true,
	string(crmcontracts.OrganizationRelationshipTypesSupplier):         true,
	string(crmcontracts.OrganizationRelationshipTypesInvestor):         true,
	string(crmcontracts.OrganizationRelationshipTypesPortfolioCompany): true,
	string(crmcontracts.OrganizationRelationshipTypesCompetitor):       true,
	string(crmcontracts.OrganizationRelationshipTypesOther):            true,
}

// dedupeRelationshipTypes collapses repeats and rejects anything outside the
// vocabulary, so the reconcile and the audit-after both see one row per type.
// The order is normalized too: a caller sending the same set in a different
// order must produce the same audit image, or every re-save reads as a change.
func dedupeRelationshipTypes(desired []string) ([]string, error) {
	seen := make(map[string]bool, len(desired))
	out := make([]string, 0, len(desired))
	for _, t := range desired {
		if !validRelationshipTypes[t] {
			return nil, httperr.Validation("relationship_types", "invalid_enum",
				fmt.Sprintf("%q is not a relationship type", t))
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// attachOrgRelationshipTypes fills the wire field for a page of organizations
// in one query, the same batch shape attachOrgDomains uses.
//
// An organization with no types gets an EMPTY slice rather than a nil one: the
// reader asked what this company is to us and the answer is "nothing recorded",
// which is a fact, not a missing field.
func attachOrgRelationshipTypes(ctx context.Context, tx pgx.Tx, orgs []crmcontracts.Organization) error {
	if len(orgs) == 0 {
		return nil
	}
	idx := make(map[openapi_types.UUID]*crmcontracts.Organization, len(orgs))
	orgIDs := make([]ids.UUID, len(orgs))
	for i := range orgs {
		idx[orgs[i].Id] = &orgs[i]
		orgIDs[i] = ids.UUID(orgs[i].Id)
		empty := []crmcontracts.OrganizationRelationshipTypes{}
		orgs[i].RelationshipTypes = &empty
	}

	rows, err := tx.Query(ctx,
		`SELECT organization_id, relationship_type
		 FROM organization_relationship_type
		 WHERE organization_id = ANY($1) AND archived_at IS NULL
		 ORDER BY relationship_type`, orgIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var orgID ids.UUID
		var relType string
		if err := rows.Scan(&orgID, &relType); err != nil {
			return err
		}
		o, ok := idx[openapi_types.UUID(orgID)]
		if !ok {
			continue
		}
		*o.RelationshipTypes = append(*o.RelationshipTypes, crmcontracts.OrganizationRelationshipTypes(relType))
	}
	return rows.Err()
}

// readLiveRelationshipTypes is the current set, for the reconcile's before
// image and for the partner-invariant guard.
func readLiveRelationshipTypes(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT relationship_type FROM organization_relationship_type
		 WHERE organization_id = $1 AND archived_at IS NULL
		 ORDER BY relationship_type`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var live []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		live = append(live, t)
	}
	return live, rows.Err()
}

// hasPartnerRow reports whether the organization carries the partner program
// extension — the other half of the invariant this file guards.
func hasPartnerRow(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM partner WHERE organization_id = $1)`, orgID).Scan(&exists)
	return exists, err
}

// reconcileOrgRelationshipTypes applies the replace-set: insert what is new,
// archive what is gone, leave the rest alone so an untouched type keeps its
// original provenance and created_at. Returns the before image for the audit.
//
// It refuses to drop `partner` while the partner extension row lives. ADR-0032
// bound partnerhood to a classification value and nothing enforced it; ADR-0079
// moves the invariant here and this is where it is kept. Refusing is the honest
// answer: the caller is asking for a state the partner APIs would contradict,
// and silently keeping the row would make the request a lie in the other
// direction.
func reconcileOrgRelationshipTypes(
	ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, orgID ids.OrganizationID, source, by string, desired []string,
) ([]string, error) {
	live, err := readLiveRelationshipTypes(ctx, tx, orgID)
	if err != nil {
		return nil, err
	}
	desiredSet := make(map[string]bool, len(desired))
	for _, t := range desired {
		desiredSet[t] = true
	}
	if !desiredSet[relationshipTypePartner] {
		isPartner, err := hasPartnerRow(ctx, tx, orgID)
		if err != nil {
			return nil, err
		}
		if isPartner {
			return nil, httperr.Validation("relationship_types", "partner_row_exists",
				"this company has a partner programme, so it stays a partner — remove the programme first")
		}
	}

	liveSet := make(map[string]bool, len(live))
	for _, t := range live {
		liveSet[t] = true
	}
	for _, t := range desired {
		if liveSet[t] {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO organization_relationship_type
			   (workspace_id, organization_id, relationship_type, source, captured_by)
			 VALUES ($1, $2, $3, $4, $5)`,
			wsID, orgID, t, source, by); err != nil {
			return nil, fmt.Errorf("insert relationship type %q: %w", t, err)
		}
	}
	for _, t := range live {
		if desiredSet[t] {
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE organization_relationship_type SET archived_at = now()
			 WHERE organization_id = $1 AND relationship_type = $2 AND archived_at IS NULL`,
			orgID, t); err != nil {
			return nil, fmt.Errorf("archive relationship type %q: %w", t, err)
		}
	}
	return live, nil
}

// ensureOrgRelationshipType asserts one type without disturbing the others.
// The partner upsert calls it to keep its half of the invariant, inside the
// same transaction that writes the partner row — the classification flip it
// replaces did exactly this, for exactly this reason.
func ensureOrgRelationshipType(
	ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, orgID ids.OrganizationID, relType, source, by string,
) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO organization_relationship_type
		   (workspace_id, organization_id, relationship_type, source, captured_by)
		 SELECT $1, $2, $3, $4, $5
		 WHERE NOT EXISTS (
		   SELECT 1 FROM organization_relationship_type
		   WHERE organization_id = $2 AND relationship_type = $3 AND archived_at IS NULL)`,
		wsID, orgID, relType, source, by)
	if err != nil {
		return fmt.Errorf("ensure relationship type %q: %w", relType, err)
	}
	return nil
}

// moveOrgRelationshipTypes re-homes the loser's types onto the survivor in a
// merge, so the survivor ends up with the UNION of both. Types the survivor
// already carries are archived on the loser rather than moved, because the
// unique index admits one live row per (organization, type) and the survivor's
// own row is the one with the older provenance worth keeping.
func moveOrgRelationshipTypes(ctx context.Context, tx pgx.Tx, from, to ids.OrganizationID) error {
	if _, err := tx.Exec(ctx,
		`UPDATE organization_relationship_type l SET archived_at = now()
		 WHERE l.organization_id = $1 AND l.archived_at IS NULL
		   AND EXISTS (SELECT 1 FROM organization_relationship_type s
		               WHERE s.organization_id = $2 AND s.relationship_type = l.relationship_type
		                 AND s.archived_at IS NULL)`, from, to); err != nil {
		return fmt.Errorf("archive duplicate relationship types on merge: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE organization_relationship_type SET organization_id = $2
		 WHERE organization_id = $1 AND archived_at IS NULL`, from, to); err != nil {
		return fmt.Errorf("re-home relationship types on merge: %w", err)
	}
	return nil
}

// lifecycleValue reads the wire's optional lifecycle as the string the patch
// compares against. The column is NOT NULL, so a read always carries one; the
// pointer is the contract's shape, not a real absence, and an empty before
// image would make every first edit look like a change from nothing.
func lifecycleValue(l *crmcontracts.OrganizationLifecycle) string {
	if l == nil {
		return string(crmcontracts.OrganizationLifecycleUnknown)
	}
	return string(*l)
}
