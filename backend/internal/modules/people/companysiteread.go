// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"

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

// The confirm path refuses a dossier for two distinct reasons that are not the
// same news to a caller, so each carries its own error. They stay module-owned
// rather than joining the shared sentinel registry (which interfaces.md §0
// fixes) because they mean nothing outside this operation; the transport turns
// each into its own contract code, the way capture's backfill refusals and
// search's live-reindex refusal already do.

// ErrSiteReadAlreadyConfirmed refuses a dossier whose decision is already made:
// replaying it would confirm the same draft twice.
var ErrSiteReadAlreadyConfirmed = errors.New("people: the website read was already confirmed")

// ErrSiteReadNotConfirmable refuses a dossier that has no draft to confirm —
// still reading, or ended without one. Nothing was decided and the caller may
// try again once the read reaches a terminal state.
var ErrSiteReadNotConfirmable = errors.New("people: the website read is not ready to confirm")

// ConfirmCompanySiteReadInput is the inspected onboarding draft plus the
// human's selected profile and fact subset.
type ConfirmCompanySiteReadInput struct {
	ReadID           ids.UUID
	DraftVersion     int
	ProposalHash     string
	DisplayName      string
	Website          *string
	Fields           map[string]*string
	SelectedFactKeys []string
	Resolutions      []SiteReadResolution
	// ReclaimUnadoptedLogo declares that the caller owns an object store and
	// will collect the mark the confirmation reports as unadopted. A caller
	// that owns none leaves it false, and the dossier keeps its reference —
	// releaseParkedSiteReadLogo carries why that reference must not be dropped
	// by anybody who cannot delete the bytes behind it.
	ReclaimUnadoptedLogo   bool
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
//
// It also hands back the storage key of a mark the anchor did NOT adopt,
// because a logo already holds that field, so the caller collects bytes no
// record wears. Nil is the ordinary answer: the read parked no mark, the anchor
// adopted it, or the caller declared no object store to collect it with. Same
// contract as SetOrganizationLogo and RecordSiteReadLogo — a store reports a
// collection, it never performs one.
func (s *Store) ConfirmCompanySiteRead(ctx context.Context, in ConfirmCompanySiteReadInput, stagePeople StageSiteReadPeople) (Company, *string, error) {
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return Company{}, nil, err
	}
	var out Company
	var unadoptedLogo *string
	err = s.tx(ctx, func(tx pgx.Tx) error {
		out, unadoptedLogo, err = s.confirmCompanySiteReadTx(ctx, tx, in, by, stagePeople)
		return err
	})
	if err != nil {
		// The transaction rolled back, so the dossier still names whatever it
		// parked. Reporting a key here would ask the caller to delete bytes a
		// row is still pointing at.
		return Company{}, nil, err
	}
	return out, unadoptedLogo, nil
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
) (Company, *string, error) {
	if err := lockCompanyState(ctx, tx); err != nil {
		return Company{}, nil, err
	}
	read, err := lockOnboardingSiteRead(ctx, tx, in.ReadID)
	if err != nil {
		return Company{}, nil, err
	}
	if err := validateSiteReadConfirmation(read, in); err != nil {
		return Company{}, nil, err
	}
	current, err := readAnchorForComparison(ctx, tx)
	if err != nil {
		return Company{}, nil, err
	}
	in, err = resolveSiteReadConflicts(read, current, in)
	if err != nil {
		return Company{}, nil, err
	}

	confirmation, err := applySiteReadConfirmation(ctx, tx, read, in, by)
	if err != nil {
		return Company{}, nil, err
	}
	confirmation.proposalIDs, err = stageConfirmedSiteReadPeople(ctx, tx, confirmation.organizationID, read, stagePeople)
	if err != nil {
		return Company{}, nil, err
	}
	if err := recordSiteReadConfirmation(ctx, tx, read, confirmation); err != nil {
		return Company{}, nil, err
	}
	// The logo lands AFTER the confirmation's own event, never before it. Its
	// write publishes organization.updated, and the confirmation that mints the
	// anchor publishes organization.created; the outbox ships a single entity's
	// rows in insert order, so binding first would hand a consumer an update for
	// an organization it has not been told about yet.
	unadoptedLogo, err := bindSiteReadLogo(ctx, tx, read.ID, confirmation.organizationID, in.ReclaimUnadoptedLogo)
	if err != nil {
		return Company{}, nil, err
	}
	company, err := readCompany(ctx, tx, confirmation.organizationID)
	if err != nil {
		return Company{}, nil, err
	}
	return company, unadoptedLogo, nil
}

