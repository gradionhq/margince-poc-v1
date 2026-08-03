// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The organization half of the §1.3 merge (merge.go documents the
// shared collision-aware relink rules): beyond the shared machinery it
// re-homes the org hierarchy, deal/partner attributions, and the 1:1
// partner extension.

package people

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// MergeOrganization merges organization source→target and returns the
// survivor. The org half additionally re-homes the hierarchy (A's
// children become B's) and the deal/partner attributions.
func (s *Store) MergeOrganization(ctx context.Context, sourceID, targetID ids.OrganizationID) (crmcontracts.Organization, error) {
	// Same rule, same reason as MergePerson: an omitted target_id is not caught
	// by the self-merge check and would answer not-found for a survivor the
	// caller never named.
	if err := httperr.RequireBodyID(targetIDField, targetID.UUID); err != nil {
		return crmcontracts.Organization{}, err
	}
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return crmcontracts.Organization{}, err
	}
	if sourceID == targetID {
		return crmcontracts.Organization{}, &MergeSelfError{}
	}
	active, err := s.activeColumns(ctx, "organization")
	if err != nil {
		return crmcontracts.Organization{}, err
	}

	var out crmcontracts.Organization
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// A merge fills the survivor's legal_name from the record it retires
		// (fillOrgSurvivorship), so it is a name writer like any other and owes
		// the same two things: the name lock BEFORE any organization row lock,
		// and a re-check afterwards. Taking it here rather than beside the fill
		// is what keeps the order — LockPair is next.
		if err := lockOrgNameWrites(ctx, tx); err != nil {
			return err
		}
		// The pair lock keeps BOTH endpoints held to commit: without it a
		// concurrent merge(target→elsewhere) archives the survivor
		// mid-merge and the relinked children point at a dead record.
		_, tgtLock, err := storekit.LockPair(ctx, tx, "organization", sourceID.UUID, targetID.UUID)
		if err != nil {
			return err
		}
		src, tgt, err := mergePair(ctx, tx, "organization", sourceID, targetID, readOrgMergeState)
		if err != nil {
			return err
		}
		if err := refuseWhenBothCarryProjects(ctx, tx, sourceID, targetID); err != nil {
			return err
		}
		targetIsPartner, err := relinkOrgAssociations(ctx, tx, sourceID, targetID)
		if err != nil {
			return err
		}
		filled, err := fillOrgSurvivorship(ctx, tx, src, tgt, targetIsPartner, tgtLock)
		if err != nil {
			return err
		}
		out, err = finalizeOrgMerge(ctx, tx, sourceID, targetID, filled, active)
		if err != nil {
			return err
		}
		// A survivor that just inherited the retired record's legal name can now
		// be the twin of a THIRD organization, and resolving one duplicate is no
		// reason to leave that one unfiled. Only when the name actually moved: a
		// merge that filled nothing renames nobody.
		//
		// It runs after finalizeOrgMerge, which retires the source. Before it,
		// the source still reads as live AND holds the very name it has just
		// donated, so it scores 1.0, wins the ranked list, and the pair filed
		// names a row archived one statement later — a pair no human can ever
		// dispose of, because merging it answers AlreadyMerged and that reopens
		// it. The genuine third record is never reached, since the walk stops at
		// the first unfiled rival.
		if _, renamed := filled[fieldLegalName]; renamed {
			by, err := storekit.CapturedBy(ctx)
			if err != nil {
				return err
			}
			if err := recheckOrgNameForDuplicates(ctx, tx, targetID, by); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// relinkOrgAssociations moves every association off the merged-away org
// onto the survivor — domains (demoting a duplicate primary), relationship
// edges, activity/list/tag rows, and the deal/hierarchy/partner references
// — and reports whether the survivor ends up holding a partner row.
func relinkOrgAssociations(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.OrganizationID) (bool, error) {
	if _, err := relinkDemotingPrimary(ctx, tx, `
		UPDATE organization_domain a SET organization_id = $2,
		  is_primary = a.is_primary AND NOT EXISTS (
		    SELECT 1 FROM organization_domain b
		    WHERE b.organization_id = $2 AND b.is_primary AND b.archived_at IS NULL)
		WHERE a.organization_id = $1 AND a.archived_at IS NULL`, sourceID.UUID, targetID.UUID); err != nil {
		return false, fmt.Errorf("relink domains: %w", err)
	}
	if err := relinkOrgEdges(ctx, tx, sourceID, targetID); err != nil {
		return false, fmt.Errorf("relink relationships: %w", err)
	}
	if _, err := relinkLinkRows(ctx, tx, "organization", sourceID.UUID, targetID.UUID); err != nil {
		return false, fmt.Errorf("relink activity/list/tag rows: %w", err)
	}
	return absorbOrgReferences(ctx, tx, sourceID, targetID)
}

// fillOrgSurvivorship folds the merged-away org's fields into the survivor
// where the survivor is blank and, when the survivor gained the 1:1 partner
// extension, flips its classification to 'partner' (the A41 invariant:
// classification='partner' iff a partner row exists). It returns the
// applied after-image for the merge audit.
func fillOrgSurvivorship(ctx context.Context, tx pgx.Tx, src, tgt crmcontracts.Organization, targetIsPartner bool, tgtLock storekit.RowLock) (map[string]any, error) {
	p := storekit.NewPatch()
	fillString(p, fieldLegalName, tgt.LegalName, src.LegalName)
	fillString(p, "industry", tgt.Industry, src.Industry)
	if targetIsPartner && (tgt.Classification == nil || *tgt.Classification != crmcontracts.OrganizationClassificationPartner) {
		p.Set("classification", tgt.Classification, "partner")
	}
	if !p.Empty() {
		if err := p.ApplyLocked(ctx, tx, tgtLock); err != nil {
			return nil, fmt.Errorf("apply survivorship fill: %w", err)
		}
	}
	return p.After(), nil
}

// finalizeOrgMerge retires the merged-away org and records the merge on the
// write shape — audit row plus organization.merged event in the one
// transaction — then returns the reloaded survivor.
func finalizeOrgMerge(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.OrganizationID, filled map[string]any, active []fieldcatalog.Column) (crmcontracts.Organization, error) {
	if err := archiveMergedAway(ctx, tx, "organization", sourceID.UUID, targetID.UUID); err != nil {
		return crmcontracts.Organization{}, fmt.Errorf("retire merged-away organization: %w", err)
	}
	auditID, err := storekit.Audit(ctx, tx, "merge", "organization", sourceID.UUID,
		map[string]any{"merged_into_id": nil},
		map[string]any{"merged_into_id": targetID, "filled": filled})
	if err != nil {
		return crmcontracts.Organization{}, fmt.Errorf("audit organization merge: %w", err)
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, sourceID.UUID, crmcontracts.PublicEventOrganizationMerged{
		MergedFromId: openapi_types.UUID(sourceID.UUID),
		MergedIntoId: openapi_types.UUID(targetID.UUID),
	}); err != nil {
		return crmcontracts.Organization{}, fmt.Errorf("emit organization.merged: %w", err)
	}
	out, err := readOrganization(ctx, tx, targetID, storekit.LiveOnly, active)
	if err != nil {
		return crmcontracts.Organization{}, fmt.Errorf("read surviving organization: %w", err)
	}
	return out, nil
}

// absorbOrgReferences re-homes everything beyond the relationship and
// link tables that points at the source org — deal attributions, the
// org hierarchy, the 1:1 partner extension, and the merge redirect
// chain — and reports whether the survivor ends up holding a partner
// row (the A41 classification invariant needs to know).
func absorbOrgReferences(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.OrganizationID) (bool, error) {
	for _, stmt := range []string{
		// The project moves FIRST, and the order is load-bearing: the
		// deal_project_same_org trigger is INITIALLY IMMEDIATE, so moving a
		// deal while its project still points at the dissolved org fires it
		// mid-merge. A project's anchor is NOT NULL ... ON DELETE RESTRICT
		// and so cannot stay behind either (PROJ-LIFE-4) — leaving it is
		// what turns a healthy deal un-editable over a mismatch nobody made.
		`UPDATE project SET organization_id = $2 WHERE organization_id = $1`,
		`UPDATE deal SET organization_id = $2 WHERE organization_id = $1`,
		`UPDATE deal SET partner_org_id = $2 WHERE partner_org_id = $1`,
	} {
		if _, err := tx.Exec(ctx, stmt, sourceID, targetID); err != nil {
			return false, fmt.Errorf("repoint project and deal attributions: %w", err)
		}
	}

	// Hierarchy: if the survivor sits under the source, lift it to the
	// source's parent first — otherwise absorbing the source's
	// children would make B its own ancestor.
	if _, err := tx.Exec(ctx, `
		UPDATE organization SET parent_org_id =
		  (SELECT parent_org_id FROM organization WHERE id = $1)
		WHERE id = $2 AND parent_org_id = $1`, sourceID, targetID); err != nil {
		return false, fmt.Errorf("lift survivor out of source hierarchy: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE organization SET parent_org_id = $2 WHERE parent_org_id = $1`,
		sourceID, targetID); err != nil {
		return false, fmt.Errorf("re-parent child organizations: %w", err)
	}

	// The 1:1 partner extension moves only into a vacancy; when both
	// records carry program state the survivor's stands and the
	// source's rides its archived org untouched (recoverable, never
	// silently blended).
	var targetIsPartner bool
	if err := tx.QueryRow(ctx, `
		WITH moved AS (
		  UPDATE partner SET organization_id = $2
		  WHERE organization_id = $1
		    AND NOT EXISTS (SELECT 1 FROM partner WHERE organization_id = $2)
		  RETURNING 1)
		SELECT EXISTS (SELECT 1 FROM moved)
		    OR EXISTS (SELECT 1 FROM partner WHERE organization_id = $2)`,
		sourceID, targetID).Scan(&targetIsPartner); err != nil {
		return false, fmt.Errorf("move partner extension: %w", err)
	}
	// Earlier merged-away rows repoint too: the redirect chain stays
	// one hop deep, so following merged_into_id always lands live.
	if _, err := tx.Exec(ctx,
		`UPDATE organization SET merged_into_id = $2 WHERE merged_into_id = $1`,
		sourceID, targetID); err != nil {
		return false, fmt.Errorf("repoint earlier merges: %w", err)
	}
	return targetIsPartner, nil
}

// readOrgMergeState loads one end of an organization merge: a live row
// returns itself; an archived one returns its redirect pointer (nil when
// it was plain-archived, not merged).
func readOrgMergeState(ctx context.Context, tx pgx.Tx, id ids.OrganizationID) (crmcontracts.Organization, *ids.UUID, error) {
	// A merge-state read feeds the resolution decision, never the wire —
	// core columns suffice.
	o, err := readOrganization(ctx, tx, id, storekit.IncludeArchived, nil)
	if err != nil {
		return crmcontracts.Organization{}, nil, err
	}
	if o.ArchivedAt == nil {
		return o, nil, nil
	}
	return crmcontracts.Organization{}, (*ids.UUID)(o.MergedIntoId), apperrors.ErrNotFound
}

// relinkOrgEdges moves A's relationship edges to B across both org
// columns. Order matters: edges that would degenerate (A↔B partner
// edges, duplicates of what B already has) archive first, then the
// survivors relink.
func relinkOrgEdges(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.OrganizationID) error {
	now := time.Now().UTC()
	// An edge between the two merging orgs would become a self-edge.
	if _, err := tx.Exec(ctx, `
		UPDATE relationship SET archived_at = $3
		WHERE archived_at IS NULL
		  AND ((organization_id = $1 AND counterparty_org_id = $2)
		    OR (organization_id = $2 AND counterparty_org_id = $1))`,
		sourceID, targetID, now); err != nil {
		return err
	}
	// Duplicates of edges the survivor already has, on either column.
	if _, err := tx.Exec(ctx, `
		UPDATE relationship a SET archived_at = $3
		WHERE a.archived_at IS NULL
		  AND (a.organization_id = $1 OR a.counterparty_org_id = $1)
		  AND EXISTS (
		    SELECT 1 FROM relationship b
		    WHERE b.kind = a.kind AND b.archived_at IS NULL AND b.id <> a.id
		      AND b.person_id IS NOT DISTINCT FROM a.person_id
		      AND b.deal_id IS NOT DISTINCT FROM a.deal_id
		      AND b.organization_id IS NOT DISTINCT FROM
		            (CASE WHEN a.organization_id = $1 THEN $2::uuid ELSE a.organization_id END)
		      AND b.counterparty_org_id IS NOT DISTINCT FROM
		            (CASE WHEN a.counterparty_org_id = $1 THEN $2::uuid ELSE a.counterparty_org_id END))`,
		sourceID, targetID, now); err != nil {
		return err
	}
	// Relinked employment edges keep ≤1 current-primary per person.
	if _, err := tx.Exec(ctx, `
		UPDATE relationship a SET organization_id = $2,
		  is_current_primary = a.is_current_primary AND NOT EXISTS (
		    SELECT 1 FROM relationship b
		    WHERE b.person_id = a.person_id AND b.kind = 'employment'
		      AND b.is_current_primary AND b.archived_at IS NULL AND b.id <> a.id)
		WHERE a.organization_id = $1 AND a.archived_at IS NULL`, sourceID, targetID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx,
		`UPDATE relationship SET counterparty_org_id = $2
		 WHERE counterparty_org_id = $1 AND archived_at IS NULL`, sourceID, targetID)
	return err
}

// refuseWhenBothCarryProjects enforces PROJ-LIFE-4's ask. Two companies
// that each hold live bodies of work may, once merged, be running the same
// one twice or two genuinely different ones — and nothing in the data says
// which. Combining them silently would leave a human to find the duplicates
// later, so the merge stops and names them instead. The refusal is
// actionable and reversible: archive or re-anchor one side, then merge.
//
// Only live projects count. An archived project is a grouping already ended,
// and it relinks with everything else.
// The DECISION is unscoped and the NAMING is not, and the split is the whole
// point: work the caller cannot see must still block the merge, or a rep
// would quietly combine two companies whose projects another team owns. But
// naming a project is a read of it, so the refusal lists only the ones this
// caller may already see and counts the rest.
func refuseWhenBothCarryProjects(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.OrganizationID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	sourcePos, targetPos := arg(sourceID), arg(targetID)
	scope, err := auth.ScopeClauseFor(ctx, projectObjectName, "", arg)
	if err != nil {
		return err
	}
	visible := "true"
	if scope != "" {
		visible = scope
	}
	rows, err := tx.Query(ctx, storekit.SQLf(`
		SELECT organization_id, name, (%s) AS visible FROM project
		WHERE organization_id IN ($%d, $%d) AND archived_at IS NULL
		ORDER BY organization_id, name`, visible, sourcePos, targetPos), args...)
	if err != nil {
		return fmt.Errorf("read projects on both merge endpoints: %w", err)
	}
	defer rows.Close()

	var refusal BothCompaniesCarryProjectsError
	for rows.Next() {
		var org ids.UUID
		var name string
		var canSee bool
		if err := rows.Scan(&org, &name, &canSee); err != nil {
			return err
		}
		side, names := &refusal.TargetCount, &refusal.Target
		if org == sourceID.UUID {
			side, names = &refusal.SourceCount, &refusal.Source
		}
		*side++
		if canSee {
			*names = append(*names, name)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if refusal.SourceCount > 0 && refusal.TargetCount > 0 {
		return &refusal
	}
	return nil
}

// BothCompaniesCarryProjectsError maps to 409: the merge needs a human to
// say whether the two sets of work are the same body of work or two.
// Source and Target name only the projects the caller may see; the counts
// cover every live project on each side, visible or not. A caller with no
// sight of either side still learns the merge is blocked and why, without
// learning what another team is working on.
type BothCompaniesCarryProjectsError struct {
	Source      []string
	Target      []string
	SourceCount int
	TargetCount int
}

func (e *BothCompaniesCarryProjectsError) Error() string {
	return "both companies have live projects (" +
		mergeSideSummary(e.Source, e.SourceCount) + " and " + mergeSideSummary(e.Target, e.TargetCount) +
		"); archive or re-anchor one side before merging, so the merge does not guess whether they are the same work"
}

// mergeSideSummary names what the caller may see and acknowledges the rest
// WITHOUT counting it. "Other work is live here" is as actionable as a number
// — either way the answer is to archive or re-anchor a side — but a number is
// a census: repeat the merge against every company you can reach and the
// refusals tell you how much hidden work each one carries, and how that
// changes week to week. The caller's own side is theirs to count; the other
// side's total is not something this refusal needs to settle.
func mergeSideSummary(names []string, total int) string {
	switch {
	case len(names) == 0:
		return "work you cannot see"
	case len(names) == total:
		return strings.Join(names, ", ")
	default:
		return strings.Join(names, ", ") + ", and other work you cannot see"
	}
}
