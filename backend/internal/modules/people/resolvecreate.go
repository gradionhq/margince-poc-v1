// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The identity chokepoint: the ONE place a person row and the ONE place an
// organization row is minted. Four person paths and four organization paths
// used to each own their own INSERT, and they drifted — capture asked PO-F-2
// about a domain and never about a name, cold start never asked at all, lead
// promotion probed one column by hand. Two companies in a real workspace ended
// up doubled because of it.
//
// Both functions take the PO-F ladder's verdict as an ARGUMENT, and that is
// the point: a create that never consulted dedupe.go cannot be written,
// because there is no verdict to hand over. The verdict is not decoration
// either — an exact-key collision means the record already exists, and these
// functions refuse to mint a second one rather than trusting every caller to
// remember.
//
// What stays with the caller: the RBAC gate, the audit row and the outbox
// event (a promotion audits as `promote`, not `create`), the review-trail
// recording with its per-lane evidence, and the read-back. The write shape is
// unchanged — these run on the caller's transaction, so domain row, audit and
// outbox still commit together.
//
// `backend/dedupespine_test.go` is what keeps this the only door.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// The row-visibility values the create paths choose between. A connector-
// minted row starts owner-private until a human promotes it; the channel bot
// serves the whole workspace and mints shared rows.
const (
	visibilityOwner     = "owner"
	visibilityWorkspace = "workspace"
)

// ownerFromUUID adapts the storage-level owner id the capture and triage paths
// carry to the typed one the specs take.
func ownerFromUUID(u *ids.UUID) *ids.UserID {
	if u == nil {
		return nil
	}
	id := ids.From[ids.UserKind](*u)
	return &id
}

// PersonSpec is every column a person create writes, across all four paths.
// A field left zero writes the column's default — the specs are unions on
// purpose, so a caller's columns are visible at its call site rather than
// hidden behind a policy enum.
type PersonSpec struct {
	FullName  string
	FirstName *string
	LastName  *string
	Title     *string
	OwnerID   *ids.UserID
	Address   *crmcontracts.Address
	Social    map[string]any
	Emails    []PersonEmailInput
	Phones    []PersonPhoneInput

	// Visibility is "" for the column default; capture mints 'owner' rows
	// until a human promotes them, the channel bot mints 'workspace' ones.
	Visibility string
	// Quarantined flags the impersonation tells capture screens for; the row
	// is still created, because hiding suspicious mail is worse than labeling
	// it.
	Quarantined bool
	// ConvertedFromLeadID is set only by promotion — the origin pointer that
	// makes a promoted person traceable to the lead it graduated from.
	ConvertedFromLeadID *ids.UUID

	Source       string
	CapturedBy   string
	CustomFields map[string]any
	// Active is the custom-field catalog for `person`; nil on the capture
	// paths, which carry no request body to source extra columns from.
	Active []fieldcatalog.Column
}

