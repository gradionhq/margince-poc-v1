// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The installation's OWN company — the anchor organization (organization
// .is_anchor, 0083). It is an organization row like any other; the mark is what
// makes it findable, so "has this installation described itself yet?" is a
// question the database can answer instead of a guess derived from a hostname.
// At most one live anchor per workspace, enforced by uq_organization_anchor.
//
// This is the human's write. Unlike the cold-start read-back it resolves no
// domain, creates no approval and fills no blanks on its own: a human looked
// at every value in a form and saved it, so every field lands stamped
// human:<user id> / source=human — which is exactly what makes a later agent
// read-back leave it alone (applyEvidenceFields refuses to overwrite a
// human-captured row).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The profile-field vocabulary — the contract's ColdStartField enum, spelled
// once. A read-back fills these and the company form types them; they are the
// same set on purpose, which is what lets a site pre-fill a form.
const (
	fieldOfferSummary      = "offer_summary"
	fieldLegalName         = "legal_name"
	fieldRegisteredAddress = "registered_address"
	fieldRegisterVat       = "register_vat"
	fieldIndustry          = "industry"
	fieldICP               = "icp"
	fieldValueProposition  = "value_proposition"
	fieldUSP               = "usp"
	fieldCustomerPains     = "customer_pains"
	fieldDesiredOutcomes   = "desired_outcomes"
	fieldBuyingCenter      = "buying_center"
	fieldBuyingIntents     = "buying_intents"
	fieldCommonObjections  = "common_objections"
	fieldSalesMotion       = "sales_motion"
	fieldHistory           = "history"
)

const (
	companySourceHuman    = "human"
	companySourceSiteRead = "site_read"
)

const (
	actionUpdate      = "update"
	auditKeyFields    = "fields"
	auditKeySource    = "source"
	auditKeySourceURL = "source_url"
	eventKeyDelta     = "delta"
)

// companyField is one field of the company form: its name, and — when the
// field also lives on an organization column — the statement that writes it
// there. The column is the canonical value; the profile-field row carries the
// provenance either way, exactly as the read-back writes it. The column is
// never a bind parameter: the statement is fixed here, only values bind.
type companyField struct {
	name   string
	update string
}

// companyFields is the form's vocabulary — the contract's ColdStartField enum,
// deliberately the same set a read-back can fill. Ordered so an audit delta
// reads the way the form does.
var companyFields = []companyField{
	{name: fieldDisplayName},
	{name: fieldOfferSummary},
	{name: fieldLegalName, update: `UPDATE organization SET legal_name = $2 WHERE id = $1`},
	{name: fieldRegisteredAddress, update: `UPDATE organization SET address_line1 = $2 WHERE id = $1`},
	{name: fieldRegisterVat},
	{name: fieldIndustry, update: `UPDATE organization SET industry = $2 WHERE id = $1`},
	{name: fieldICP},
	{name: fieldValueProposition},
	{name: fieldUSP},
	{name: fieldCustomerPains},
	{name: fieldDesiredOutcomes},
	{name: fieldBuyingCenter},
	{name: fieldBuyingIntents},
	{name: fieldCommonObjections},
	{name: fieldSalesMotion},
	{name: fieldHistory},
}

// CompanyProfileField is one confirmed single-value company statement with
// its field-level provenance. Empty evidence/source URLs mean the value was
// supplied by a human rather than read from a source document.
type CompanyProfileField struct {
	Field           string
	Value           string
	EvidenceSnippet string
	SourceURL       string
	Confidence      float32
	Source          string
	CapturedBy      string
	UpdatedAt       time.Time
}

// CompanyFact is one accepted repeatable fact about the company.
type CompanyFact struct {
	Category        string
	Field           string
	Value           string
	ValueKey        string
	EvidenceSnippet string
	SourceURL       string
	Confidence      float32
	Source          string
	CapturedBy      string
	UpdatedAt       time.Time
}

// Company is the installation's own company as the form reads and writes it.
// Fields carries the companyFields vocabulary; an absent key is a field
// nobody has filled yet.
type Company struct {
	OrganizationID         ids.OrganizationID
	DisplayName            string
	OrganizationSource     string
	OrganizationCapturedBy string
	Website                *string
	Fields                 map[string]string
	ProfileFields          []CompanyProfileField
	Facts                  []CompanyFact
	MinimumComplete        bool
	UpdatedAt              time.Time
}

