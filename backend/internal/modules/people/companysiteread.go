// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

const (
	actionCreate              = "create"
	companySiteReadCapturedBy = "agent:site-read"
	eventKeyCapturedBy        = "captured_by"
	eventOrganizationCreated  = "organization.created"
	eventOrganizationUpdated  = "organization.updated"
)

// ConfirmCompanySiteReadInput is the inspected onboarding draft plus the
// human's selected profile and fact subset.
type ConfirmCompanySiteReadInput struct {
	ReadID                 ids.UUID
	DraftVersion           int
	ProposalHash           string
	DisplayName            string
	Website                *string
	Fields                 map[string]*string
	SelectedFactKeys       []string
	Resolutions            []SiteReadResolution
	skipProfileFields      map[string]bool
	overwriteProfileFields map[string]bool
	overwriteFactKeys      map[string]bool
	humanFactEdits         []resolvedHumanFact
}

// StageSiteReadPeople stages the dossier's published people after the anchor
// exists. The callback runs inside the company confirmation transaction.
type StageSiteReadPeople func(context.Context, pgx.Tx, ids.OrganizationID, SiteRead, []SiteReadPerson) ([]ids.UUID, error)

// SiteReadFactKey is the stable selection key exposed by the dossier wire.
// It includes category and field because singleton facts legitimately carry
// an empty storage value_key.
func SiteReadFactKey(f DeepReadFact) string {
	return f.Category + "/" + f.Field + "/" + f.ValueKey
}

// ConfirmCompanySiteRead atomically binds the inspected draft, creates or
// updates the anchor, writes the selected profile/facts, stages people
// separately, and marks the dossier confirmed. A stale or replayed draft
// changes nothing.
func (s *Store) ConfirmCompanySiteRead(ctx context.Context, in ConfirmCompanySiteReadInput, stagePeople StageSiteReadPeople) (Company, error) {
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return Company{}, err
	}
	var out Company
	err = s.tx(ctx, func(tx pgx.Tx) error {
		out, err = s.confirmCompanySiteReadTx(ctx, tx, in, by, stagePeople)
		return err
	})
	return out, err
}

type siteReadConfirmation struct {
	organizationID ids.OrganizationID
	created        bool
	appliedSite    map[string]any
	appliedHuman   map[string]any
	appliedFacts   []map[string]any
	proposalIDs    []ids.UUID
}

func (s *Store) confirmCompanySiteReadTx(
	ctx context.Context,
	tx pgx.Tx,
	in ConfirmCompanySiteReadInput,
	by string,
	stagePeople StageSiteReadPeople,
) (Company, error) {
	if err := lockCompanyState(ctx, tx); err != nil {
		return Company{}, err
	}
	read, err := lockOnboardingSiteRead(ctx, tx, in.ReadID)
	if err != nil {
		return Company{}, err
	}
	if err := validateSiteReadConfirmation(read, in); err != nil {
		return Company{}, err
	}
	current, err := readAnchorForComparison(ctx, tx)
	if err != nil {
		return Company{}, err
	}
	in, err = resolveSiteReadConflicts(read, current, in)
	if err != nil {
		return Company{}, err
	}

	confirmation, err := applySiteReadConfirmation(ctx, tx, read, in, by)
	if err != nil {
		return Company{}, err
	}
	confirmation.proposalIDs, err = stageConfirmedSiteReadPeople(ctx, tx, confirmation.organizationID, read, stagePeople)
	if err != nil {
		return Company{}, err
	}
	if err := recordSiteReadConfirmation(ctx, tx, read, confirmation); err != nil {
		return Company{}, err
	}
	return readCompany(ctx, tx, confirmation.organizationID)
}

func validateSiteReadConfirmation(read SiteRead, in ConfirmCompanySiteReadInput) error {
	if read.ConfirmedAt != nil {
		return fmt.Errorf("the website read was already confirmed: %w", apperrors.ErrConflict)
	}
	if read.Status != "done" && read.Status != "partial" {
		return fmt.Errorf("the website read is %s, not ready to confirm: %w", read.Status, apperrors.ErrConflict)
	}
	if read.DraftVersion != in.DraftVersion || read.ProposalHash != in.ProposalHash {
		return fmt.Errorf("the website draft changed since it was reviewed: %w", apperrors.ErrVersionSkew)
	}
	return nil
}

