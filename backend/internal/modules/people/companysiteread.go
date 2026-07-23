// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

const (
	actionCreate              = "create"
	companySiteReadCapturedBy = "agent:site-read"
	eventKeyCapturedBy        = "captured_by"
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
	siteFields, humanFields := splitConfirmedProfile(read.ProfileFields, read.LegalEntities, in)
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
	action := actionUpdate
	if confirmation.created {
		action = actionCreate
	}
	auditID, err := storekit.Audit(ctx, tx, action, "organization", confirmation.organizationID.UUID, nil, map[string]any{
		auditKeySource: companySourceSiteRead, auditKeySourceURL: read.SeedURL,
		auditKeyFields: confirmation.appliedSite, "human_fields": confirmation.appliedHuman,
		auditKeyFacts: confirmation.appliedFacts, "site_read_id": read.ID, "draft_version": read.DraftVersion,
	})
	if err != nil {
		return fmt.Errorf("audit company site-read confirmation: %w", err)
	}
	payload := siteReadConfirmationPayload(read, confirmation)
	if err := storekit.EmitEvent(ctx, tx, auditID, confirmation.organizationID.UUID, payload); err != nil {
		return fmt.Errorf("emit %s: %w", payload.EventType(), err)
	}
	if _, err := tx.Exec(ctx, `UPDATE site_read
		SET organization_id = $2, proposal_ids = $3, confirmed_at = now(), updated_at = now()
		WHERE id = $1`, read.ID, confirmation.organizationID, confirmation.proposalIDs); err != nil {
		return fmt.Errorf("mark website read confirmed: %w", err)
	}
	return nil
}

// siteReadConfirmationPayload builds the organization-side event a
// confirmed site-read emits — organization.created (the union struct)
// when the confirmation minted a fresh organization, or an
// organization.updated changed_fields note when it filled an existing
// one — the ONE place that maps the applied site/human/fact deltas onto
// the published schema. The two shapes are different published events,
// not variants of one, so the return type is the shared events.Payload
// seam.
//
//nolint:ireturn // dispatches to WebhookPayloadOrganizationCreated vs Updated by confirmation.created; tested directly via the interface in person_organization_payload_test.go
func siteReadConfirmationPayload(read SiteRead, confirmation siteReadConfirmation) events.Payload {
	delta := map[string]any{
		auditKeyFields: confirmation.appliedSite,
		"human_fields": confirmation.appliedHuman,
		auditKeyFacts:  confirmation.appliedFacts,
	}
	if confirmation.created {
		source := companySourceSiteRead
		sourceURL := read.SeedURL
		siteReadID := openapi_types.UUID(read.ID)
		capturedBy := companySiteReadCapturedBy
		return crmcontracts.WebhookPayloadOrganizationCreated{
			Delta:      &delta,
			Source:     &source,
			SourceUrl:  &sourceURL,
			SiteReadId: &siteReadID,
			CapturedBy: &capturedBy,
		}
	}
	return crmcontracts.WebhookPayloadOrganizationUpdated{
		ChangedFields: map[string]any{
			eventKeyDelta:  delta,
			auditKeySource: companySourceSiteRead, auditKeySourceURL: read.SeedURL,
			"site_read_id": read.ID, eventKeyCapturedBy: companySiteReadCapturedBy,
		},
	}
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

func splitConfirmedProfile(proposed []DeepReadField, legalEntities []SiteReadLegalEntity, in ConfirmCompanySiteReadInput) ([]ColdStartFieldInput, map[string]*string) {
	byField := make(map[string]DeepReadField, len(proposed))
	for _, field := range proposed {
		byField[field.Field] = field
	}
	for _, field := range selectedLegalEntityFields(legalEntities, in) {
		if _, alreadyProposed := byField[field.Field]; !alreadyProposed {
			byField[field.Field] = field
		}
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

// selectedLegalEntityFields preserves the website provenance of the legal
// block a human selected. The selection decides which entity belongs to this
// installation; it does not turn the entity's printed address and register
// number into claims typed by that human. Every non-blank submitted detail
// must match one and only one stored block, so mixed or edited identities keep
// the normal human provenance.
func selectedLegalEntityFields(entities []SiteReadLegalEntity, in ConfirmCompanySiteReadInput) []DeepReadField {
	legalName := strings.TrimSpace(pointerValue(in.Fields[fieldLegalName]))
	if legalName == "" {
		return nil
	}
	address := strings.TrimSpace(pointerValue(in.Fields[fieldRegisteredAddress]))
	register := strings.TrimSpace(pointerValue(in.Fields[fieldRegisterVat]))
	var selected *SiteReadLegalEntity
	for i := range entities {
		entity := &entities[i]
		if strings.TrimSpace(entity.Name) != legalName ||
			(address != "" && strings.TrimSpace(entity.RegisteredAddress) != address) ||
			(register != "" && strings.TrimSpace(entity.RegisterNumber) != register) {
			continue
		}
		if selected != nil {
			return nil
		}
		selected = entity
	}
	if selected == nil {
		return nil
	}

	fields := []DeepReadField{{
		Field: fieldLegalName, Value: selected.Name, EvidenceSnippet: selected.EvidenceSnippet,
		SourceURL: selected.SourceURL, Confidence: 1,
	}}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: fieldRegisteredAddress, value: selected.RegisteredAddress},
		{name: fieldRegisterVat, value: selected.RegisterNumber},
	} {
		if strings.TrimSpace(field.value) != "" {
			fields = append(fields, DeepReadField{
				Field: field.name, Value: field.value, EvidenceSnippet: selected.EvidenceSnippet,
				SourceURL: selected.SourceURL, Confidence: 1,
			})
		}
	}
	return fields
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
