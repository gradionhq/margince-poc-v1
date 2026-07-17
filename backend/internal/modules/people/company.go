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
// human:<user id> / source=manual — which is exactly what makes a later agent
// read-back leave it alone (applyEvidenceFields refuses to overwrite a
// human-captured row).

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The profile-field vocabulary — the contract's ColdStartField enum, spelled
// once. A read-back fills these and the company form types them; they are the
// same set on purpose, which is what lets a site pre-fill a form.
const (
	fieldLegalName         = "legal_name"
	fieldRegisteredAddress = "registered_address"
	fieldRegisterVat       = "register_vat"
	fieldIndustry          = "industry"
	fieldICP               = "icp"
	fieldValueProposition  = "value_proposition"
	fieldUSP               = "usp"
	fieldBuyingCenter      = "buying_center"
	fieldBuyingIntents     = "buying_intents"
	fieldHistory           = "history"
)

// The audit row's and the event envelope's payload keys.
const (
	auditKeyFields = "fields"
	auditKeySource = "source"
	eventKeyDelta  = "delta"
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
	{name: fieldLegalName, update: `UPDATE organization SET legal_name = $2 WHERE id = $1`},
	{name: fieldRegisteredAddress, update: `UPDATE organization SET address_line1 = $2 WHERE id = $1`},
	{name: fieldRegisterVat},
	{name: fieldIndustry, update: `UPDATE organization SET industry = $2 WHERE id = $1`},
	{name: fieldICP},
	{name: fieldValueProposition},
	{name: fieldUSP},
	{name: fieldBuyingCenter},
	{name: fieldBuyingIntents},
	{name: fieldHistory},
}

// Company is the installation's own company as the form reads and writes it.
// Fields carries the companyFields vocabulary; an absent key is a field
// nobody has filled yet.
type Company struct {
	OrganizationID ids.OrganizationID
	DisplayName    string
	Website        *string
	Fields         map[string]string
	UpdatedAt      time.Time
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
		orgID, created, err := resolveOrCreateAnchor(ctx, tx, in.DisplayName, by)
		if err != nil {
			return err
		}

		applied, err := writeCompanyFields(ctx, tx, orgID, by, in.Fields)
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

		action, eventType := "update", "organization.updated"
		if created {
			action, eventType = "create", "organization.created"
		}
		auditID, err := storekit.Audit(ctx, tx, action, "organization", orgID.UUID, nil, map[string]any{
			auditKeySource: "manual", "anchor": true, auditKeyFields: applied,
		})
		if err != nil {
			return fmt.Errorf("audit company save: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, eventType, "organization", orgID.UUID, map[string]any{
			eventKeyDelta: applied, auditKeySource: "manual", "anchor": true, "captured_by": by,
		}); err != nil {
			return fmt.Errorf("emit %s: %w", eventType, err)
		}

		out, err = readCompany(ctx, tx, orgID)
		return err
	})
	if err != nil {
		return Company{}, err
	}
	return out, nil
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
		`UPDATE organization SET display_name = $2, version = version + 1 WHERE id = $1`,
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
		// evidence, which is what source=manual + captured_by=human:<id>
		// record. Confidence is 1: they are not guessing about themselves.
		if _, err := tx.Exec(ctx, `
			INSERT INTO organization_profile_field
			  (workspace_id, organization_id, field, value, evidence_snippet, source_url, confidence, source, captured_by)
			VALUES ($1, $2, $3, $4, '', '', 1, 'manual', $5)
			ON CONFLICT (workspace_id, organization_id, field)
			DO UPDATE SET value = EXCLUDED.value, evidence_snippet = '', source_url = '',
			              confidence = 1, source = 'manual',
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

// setCompanyDomain records the company's own website as its primary domain —
// the same handle the cold-start read-back resolves organizations by, so a
// company saved by hand is findable exactly like one read from a site.
func setCompanyDomain(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, website, by string) error {
	host, err := companyHost(website)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO organization_domain (workspace_id, organization_id, domain, is_primary, source, captured_by)
		 VALUES ($1, $2, lower($3), true, 'manual', $4)
		 ON CONFLICT DO NOTHING`,
		workspaceID(ctx), orgID, host, by); err != nil {
		return fmt.Errorf("set company domain: %w", err)
	}
	return nil
}

// companyHost reduces a website to the bare domain the organization_domain
// index keys on, accepting what a human actually types: "acme.com" as readily
// as "https://www.acme.com/about". The transport rejects an unparseable
// website before it gets here; this guard keeps a malformed one out of the
// domain index rather than storing it.
func companyHost(website string) (string, error) {
	if !strings.Contains(website, "://") {
		website = "https://" + website
	}
	parsed, err := url.Parse(website)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("people: company website %q has no host", website)
	}
	return strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www."), nil
}

// readCompany assembles the form's view: the name and website from the
// organization, every profile field from its provenance row.
func readCompany(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (Company, error) {
	out := Company{OrganizationID: orgID, Fields: map[string]string{}}
	if err := tx.QueryRow(ctx,
		`SELECT o.display_name, o.updated_at, d.domain
		   FROM organization o
		   LEFT JOIN organization_domain d
		     ON d.organization_id = o.id AND d.is_primary AND d.archived_at IS NULL
		  WHERE o.id = $1`,
		orgID).Scan(&out.DisplayName, &out.UpdatedAt, &out.Website); err != nil {
		return Company{}, fmt.Errorf("read company: %w", err)
	}

	rows, err := tx.Query(ctx,
		`SELECT field, value FROM organization_profile_field
		  WHERE workspace_id = $1 AND organization_id = $2`,
		workspaceID(ctx), orgID)
	if err != nil {
		return Company{}, fmt.Errorf("read company fields: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var field, value string
		if err := rows.Scan(&field, &value); err != nil {
			return Company{}, fmt.Errorf("scan company field: %w", err)
		}
		out.Fields[field] = value
	}
	if err := rows.Err(); err != nil {
		return Company{}, fmt.Errorf("read company fields: %w", err)
	}
	return out, nil
}
