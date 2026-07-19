// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	siteReadValueProfile = "profile_field"
	siteReadValueFact    = "fact"

	siteReadComparisonNew           = "new"
	siteReadComparisonMachineChange = "machine_change"
	siteReadComparisonHumanConflict = "human_conflict"
	siteReadComparisonUnchanged     = "unchanged"

	siteReadResolutionKeep   = "keep_current"
	siteReadResolutionAccept = "accept_proposal"
	siteReadResolutionUse    = "use_value"
)

// SiteReadComparison is one version-bound proposal/current-value comparison.
type SiteReadComparison struct {
	Key            string
	ValueKind      string
	Classification string
	CurrentValue   *string
	CurrentSource  *string
	ProposedValue  string
}

// SiteReadResolution is the human's explicit decision for one conflict.
type SiteReadResolution struct {
	Key    string
	Action string
	Value  *string
}

// InvalidSiteReadResolutionError identifies request-shape errors that the
// transport maps to a field-level 422 without exposing store internals.
type InvalidSiteReadResolutionError struct {
	Reason string
}

func (e *InvalidSiteReadResolutionError) Error() string { return e.Reason }

// GetCompanySiteRead returns the operational dossier plus its comparison to
// the currently confirmed anchor in one workspace transaction.
func (s *Store) GetCompanySiteRead(ctx context.Context, readID ids.UUID) (SiteRead, []SiteReadComparison, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		if createErr := auth.Require(ctx, "organization", principal.ActionCreate); createErr != nil {
			return SiteRead{}, nil, createErr
		}
	}
	var read SiteRead
	var comparisons []SiteReadComparison
	err := s.tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT `+siteReadColumns+` FROM site_read
			WHERE id = $1 AND target_kind = 'onboarding'`, readID)
		var err error
		read, err = scanSiteRead(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get company site read: %w", err)
		}
		company, err := readAnchorForComparison(ctx, tx)
		if err != nil {
			return err
		}
		comparisons = compareCompanySiteRead(read, company)
		return nil
	})
	return read, comparisons, err
}

//nolint:nilnil // a missing anchor is a valid pre-onboarding comparison state
func readAnchorForComparison(ctx context.Context, tx pgx.Tx) (*Company, error) {
	orgID, err := anchorOrganization(ctx, tx, false)
	if errors.Is(err, apperrors.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
		return nil, err
	}
	company, err := readCompany(ctx, tx, orgID)
	if err != nil {
		return nil, err
	}
	return &company, nil
}

func compareCompanySiteRead(read SiteRead, company *Company) []SiteReadComparison {
	currentFields := map[string]CompanyProfileField{}
	currentFacts := map[string]CompanyFact{}
	if company != nil {
		for _, field := range company.ProfileFields {
			currentFields[field.Field] = field
		}
		if _, found := currentFields[fieldDisplayName]; !found {
			currentFields[fieldDisplayName] = CompanyProfileField{
				Field: fieldDisplayName, Value: company.DisplayName,
				Source: normalizeCompanySource(company.OrganizationSource),
			}
		}
		for _, fact := range company.Facts {
			currentFacts[companyFactKey(fact)] = fact
		}
	}

	out := make([]SiteReadComparison, 0, len(read.ProfileFields)+len(read.Facts))
	for _, proposal := range read.ProfileFields {
		current, found := currentFields[proposal.Field]
		out = append(out, classifySiteReadValue(proposal.Field, siteReadValueProfile,
			proposal.Value, current.Value, current.Source, found))
	}
	for _, proposal := range read.Facts {
		key := SiteReadFactKey(proposal)
		current, found := currentFacts[key]
		out = append(out, classifySiteReadValue(key, siteReadValueFact,
			proposal.Value, current.Value, current.Source, found))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ValueKind != out[j].ValueKind {
			return out[i].ValueKind < out[j].ValueKind
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func classifySiteReadValue(key, kind, proposed, current, source string, found bool) SiteReadComparison {
	comparison := SiteReadComparison{Key: key, ValueKind: kind, ProposedValue: proposed}
	if !found {
		comparison.Classification = siteReadComparisonNew
		return comparison
	}
	comparison.CurrentValue = &current
	comparison.CurrentSource = &source
	switch {
	case strings.TrimSpace(current) == strings.TrimSpace(proposed):
		comparison.Classification = siteReadComparisonUnchanged
	case source == companySourceHuman:
		comparison.Classification = siteReadComparisonHumanConflict
	default:
		comparison.Classification = siteReadComparisonMachineChange
	}
	return comparison
}

func companyFactKey(fact CompanyFact) string {
	return fact.Category + "/" + fact.Field + "/" + fact.ValueKey
}