// SaveCompanyInput is one submission of the company form. A nil field was not
// sent and keeps whatever it held; a field sent empty is cleared. DisplayName
// is required — the form cannot save a nameless company.
type SaveCompanyInput struct {
	DisplayName string
	Website     *string
	Fields      map[string]*string
}

// GetCompany reads the anchor organization. It returns ErrNotFound when the
// installation has not described itself yet — that 404 IS the onboarding
// signal, and it is deliberately indistinguishable from "no such record" to a
// caller who may not see it.
func (s *Store) GetCompany(ctx context.Context) (Company, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return Company{}, err
	}
	var out Company
	err := s.tx(ctx, func(tx pgx.Tx) error {
		orgID, err := anchorOrganization(ctx, tx, false)
		if err != nil {
			return err
		}
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
			return err
		}
		out, err = readCompany(ctx, tx, orgID)
		return err
	})
	if err != nil {
		return Company{}, err
	}
	return out, nil
}

// SaveCompany creates the anchor organization on first save and updates it on
// every later one, in one transaction with its audit row and its event. The
// transport validates the submission's shape; only names in the companyFields
// vocabulary are ever written.
func (s *Store) SaveCompany(ctx context.Context, in SaveCompanyInput) (Company, error) {
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return Company{}, err
	}

	var out Company
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockCompanyState(ctx, tx); err != nil {
			return err
		}
		orgID, created, err := resolveOrCreateAnchor(ctx, tx, in.DisplayName, by)
		if err != nil {
			return err
		}

		fields := make(map[string]*string, len(in.Fields)+1)
		for field, value := range in.Fields {
			fields[field] = value
		}
		fields[fieldDisplayName] = &in.DisplayName
		applied, err := writeCompanyFields(ctx, tx, orgID, by, fields)
		if err != nil {
			return err
		}
		if in.Website != nil {
			if err := setCompanyDomain(ctx, tx, orgID, *in.Website, by); err != nil {
				return err
			}
			applied["website"] = *in.Website
		}
		applied["display_name"] = in.DisplayName

		action := actionUpdate
		if created {
			action = "create"
		}
		auditID, err := storekit.Audit(ctx, tx, action, "organization", orgID.UUID, nil, map[string]any{
			auditKeySource: companySourceHuman, "anchor": true, auditKeyFields: applied,
		})
		if err != nil {
			return fmt.Errorf("audit company save: %w", err)
		}
		payload := companySaveEventPayload(created, applied, by)
		if err := storekit.EmitEvent(ctx, tx, auditID, orgID.UUID, payload); err != nil {
			return fmt.Errorf("emit %s: %w", payload.EventType(), err)
		}

		out, err = readCompany(ctx, tx, orgID)
		return err
	})
	if err != nil {
		return Company{}, err
	}
	return out, nil
}

// companySaveEventPayload builds the organization-side event SaveCompany
// emits — organization.created (the union struct) on the anchor's first
// save, or an organization.updated changed_fields note on every later
// one — the ONE place that maps the applied field delta onto the
// published schema. The two shapes are different published events, not
// variants of one, so the return type is the shared events.Payload seam.
//
//nolint:ireturn // dispatches to PublicEventOrganizationCreated vs Updated by the created condition; tested directly via the interface in person_organization_payload_test.go
func companySaveEventPayload(created bool, applied map[string]any, by string) events.Payload {
	if created {
		source := companySourceHuman
		anchor := true
		return crmcontracts.PublicEventOrganizationCreated{
			Delta:      &applied,
			Source:     &source,
			Anchor:     &anchor,
			CapturedBy: &by,
		}
	}
	return crmcontracts.PublicEventOrganizationUpdated{
		ChangedFields: map[string]any{
			eventKeyDelta: applied, auditKeySource: companySourceHuman, "anchor": true, "captured_by": by,
		},
	}
}