func applySiteReadConfirmation(
	ctx context.Context,
	tx pgx.Tx,
	read SiteRead,
	in ConfirmCompanySiteReadInput,
	by string,
) (siteReadConfirmation, error) {
	orgID, created, err := resolveOrCreateAnchor(ctx, tx, in.DisplayName, by)
	if err != nil {
		return siteReadConfirmation{}, err
	}
	siteFields, humanFields := splitConfirmedProfile(read.ProfileFields, in)
	appliedSite, err := applyEvidenceFieldsWithOverwrite(ctx, tx, workspaceID(ctx), orgID,
		companySourceSiteRead, companySiteReadCapturedBy, siteFields, in.overwriteProfileFields)
	if err != nil {
		return siteReadConfirmation{}, err
	}
	appliedHuman, err := writeCompanyFields(ctx, tx, orgID, by, humanFields)
	if err != nil {
		return siteReadConfirmation{}, err
	}
	if in.Website != nil {
		if err := setCompanyDomain(ctx, tx, orgID, *in.Website, by); err != nil {
			return siteReadConfirmation{}, err
		}
	}
	appliedFacts, err := applySelectedSiteReadFacts(ctx, tx, orgID, read, in.SelectedFactKeys, in.overwriteFactKeys)
	if err != nil {
		return siteReadConfirmation{}, err
	}
	humanFacts, err := applyResolvedHumanFacts(ctx, tx, orgID, by, in.humanFactEdits)
	if err != nil {
		return siteReadConfirmation{}, err
	}
	appliedFacts = append(appliedFacts, humanFacts...)
	return siteReadConfirmation{
		organizationID: orgID,
		created:        created,
		appliedSite:    appliedSite,
		appliedHuman:   appliedHuman,
		appliedFacts:   appliedFacts,
	}, nil
}

