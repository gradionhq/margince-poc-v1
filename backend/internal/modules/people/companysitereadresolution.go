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

// siteReadResolutionTarget is a comparison a keyed resolution may address, and
// the terms it may be addressed on.
type siteReadResolutionTarget struct {
	comparison SiteReadComparison
	conflict   bool
}

// siteReadResolutionTargets is what a confirmation is allowed to decide.
//
// Every human-held collision is here and must be answered — that is the refresh
// rule. Every PROPOSED FACT is here too, on use_value alone, because the
// selection list can only take a fact or drop it: a reader who sees the read got
// a fact wrong has no other way to say what is true, and dropping it loses the
// fact entirely. A profile field needs no such entry — the submitted profile IS
// its correction channel, and splitConfirmedProfile already separates an edited
// value from an accepted one.
func siteReadResolutionTargets(read SiteRead, company *Company) map[string]siteReadResolutionTarget {
	targets := map[string]siteReadResolutionTarget{}
	for _, comparison := range compareCompanySiteRead(read, company) {
		conflict := comparison.Classification == siteReadComparisonHumanConflict
		if !conflict && comparison.ValueKind != siteReadValueFact {
			continue
		}
		targets[comparison.Key] = siteReadResolutionTarget{comparison: comparison, conflict: conflict}
	}
	return targets
}

func collectSiteReadResolutions(
	targets map[string]siteReadResolutionTarget,
	submitted []SiteReadResolution,
) (map[string]SiteReadResolution, error) {
	resolutions := make(map[string]SiteReadResolution, len(submitted))
	for _, resolution := range submitted {
		if _, duplicate := resolutions[resolution.Key]; duplicate {
			return nil, invalidResolution("resolution key " + resolution.Key + " appears more than once")
		}
		target, addressable := targets[resolution.Key]
		if !addressable {
			return nil, invalidResolution("resolution key " + resolution.Key +
				" is neither a current human conflict nor a fact this draft proposes")
		}
		if !target.conflict && resolution.Action != siteReadResolutionUse {
			return nil, invalidResolution("resolution key " + resolution.Key +
				" holds no human value to keep or overwrite; list it in selected_fact_keys to accept it, or send use_value to correct it")
		}
		if err := validateResolutionValue(resolution); err != nil {
			return nil, err
		}
		resolutions[resolution.Key] = resolution
	}
	for key, target := range targets {
		if _, answered := resolutions[key]; !answered && target.conflict {
			return nil, invalidResolution("human conflict " + key + " needs an explicit resolution")
		}
	}
	return resolutions, nil
}

func resolveSiteReadConflicts(
	read SiteRead,
	company *Company,
	in ConfirmCompanySiteReadInput,
) (ConfirmCompanySiteReadInput, error) {
	targets := siteReadResolutionTargets(read, company)
	resolutions, err := collectSiteReadResolutions(targets, in.Resolutions)
	if err != nil {
		return in, err
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
		comparison := targets[key].comparison
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
		acceptSiteReadFact(in, key)
	case siteReadResolutionUse:
		value := strings.TrimSpace(*resolution.Value)
		// A submitted value the page already states is an acceptance, not an
		// authored claim, so it keeps that page's evidence instead of being
		// rewritten as an evidence-less human assertion (ADR-0065). It is the
		// same rule splitConfirmedProfile applies to a profile field.
		if value == strings.TrimSpace(proposal.Value) {
			acceptSiteReadFact(in, key)
			return
		}
		in.humanFactEdits = append(in.humanFactEdits, resolvedHumanFact{proposal: proposal, value: value})
	}
}

func acceptSiteReadFact(in *ConfirmCompanySiteReadInput, key string) {
	in.SelectedFactKeys = append(in.SelectedFactKeys, key)
	// The accepted value has to land even when a human row holds the slot:
	// upsertOrganizationFacts never writes over one, so taking the website's
	// value means clearing what the human previously asserted there.
	in.overwriteFactKeys[key] = true
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