// anchorOrganization resolves the workspace's own organization, or ErrNotFound
// when it has none yet. lock takes the row for the rest of the transaction:
// the save path serializes concurrent edits on it, a plain read does not.
func anchorOrganization(ctx context.Context, tx pgx.Tx, lock bool) (ids.OrganizationID, error) {
	query := `SELECT id FROM organization
	           WHERE workspace_id = $1 AND is_anchor AND archived_at IS NULL`
	if lock {
		query += ` FOR UPDATE`
	}
	var orgID ids.OrganizationID
	err := tx.QueryRow(ctx, query, workspaceID(ctx)).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.OrganizationID{}, apperrors.ErrNotFound
	}
	if err != nil {
		return ids.OrganizationID{}, fmt.Errorf("resolve anchor organization: %w", err)
	}
	return orgID, nil
}

// resolveOrCreateAnchor returns the workspace's own company, minting it on the
// first save, and reports whether it created it — which decides the audit
// action and the event the caller emits. Creating and updating carry different
// authority, so each arm gates on its own.
func resolveOrCreateAnchor(ctx context.Context, tx pgx.Tx, displayName, by string) (ids.OrganizationID, bool, error) {
	// The company is a single standing record, not an optimistically
	// concurrent one: the form carries no version, so the row is LOCKED for
	// the rest of the transaction instead. Two admins saving at once serialize
	// — the second writes on top of the first rather than silently losing it.
	orgID, err := anchorOrganization(ctx, tx, true)
	if errors.Is(err, apperrors.ErrNotFound) {
		if err := auth.Require(ctx, "organization", principal.ActionCreate); err != nil {
			return ids.OrganizationID{}, false, err
		}
		orgID, err = createAnchorOrganization(ctx, tx, displayName, by)
		return orgID, true, err
	}
	if err != nil {
		return ids.OrganizationID{}, false, err
	}
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return ids.OrganizationID{}, false, err
	}
	if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
		return ids.OrganizationID{}, false, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE organization SET display_name = $2, version = version + 1
		 WHERE id = $1 AND display_name IS DISTINCT FROM $2`,
		orgID, displayName); err != nil {
		return ids.OrganizationID{}, false, fmt.Errorf("update company name: %w", err)
	}
	return orgID, false, nil
}

// createAnchorOrganization mints the company row, marked as the installation's
// own. Nothing serializes two FIRST saves — neither has a row to lock — so the
// uq_organization_anchor index is what decides: the loser is told the company
// already exists rather than quietly minting a rival one.
func createAnchorOrganization(ctx context.Context, tx pgx.Tx, displayName, by string) (ids.OrganizationID, error) {
	orgID := ids.New[ids.OrganizationKind]()
	_, err := tx.Exec(ctx,
		`INSERT INTO organization (id, workspace_id, display_name, is_anchor, source, captured_by)
		 VALUES ($1, $2, $3, true, 'manual', $4)`,
		orgID, workspaceID(ctx), displayName, by)
	if constraint, dup := storekit.UniqueViolation(err); dup && constraint == "uq_organization_anchor" {
		return ids.OrganizationID{}, fmt.Errorf("the company was created by someone else just now: %w", apperrors.ErrConflict)
	}
	if err != nil {
		return ids.OrganizationID{}, fmt.Errorf("insert company: %w", err)
	}
	return orgID, nil
}

// writeCompanyFields applies the submitted fields: the column-backed ones onto
// their column (a human's own form overwrites — unlike a read-back, which only
// fills blanks), and every one onto its provenance row. Returns what changed,
// for the audit delta.
func writeCompanyFields(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, by string, fields map[string]*string) (map[string]any, error) {
	applied := map[string]any{}
	for _, spec := range companyFields {
		field := spec.name
		value, sent := fields[field]
		if !sent || value == nil {
			continue
		}
		trimmed := strings.TrimSpace(*value)
		if spec.update != "" {
			if err := setCompanyColumn(ctx, tx, orgID, spec, trimmed); err != nil {
				return nil, err
			}
		}
		if trimmed == "" {
			if _, err := tx.Exec(ctx,
				`DELETE FROM organization_profile_field
				 WHERE workspace_id = $1 AND organization_id = $2 AND field = $3`,
				workspaceID(ctx), orgID, field); err != nil {
				return nil, fmt.Errorf("clear company field %s: %w", field, err)
			}
			applied[field] = nil
			continue
		}
		// A human-typed value has no snippet to quote — the human IS the
		// evidence, which is what source=human + captured_by=human:<id>
		// record. Confidence is 1: they are not guessing about themselves.
		if _, err := tx.Exec(ctx, `
			INSERT INTO organization_profile_field
			  (workspace_id, organization_id, field, value, evidence_snippet, source_url, confidence, source, captured_by)
			VALUES ($1, $2, $3, $4, '', '', 1, 'human', $5)
			ON CONFLICT (workspace_id, organization_id, field)
			DO UPDATE SET value = EXCLUDED.value, evidence_snippet = '', source_url = '',
			              confidence = 1, source = 'human',
			              captured_by = EXCLUDED.captured_by, captured_at = now()`,
			workspaceID(ctx), orgID, field, trimmed, by); err != nil {
			return nil, fmt.Errorf("save company field %s: %w", field, err)
		}
		applied[field] = trimmed
	}
	return applied, nil
}

// setCompanyColumn writes a column-backed field, clearing it to NULL rather
// than storing an empty string — an unfilled field reads as absent, never as
// the empty answer.
func setCompanyColumn(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, spec companyField, value string) error {
	var stored *string
	if value != "" {
		stored = &value
	}
	if _, err := tx.Exec(ctx, spec.update, orgID, stored); err != nil {
		return fmt.Errorf("set %s: %w", spec.name, err)
	}
	return nil
}

// readCompany assembles the form's view: the name and website from the
// organization, every profile field from its provenance row.
func readCompany(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (Company, error) {
	out := Company{OrganizationID: orgID, Fields: map[string]string{}}
	if err := tx.QueryRow(ctx,
		`SELECT o.display_name, o.source, o.captured_by, o.updated_at, d.domain
		   FROM organization o
		   LEFT JOIN organization_domain d
		     ON d.organization_id = o.id AND d.is_primary AND d.archived_at IS NULL
		  WHERE o.id = $1`,
		orgID).Scan(&out.DisplayName, &out.OrganizationSource, &out.OrganizationCapturedBy,
		&out.UpdatedAt, &out.Website); err != nil {
		return Company{}, fmt.Errorf("read company: %w", err)
	}

	rows, err := tx.Query(ctx,
		`SELECT field, value, evidence_snippet, source_url, confidence,
		        source, captured_by, updated_at
		   FROM organization_profile_field
		  WHERE workspace_id = $1 AND organization_id = $2
		  ORDER BY field`,
		workspaceID(ctx), orgID)
	if err != nil {
		return Company{}, fmt.Errorf("read company fields: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var field CompanyProfileField
		if err := rows.Scan(&field.Field, &field.Value, &field.EvidenceSnippet,
			&field.SourceURL, &field.Confidence, &field.Source, &field.CapturedBy,
			&field.UpdatedAt); err != nil {
			return Company{}, fmt.Errorf("scan company field: %w", err)
		}
		out.Fields[field.Field] = field.Value
		out.ProfileFields = append(out.ProfileFields, field)
	}
	if err := rows.Err(); err != nil {
		return Company{}, fmt.Errorf("read company fields: %w", err)
	}

	facts, err := tx.Query(ctx,
		`SELECT category, field, value, value_key, evidence_snippet, source_url,
		        confidence, source, captured_by, updated_at
		   FROM organization_fact
		  WHERE workspace_id = $1 AND organization_id = $2
		  ORDER BY category, field, value_key, value`,
		workspaceID(ctx), orgID)
	if err != nil {
		return Company{}, fmt.Errorf("read company facts: %w", err)
	}
	defer facts.Close()
	for facts.Next() {
		var fact CompanyFact
		if err := facts.Scan(&fact.Category, &fact.Field, &fact.Value, &fact.ValueKey,
			&fact.EvidenceSnippet, &fact.SourceURL, &fact.Confidence, &fact.Source,
			&fact.CapturedBy, &fact.UpdatedAt); err != nil {
			return Company{}, fmt.Errorf("scan company fact: %w", err)
		}
		out.Facts = append(out.Facts, fact)
	}
	if err := facts.Err(); err != nil {
		return Company{}, fmt.Errorf("read company facts: %w", err)
	}
	out.MinimumComplete = strings.TrimSpace(out.DisplayName) != "" &&
		strings.TrimSpace(out.Fields[fieldOfferSummary]) != "" &&
		strings.TrimSpace(out.Fields[fieldICP]) != ""
	return out, nil
}