func applySelectedSiteReadFacts(
	ctx context.Context,
	tx pgx.Tx,
	orgID ids.OrganizationID,
	read SiteRead,
	selectedKeys []string,
	overwriteKeys map[string]bool,
) ([]map[string]any, error) {
	selectedFacts, err := selectSiteReadFacts(read.Facts, selectedKeys)
	if err != nil {
		return nil, err
	}
	for _, fact := range selectedFacts {
		if !overwriteKeys[SiteReadFactKey(fact)] {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM organization_fact
			WHERE workspace_id = $1 AND organization_id = $2 AND category = $3
			  AND field = $4 AND value_key = $5 AND source = $6`,
			workspaceID(ctx), orgID, fact.Category, fact.Field, fact.ValueKey, companySourceHuman); err != nil {
			return nil, fmt.Errorf("replace accepted human organization fact %s.%s: %w",
				fact.Category, fact.Field, err)
		}
	}
	return upsertOrganizationFacts(ctx, tx, workspaceID(ctx), DeepReadProposal{
		OrganizationID: orgID,
		SourceURL:      read.SeedURL,
		SiteReadID:     read.ID,
		Facts:          selectedFacts,
	}, companySiteReadCapturedBy)
}

func stageConfirmedSiteReadPeople(
	ctx context.Context,
	tx pgx.Tx,
	orgID ids.OrganizationID,
	read SiteRead,
	stagePeople StageSiteReadPeople,
) ([]ids.UUID, error) {
	if stagePeople == nil || len(read.People) == 0 {
		return nil, nil
	}
	proposalIDs, err := stagePeople(ctx, tx, orgID, read, read.People)
	if err != nil {
		return nil, fmt.Errorf("stage website people: %w", err)
	}
	return proposalIDs, nil
}

func recordSiteReadConfirmation(ctx context.Context, tx pgx.Tx, read SiteRead, confirmation siteReadConfirmation) error {
	action, eventType := actionUpdate, eventOrganizationUpdated
	if confirmation.created {
		action, eventType = actionCreate, eventOrganizationCreated
	}
	auditID, err := storekit.Audit(ctx, tx, action, "organization", confirmation.organizationID.UUID, nil, map[string]any{
		auditKeySource: companySourceSiteRead, auditKeySourceURL: read.SeedURL,
		auditKeyFields: confirmation.appliedSite, "human_fields": confirmation.appliedHuman,
		auditKeyFacts: confirmation.appliedFacts, "site_read_id": read.ID, "draft_version": read.DraftVersion,
	})
	if err != nil {
		return fmt.Errorf("audit company site-read confirmation: %w", err)
	}
	if err := storekit.Emit(ctx, tx, auditID, eventType, "organization", confirmation.organizationID.UUID, map[string]any{
		eventKeyDelta: map[string]any{
			auditKeyFields: confirmation.appliedSite,
			"human_fields": confirmation.appliedHuman,
			auditKeyFacts:  confirmation.appliedFacts,
		},
		auditKeySource: companySourceSiteRead, auditKeySourceURL: read.SeedURL,
		"site_read_id": read.ID, eventKeyCapturedBy: companySiteReadCapturedBy,
	}); err != nil {
		return fmt.Errorf("emit %s: %w", eventType, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE site_read
		SET organization_id = $2, proposal_ids = $3, confirmed_at = now(), updated_at = now()
		WHERE id = $1`, read.ID, confirmation.organizationID, confirmation.proposalIDs); err != nil {
		return fmt.Errorf("mark website read confirmed: %w", err)
	}
	return nil
}

func lockOnboardingSiteRead(ctx context.Context, tx pgx.Tx, readID ids.UUID) (SiteRead, error) {
	row := tx.QueryRow(ctx, `SELECT `+siteReadColumns+` FROM site_read
		WHERE id = $1 AND target_kind = 'onboarding' FOR UPDATE`, readID)
	read, err := scanSiteRead(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return SiteRead{}, apperrors.ErrNotFound
	}
	if err != nil {
		return SiteRead{}, fmt.Errorf("lock onboarding site read: %w", err)
	}
	return read, nil
}

func splitConfirmedProfile(proposed []DeepReadField, in ConfirmCompanySiteReadInput) ([]ColdStartFieldInput, map[string]*string) {
	byField := make(map[string]DeepReadField, len(proposed))
	for _, field := range proposed {
		byField[field.Field] = field
	}
	values := make(map[string]*string, len(in.Fields)+1)
	for field, value := range in.Fields {
		values[field] = value
	}
	values[fieldDisplayName] = &in.DisplayName

	siteFields := make([]ColdStartFieldInput, 0, len(values))
	humanFields := make(map[string]*string, len(values))
	for field, value := range values {
		if in.skipProfileFields[field] {
			continue
		}
		if value == nil {
			continue
		}
		trimmed := strings.TrimSpace(*value)
		proposal, exact := byField[field]
		if exact && trimmed == strings.TrimSpace(proposal.Value) {
			siteFields = append(siteFields, ColdStartFieldInput(proposal))
			continue
		}
		humanFields[field] = value
	}
	return siteFields, humanFields
}

func selectSiteReadFacts(proposed []DeepReadFact, selected []string) ([]DeepReadFact, error) {
	byKey := make(map[string]DeepReadFact, len(proposed))
	for _, fact := range proposed {
		byKey[SiteReadFactKey(fact)] = fact
	}
	out := make([]DeepReadFact, 0, len(selected))
	seen := make(map[string]bool, len(selected))
	for _, key := range selected {
		if seen[key] {
			return nil, fmt.Errorf("people: selected fact key %q appears more than once", key)
		}
		fact, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("people: selected fact key %q is not in the inspected website draft: %w", key, apperrors.ErrVersionSkew)
		}
		seen[key] = true
		out = append(out, fact)
	}
	return out, nil
}