func validateSiteReadConfirmation(read SiteRead, in ConfirmCompanySiteReadInput) error {
	if read.ConfirmedAt != nil {
		return ErrSiteReadAlreadyConfirmed
	}
	if read.Status != "done" && read.Status != "partial" {
		return fmt.Errorf("its status is %s: %w", read.Status, ErrSiteReadNotConfirmable)
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
	// coalesce, because "staged nobody" is an EMPTY list and not an unknown
	// one. A read of a site that names no contactable person stages nothing
	// and hands back a nil slice, which encodes as SQL NULL against a NOT NULL
	// column — so confirming an ordinary company page failed outright, at the
	// last step of onboarding, with nothing but a 500 to show for it.
	if _, err := tx.Exec(ctx, `UPDATE site_read
		SET organization_id = $2, proposal_ids = coalesce($3, '{}'::uuid[]),
		    confirmed_at = now(), updated_at = now()
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
//nolint:ireturn // dispatches to PublicEventOrganizationCreated vs Updated by confirmation.created; tested directly via the interface in person_organization_payload_test.go
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
		return crmcontracts.PublicEventOrganizationCreated{
			Delta:      &delta,
			Source:     &source,
			SourceUrl:  &sourceURL,
			SiteReadId: &siteReadID,
			CapturedBy: &capturedBy,
		}
	}
	return crmcontracts.PublicEventOrganizationUpdated{
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
		proposal, fromRead := byField[field]
		if fromRead && samePrintedValue(*value, proposal.Value) {
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
//
// Submitted and stored details meet through PrintedSiteReadValue, the spelling
// the option a human picked was built in: comparing the raw extraction instead
// would refuse the pick for every entity whose printed identity carries a run
// of whitespace, and file the page's own words as that human's assertion.
func selectedLegalEntityFields(entities []SiteReadLegalEntity, in ConfirmCompanySiteReadInput) []DeepReadField {
	legalName := PrintedSiteReadValue(pointerValue(in.Fields[fieldLegalName]))
	if legalName == "" {
		return nil
	}
	address := PrintedSiteReadValue(pointerValue(in.Fields[fieldRegisteredAddress]))
	register := PrintedSiteReadValue(pointerValue(in.Fields[fieldRegisterVat]))
	var selected *SiteReadLegalEntity
	for i := range entities {
		entity := &entities[i]
		if !samePrintedValue(entity.Name, legalName) ||
			(address != "" && !samePrintedValue(entity.RegisteredAddress, address)) ||
			(register != "" && !samePrintedValue(entity.RegisterNumber, register)) {
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

	// The value lands in the spelling the human was shown and picked, not the
	// raw run the extraction kept — the record carries what the page reads as.
	fields := []DeepReadField{{
		Field: fieldLegalName, Value: PrintedSiteReadValue(selected.Name), EvidenceSnippet: selected.EvidenceSnippet,
		SourceURL: selected.SourceURL, Confidence: 1,
	}}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: fieldRegisteredAddress, value: PrintedSiteReadValue(selected.RegisteredAddress)},
		{name: fieldRegisterVat, value: PrintedSiteReadValue(selected.RegisterNumber)},
	} {
		if field.value != "" {
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
