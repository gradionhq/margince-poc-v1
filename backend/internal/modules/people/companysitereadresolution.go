// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type resolvedHumanFact struct {
	proposal DeepReadFact
	value    string
}

func resolveSiteReadConflicts(
	read SiteRead,
	company *Company,
	in ConfirmCompanySiteReadInput,
) (ConfirmCompanySiteReadInput, error) {
	conflicts := map[string]SiteReadComparison{}
	for _, comparison := range compareCompanySiteRead(read, company) {
		if comparison.Classification == siteReadComparisonHumanConflict {
			conflicts[comparison.Key] = comparison
		}
	}
	resolutions := make(map[string]SiteReadResolution, len(in.Resolutions))
	for _, resolution := range in.Resolutions {
		if _, duplicate := resolutions[resolution.Key]; duplicate {
			return in, invalidResolution("resolution key " + resolution.Key + " appears more than once")
		}
		if _, expected := conflicts[resolution.Key]; !expected {
			return in, invalidResolution("resolution key " + resolution.Key + " is not a current human conflict")
		}
		resolutions[resolution.Key] = resolution
	}
	for key := range conflicts {
		if _, found := resolutions[key]; !found {
			return in, invalidResolution("human conflict " + key + " needs an explicit resolution")
		}
	}

	in.skipProfileFields = map[string]bool{}
	in.overwriteProfileFields = map[string]bool{}
	in.overwriteFactKeys = map[string]bool{}
	facts := siteReadFactsByKey(read.Facts)
	orderedKeys := make([]string, 0, len(resolutions))
	for key := range resolutions {
		orderedKeys = append(orderedKeys, key)
	}
	sort.Strings(orderedKeys)
	for _, key := range orderedKeys {
		resolution := resolutions[key]
		comparison := conflicts[key]
		if err := validateResolutionValue(resolution); err != nil {
			return in, err
		}
		if comparison.ValueKind == siteReadValueProfile {
			applyProfileResolution(&in, comparison, resolution)
			continue
		}
		proposal, found := facts[key]
		if !found {
			return in, invalidResolution("fact resolution " + key + " no longer has a proposal")
		}
		applyFactResolution(&in, proposal, resolution)
	}
	return in, nil
}

func validateResolutionValue(resolution SiteReadResolution) error {
	switch resolution.Action {
	case siteReadResolutionKeep, siteReadResolutionAccept:
		if resolution.Value != nil {
			return invalidResolution("resolution " + resolution.Key + " supplies a value for " + resolution.Action)
		}
	case siteReadResolutionUse:
		if resolution.Value == nil || strings.TrimSpace(*resolution.Value) == "" {
			return invalidResolution("resolution " + resolution.Key + " needs a non-blank value")
		}
	default:
		return invalidResolution("resolution " + resolution.Key + " has an unknown action")
	}
	return nil
}

func applyProfileResolution(
	in *ConfirmCompanySiteReadInput,
	comparison SiteReadComparison,
	resolution SiteReadResolution,
) {
	var value string
	switch resolution.Action {
	case siteReadResolutionKeep:
		value = *comparison.CurrentValue
		in.skipProfileFields[comparison.Key] = true
	case siteReadResolutionAccept:
		value = comparison.ProposedValue
		in.overwriteProfileFields[comparison.Key] = true
	case siteReadResolutionUse:
		value = strings.TrimSpace(*resolution.Value)
	}
	if comparison.Key == fieldDisplayName {
		in.DisplayName = value
		return
	}
	if resolution.Action == siteReadResolutionKeep {
		delete(in.Fields, comparison.Key)
		return
	}
	in.Fields[comparison.Key] = &value
}

func applyFactResolution(in *ConfirmCompanySiteReadInput, proposal DeepReadFact, resolution SiteReadResolution) {
	key := SiteReadFactKey(proposal)
	in.SelectedFactKeys = removeFactKey(in.SelectedFactKeys, key)
	switch resolution.Action {
	case siteReadResolutionAccept:
		in.SelectedFactKeys = append(in.SelectedFactKeys, key)
		in.overwriteFactKeys[key] = true
	case siteReadResolutionUse:
		in.humanFactEdits = append(in.humanFactEdits, resolvedHumanFact{
			proposal: proposal,
			value:    strings.TrimSpace(*resolution.Value),
		})
	}
}

func siteReadFactsByKey(facts []DeepReadFact) map[string]DeepReadFact {
	byKey := make(map[string]DeepReadFact, len(facts))
	for _, fact := range facts {
		byKey[SiteReadFactKey(fact)] = fact
	}
	return byKey
}

func removeFactKey(keys []string, unwanted string) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if key != unwanted {
			out = append(out, key)
		}
	}
	return out
}

func applyResolvedHumanFacts(
	ctx context.Context,
	tx pgx.Tx,
	orgID ids.OrganizationID,
	by string,
	edits []resolvedHumanFact,
) ([]map[string]any, error) {
	applied := make([]map[string]any, 0, len(edits))
	for _, edit := range edits {
		oldKey := edit.proposal.ValueKey
		newKey := oldKey
		if OrganizationFactMultiValue[edit.proposal.Field] {
			newKey = NormalizeFactValueKey(edit.value)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM organization_fact
			WHERE workspace_id = $1 AND organization_id = $2 AND category = $3
			  AND field = $4 AND value_key = $5`, workspaceID(ctx), orgID,
			edit.proposal.Category, edit.proposal.Field, oldKey); err != nil {
			return nil, fmt.Errorf("replace human organization fact %s.%s: %w",
				edit.proposal.Category, edit.proposal.Field, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO organization_fact
			(workspace_id, organization_id, category, field, value, value_key,
			 evidence_snippet, source_url, confidence, source, captured_by, site_read_id)
			VALUES ($1, $2, $3, $4, $5, $6, '', '', 1, 'human', $7, NULL)
			ON CONFLICT (workspace_id, organization_id, category, field, value_key)
			DO UPDATE SET value = EXCLUDED.value, evidence_snippet = '', source_url = '',
			 confidence = 1, source = 'human', captured_by = EXCLUDED.captured_by,
			 site_read_id = NULL, captured_at = now()`, workspaceID(ctx), orgID,
			edit.proposal.Category, edit.proposal.Field, edit.value, newKey, by); err != nil {
			return nil, fmt.Errorf("save human organization fact %s.%s: %w",
				edit.proposal.Category, edit.proposal.Field, err)
		}
		applied = append(applied, map[string]any{
			"category":     edit.proposal.Category,
			"field":        edit.proposal.Field,
			"value":        edit.value,
			auditKeySource: companySourceHuman,
		})
	}
	return applied, nil
}

func invalidResolution(reason string) error {
	return &InvalidSiteReadResolutionError{Reason: reason}
}
