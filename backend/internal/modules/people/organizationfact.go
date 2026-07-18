// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The accepted deep read: a human approval of a
// staged "deepread" proposal lands BOTH halves of the read in one
// transaction — the cold-start profile fields through the same
// fill-empty-plus-evidence machinery every other acceptance uses, and the
// category facts (company contact basics, offerings, market signals) into
// organization_fact, the ratified home for the new closed vocabulary. One
// audit row, one organization.updated event, and the human-precedence
// guard on both stores: a fact a human has since claimed is never
// overwritten by a machine re-accept.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// OrganizationFactFields is the closed category/field vocabulary,
// mirroring the org_fact_field_vocab CHECK so a bad staged payload reads
// as an actionable error, not a constraint 500. The 11 cold-start company
// fields stay in organization_profile_field; this vocabulary is only the
// NEW facts.
var OrganizationFactFields = map[string][]string{
	"company":  {"founded_year", "employee_range", "phone", "contact_email", "location"},
	"offering": {"service", "product"},
	"signal":   {"certification", "partner", "named_customer", "technology"},
}

// OrganizationFactMultiValue names the fields that may carry several rows
// per organization, one per normalized value_key; every other field is
// single-value with value_key ”. Derived, not listed: every offering and
// signal field is multi-value, every company field single-value — except
// location, the one company fact a business states several of (every
// office/site), carved out here and in the DB cardinality CHECK alike.
var OrganizationFactMultiValue = multiValueFactFields()

func multiValueFactFields() map[string]bool {
	multi := map[string]bool{"location": true}
	for _, category := range []string{"offering", "signal"} {
		for _, field := range OrganizationFactFields[category] {
			multi[field] = true
		}
	}
	return multi
}

// factValueSeparator splits a multi-value fact's value into its name and
// short description ("Name — short description") — the spelling the
// extraction prompts demand.
const factValueSeparator = " — "

// NormalizeFactValueKey reduces a multi-value fact's value to its dedupe
// identity: the name before the separator, lowercased with whitespace
// collapsed, so re-reads of the same offering under a reworded
// description converge on one row.
func NormalizeFactValueKey(value string) string {
	name, _, _ := strings.Cut(value, factValueSeparator)
	return strings.Join(strings.Fields(strings.ToLower(name)), " ")
}

// DeepReadField is one staged profile field on the deepread proposal —
// the wire twin of ColdStartFieldInput, shared by the compose worker that
// stages it and the accept effect that decodes it.
type DeepReadField struct {
	Field           string  `json:"field"`
	Value           string  `json:"value"`
	EvidenceSnippet string  `json:"evidence_snippet"`
	SourceURL       string  `json:"source_url"`
	Confidence      float32 `json:"confidence"`
}

// DeepReadFact is one staged category fact.
type DeepReadFact struct {
	Category        string  `json:"category"`
	Field           string  `json:"field"`
	Value           string  `json:"value"`
	ValueKey        string  `json:"value_key"`
	EvidenceSnippet string  `json:"evidence_snippet"`
	SourceURL       string  `json:"source_url"`
	Confidence      float32 `json:"confidence"`
}

// DeepReadProposal is the staged "deepread" payload: both halves of the
// read plus the dossier that produced them. One spelling for the staging
// worker and the accept effect.
type DeepReadProposal struct {
	OrganizationID ids.OrganizationID `json:"organization_id"`
	SourceURL      string             `json:"source_url"`
	SiteReadID     ids.UUID           `json:"site_read_id"`
	Fields         []DeepReadField    `json:"fields"`
	Facts          []DeepReadFact     `json:"facts"`
}

// UnmarshalDeepRead decodes a staged deepread proposal for the accept
// effect.
func UnmarshalDeepRead(raw json.RawMessage) (DeepReadProposal, error) {
	var proposal DeepReadProposal
	if err := json.Unmarshal(raw, &proposal); err != nil {
		return DeepReadProposal{}, fmt.Errorf("people: deepread proposal payload: %w", err)
	}
	return proposal, nil
}

// validDeepReadFact vets one staged fact against the closed vocabulary
// and the row's own CHECKs, so a malformed payload fails with a named
// reason before any write.
func validDeepReadFact(f DeepReadFact) error {
	fields, ok := OrganizationFactFields[f.Category]
	if !ok {
		return fmt.Errorf("people: %q is not an organization-fact category (company|offering|signal)", f.Category)
	}
	known := false
	for _, name := range fields {
		known = known || name == f.Field
	}
	if !known {
		return fmt.Errorf("people: %q is not a %s fact field", f.Field, f.Category)
	}
	if OrganizationFactMultiValue[f.Field] {
		// The key must BE the canonical normalization of the value — a
		// hand-supplied or stale key could bypass the dedupe unique index or
		// collide with an unrelated fact, so it is recomputed and checked,
		// never trusted.
		if want := NormalizeFactValueKey(f.Value); f.ValueKey != want {
			return fmt.Errorf("people: multi-value fact %s value_key %q is not the normalization of its value (want %q)", f.Field, f.ValueKey, want)
		}
	} else if f.ValueKey != "" {
		return fmt.Errorf("people: single-value fact %s carries value_key %q, want ''", f.Field, f.ValueKey)
	}
	if strings.TrimSpace(f.Value) == "" || strings.TrimSpace(f.EvidenceSnippet) == "" {
		return fmt.Errorf("people: fact %s.%s carries an empty value or evidence snippet", f.Category, f.Field)
	}
	if f.Confidence <= 0 || f.Confidence > 1 {
		return fmt.Errorf("people: fact %s.%s confidence %v is outside (0,1]", f.Category, f.Field, f.Confidence)
	}
	return nil
}