// createPerson is the one INSERT INTO person.
//
// It refuses an exact collision on a lane that names ONE human — a claimed
// address, an established channel binding — because that record already
// exists and the caller was supposed to land on it. The phone lane is
// deliberately not such a lane: a household line and a switchboard belong to
// several real people, so a shared number creates and the caller records the
// pair for review (creatededupe.go states the same policy per lane).
func createPerson(ctx context.Context, tx pgx.Tx, match PersonResolution, spec PersonSpec) (ids.PersonID, error) {
	if match.Decision == DecisionExactCollision && match.MatchedLane != lanePhone {
		return ids.PersonID{}, fmt.Errorf(
			"people: refusing to create a person while PO-F-1 holds an exact %q collision with %s — land on the incumbent instead",
			match.MatchedLane, match.PersonID)
	}
	wsID := workspaceID(ctx)
	id := ids.New[ids.PersonKind]()
	addr := addressColumns(spec.Address)
	cfCols, cfHolders, cfArgs := storekit.InsertFragments(spec.Active, spec.CustomFields, 19)
	args := []any{
		id, wsID, spec.FullName, spec.FirstName, spec.LastName, spec.Title, spec.OwnerID,
		addr.Line1, addr.Line2, addr.City, addr.Region, addr.PostalCode, addr.Country,
		spec.Source, spec.CapturedBy, spec.Visibility, spec.Quarantined, spec.ConvertedFromLeadID,
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO person (id, workspace_id, full_name, first_name, last_name, title, owner_id,
		                     address_line1, address_line2, address_city, address_region, address_postal_code, address_country,
		                     source, captured_by, visibility, quarantined_at, converted_from_lead_id`+cfCols+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
		         coalesce(NULLIF($16, ''), 'workspace'),
		         CASE WHEN $17 THEN now() ELSE NULL END, $18`+cfHolders+`)`,
		append(args, cfArgs...)...); err != nil {
		return ids.PersonID{}, fmt.Errorf("insert person: %w", err)
	}
	if err := replacePersonSocial(ctx, tx, wsID, id, spec.Social); err != nil {
		return ids.PersonID{}, err
	}
	if err := insertPersonEmails(ctx, tx, wsID, id, spec.Source, spec.CapturedBy, spec.Emails); err != nil {
		return ids.PersonID{}, err
	}
	if err := insertPersonPhones(ctx, tx, wsID, id, spec.Source, spec.CapturedBy, spec.Phones); err != nil {
		return ids.PersonID{}, err
	}
	return id, nil
}

// OrgSpec is every column an organization create writes, across all four
// paths.
type OrgSpec struct {
	DisplayName string
	LegalName   *string
	Industry    *string
	SizeBand    *string
	OwnerID     *ids.UserID
	ParentOrgID *ids.OrganizationID
	Address     *crmcontracts.Address
	Domains     []OrgDomainInput

	// NameSource is the ADR-0072/A118 authority ladder entry for the name
	// being written ("" writes the column default, 'human'). A row named from
	// its mail domain is provisional and a richer source may overwrite it; a
	// human's name is never clobbered.
	NameSource string
	// Visibility is "" for the column default; capture mints 'owner' rows.
	Visibility string
	// IsAnchor marks the workspace's own company. There is exactly one
	// (uq_organization_anchor), and it is the one create that does not file a
	// near-match for review: the anchor resembling a captured record of the
	// same company is the expected state during onboarding, not a duplicate
	// pair a human should be asked about.
	IsAnchor bool

	Source       string
	CapturedBy   string
	CustomFields map[string]any
	Active       []fieldcatalog.Column
}

// createOrganization is the one INSERT INTO organization.
//
// It refuses an exact collision outright: PO-F-2's exact tier is the domain,
// and a domain already mapped to a live organization names that same company
// (this is the capture employer-inference path — a hit lands the person on the
// existing company rather than minting a rival).
func createOrganization(ctx context.Context, tx pgx.Tx, match OrganizationMatch, spec OrgSpec) (ids.OrganizationID, error) {
	if match.Decision == DecisionExactCollision {
		return ids.OrganizationID{}, fmt.Errorf(
			"people: refusing to create an organization while PO-F-2 holds an exact domain collision with %s — land on the incumbent instead",
			match.OrganizationID)
	}
	wsID := workspaceID(ctx)
	id := ids.New[ids.OrganizationKind]()
	addr := addressColumns(spec.Address)
	cfCols, cfHolders, cfArgs := storekit.InsertFragments(spec.Active, spec.CustomFields, 20)
	args := []any{
		id, wsID, spec.DisplayName, spec.LegalName, spec.Industry, spec.SizeBand, spec.OwnerID, spec.ParentOrgID,
		addr.Line1, addr.Line2, addr.City, addr.Region, addr.PostalCode, addr.Country,
		spec.Source, spec.CapturedBy, spec.NameSource, spec.Visibility, spec.IsAnchor,
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO organization (id, workspace_id, display_name, legal_name, industry, size_band, owner_id, parent_org_id,
		                           address_line1, address_line2, address_city, address_region, address_postal_code, address_country,
		                           source, captured_by, name_source, visibility, is_anchor`+cfCols+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
		         coalesce(NULLIF($17, ''), 'human'),
		         coalesce(NULLIF($18, ''), 'workspace'), $19`+cfHolders+`)`,
		append(args, cfArgs...)...); err != nil {
		return ids.OrganizationID{}, fmt.Errorf("insert organization: %w", err)
	}
	if err := insertOrgDomains(ctx, tx, wsID, id, spec.Source, spec.CapturedBy, spec.Domains); err != nil {
		return ids.OrganizationID{}, err
	}
	return id, nil
}