// ApplyDeepRead executes an ACCEPTED deepread proposal: the profile-field
// half through the shared fill-empty-plus-evidence machinery (source
// "deepread"), the category facts upserted into organization_fact — both
// under the human-precedence guard — in ONE transaction with one audit
// row and one organization.updated event carrying both deltas.
func (s *Store) ApplyDeepRead(ctx context.Context, in DeepReadProposal) error {
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return err
	}
	if len(in.Fields) == 0 && len(in.Facts) == 0 {
		return errors.New("people: an accepted deepread proposal carries no fields and no facts")
	}
	for _, f := range in.Facts {
		if err := validDeepReadFact(f); err != nil {
			return err
		}
	}

	fields := make([]ColdStartFieldInput, 0, len(in.Fields))
	for _, f := range in.Fields {
		fields = append(fields, ColdStartFieldInput(f))
	}

	return s.tx(ctx, func(tx pgx.Tx) error {
		wsID := workspaceID(ctx)
		// The target is a KNOWN row; row-scope is re-checked here so a
		// leaked org id buys nothing (existence-hiding 404).
		if err := auth.EnsureVisible(ctx, tx, "organization", in.OrganizationID.UUID); err != nil {
			return err
		}
		appliedFields, err := applyEvidenceFields(ctx, tx, wsID, in.OrganizationID, "deepread", by, fields)
		if err != nil {
			return err
		}
		appliedFacts, err := upsertOrganizationFacts(ctx, tx, wsID, in, by)
		if err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "organization", in.OrganizationID.UUID, nil, map[string]any{
			auditKeySource: "deepread", auditKeySourceURL: in.SourceURL,
			auditKeyFields: appliedFields, "facts": appliedFacts,
		})
		if err != nil {
			return fmt.Errorf("audit deepread apply: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "organization.updated", "organization", in.OrganizationID.UUID, map[string]any{
			eventKeyDelta:  map[string]any{auditKeyFields: appliedFields, "facts": appliedFacts},
			auditKeySource: "deepread", auditKeySourceURL: in.SourceURL,
		}); err != nil {
			return fmt.Errorf("emit organization.updated: %w", err)
		}
		return nil
	})
}

// upsertOrganizationFacts lands the category facts, refreshing an
// agent-captured row and never touching one a human has since claimed —
// the same precedence rule organization_profile_field applies. It returns
// the facts actually written (a human-held row upserts zero rows and is
// honestly absent from the delta).
func upsertOrganizationFacts(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, in DeepReadProposal, by string) ([]map[string]any, error) {
	// The dossier link is provenance, not a requirement: a proposal staged
	// without one (or whose dossier was since erased) still lands its facts.
	var siteReadID *ids.UUID
	if !in.SiteReadID.IsZero() {
		siteReadID = &in.SiteReadID
	}
	applied := make([]map[string]any, 0, len(in.Facts))
	for _, f := range in.Facts {
		tag, err := tx.Exec(ctx, `
			INSERT INTO organization_fact
			  (workspace_id, organization_id, category, field, value, value_key,
			   evidence_snippet, source_url, confidence, source, captured_by, site_read_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'deepread', $10, $11)
			ON CONFLICT (workspace_id, organization_id, category, field, value_key)
			DO UPDATE SET value = EXCLUDED.value, evidence_snippet = EXCLUDED.evidence_snippet,
			              source_url = EXCLUDED.source_url, confidence = EXCLUDED.confidence,
			              source = EXCLUDED.source, captured_by = EXCLUDED.captured_by,
			              site_read_id = EXCLUDED.site_read_id, captured_at = now()
			WHERE organization_fact.captured_by NOT LIKE 'human:%'`,
			wsID, in.OrganizationID, f.Category, f.Field, f.Value, f.ValueKey,
			f.EvidenceSnippet, f.SourceURL, f.Confidence, by, siteReadID)
		if err != nil {
			return nil, fmt.Errorf("upsert organization fact %s.%s: %w", f.Category, f.Field, err)
		}
		if tag.RowsAffected() == 1 {
			applied = append(applied, map[string]any{"category": f.Category, "field": f.Field, "value": f.Value})
		}
	}
	return applied, nil
}
