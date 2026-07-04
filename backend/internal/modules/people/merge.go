// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The §1.3 two-record merge (features/01, data-model §3.2): A→B relinks
// everything that points at A to B in ONE transaction with zero orphaned
// FKs, fills B's gaps from A without overwriting anything B already has,
// and archives A with merged_into_id = B so it stays fetchable by id.
// Relinking is collision-aware, not a blind UPDATE: every unique index
// and shape constraint on the child tables encodes an invariant the
// surviving record must still satisfy afterwards —
//
//   - ≤1 primary email/phone per (person, type) and ≤1 primary domain per
//     org: A's primaries demote when B already holds that slot.
//   - ≤1 current-primary employer per person: same demotion rule.
//   - an activity/list/tag linked to BOTH records keeps B's link and
//     drops A's (pure link rows, deletion loses nothing).
//   - a relationship edge A already shares with B (same kind + same far
//     end) archives instead of relinking — a duplicate edge is noise,
//     and archived rows keep the provenance.
//   - a partner edge BETWEEN A and B can survive on neither (an org
//     cannot partner with itself): it archives.
//
// Consent merges restrictively: a merge may only ever REDUCE what the
// workspace is allowed to do with the surviving person, never expand it —
// A's withdrawal propagates to B; A's grant does not.

// MergeSelfError maps to 422: a record cannot merge into itself.
type MergeSelfError struct{}

func (e *MergeSelfError) Error() string { return "source and target of a merge must differ" }

// AlreadyMergedError maps to 409: the source was already merged away; the
// pointer names where it went.
type AlreadyMergedError struct {
	Kind   string
	IntoID ids.UUID
}

func (e *AlreadyMergedError) Error() string { return e.Kind + " is already merged" }

// MergedTargetError maps to 422: the chosen survivor is itself archived
// or merged away — nothing can merge INTO a dead record.
type MergedTargetError struct{ Kind string }

func (e *MergedTargetError) Error() string {
	return "merge target " + e.Kind + " is archived; the survivor must be live"
}

// relinkCounts is the event payload's accounting (events.md §person.merged):
// downstream consumers re-home their references from these numbers.
type relinkCounts struct {
	Emails        int64 `json:"emails"`
	Phones        int64 `json:"phones"`
	Relationships int64 `json:"relationships"`
	ActivityLinks int64 `json:"activity_links"`
}

// MergePerson merges person source→target and returns the survivor.
func (s *Store) MergePerson(ctx context.Context, sourceID, targetID ids.UUID) (crmcontracts.Person, error) {
	// authz.go maps the merge verb to update: rewriting where records
	// point is curation of both rows, not deletion of one.
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
		return crmcontracts.Person{}, err
	}
	if sourceID == targetID {
		return crmcontracts.Person{}, &MergeSelfError{}
	}

	var out crmcontracts.Person
	err := s.tx(ctx, func(tx pgx.Tx) error {
		source, target, err := mergePair(ctx, tx, "person", sourceID, targetID, readPersonMergeState)
		if err != nil {
			return err
		}

		counts := relinkCounts{}
		if counts.Emails, err = relinkDemotingPrimary(ctx, tx, `
			UPDATE person_email a SET person_id = $2,
			  is_primary = a.is_primary AND NOT EXISTS (
			    SELECT 1 FROM person_email b
			    WHERE b.person_id = $2 AND b.email_type = a.email_type
			      AND b.is_primary AND b.archived_at IS NULL)
			WHERE a.person_id = $1 AND a.archived_at IS NULL`, sourceID, targetID); err != nil {
			return err
		}
		if counts.Phones, err = relinkDemotingPrimary(ctx, tx, `
			UPDATE person_phone a SET person_id = $2,
			  is_primary = a.is_primary AND NOT EXISTS (
			    SELECT 1 FROM person_phone b
			    WHERE b.person_id = $2 AND b.phone_type = a.phone_type
			      AND b.is_primary AND b.archived_at IS NULL)
			WHERE a.person_id = $1 AND a.archived_at IS NULL`, sourceID, targetID); err != nil {
			return err
		}
		if counts.Relationships, err = relinkPersonEdges(ctx, tx, sourceID, targetID); err != nil {
			return err
		}
		if counts.ActivityLinks, err = relinkLinkRows(ctx, tx, "person", sourceID, targetID); err != nil {
			return err
		}
		if err := mergeConsent(ctx, tx, sourceID, targetID); err != nil {
			return err
		}
		// The promotion outcome pointer follows the survivor so a
		// re-promote 409 names a live person.
		if _, err := tx.Exec(ctx,
			`UPDATE lead SET promoted_person_id = $2 WHERE promoted_person_id = $1`,
			sourceID, targetID); err != nil {
			return err
		}
		// Earlier merged-away rows repoint too: the redirect chain stays
		// one hop deep, so following merged_into_id always lands live.
		if _, err := tx.Exec(ctx,
			`UPDATE person SET merged_into_id = $2 WHERE merged_into_id = $1`,
			sourceID, targetID); err != nil {
			return err
		}

		// Survivorship is fill-only: A never overwrites what B has.
		src, tgt := source.(crmcontracts.Person), target.(crmcontracts.Person)
		p := storekit.NewPatch()
		fillString(p, "first_name", tgt.FirstName, src.FirstName)
		fillString(p, "last_name", tgt.LastName, src.LastName)
		fillString(p, "title", tgt.Title, src.Title)
		if tgt.ConvertedFromLeadId == nil && src.ConvertedFromLeadId != nil {
			p.Set("converted_from_lead_id", nil, ids.UUID(*src.ConvertedFromLeadId))
		}
		if (tgt.Social == nil || len(*tgt.Social) == 0) && src.Social != nil && len(*src.Social) > 0 {
			p.Set("social", nil, storekit.JSONArg(*src.Social))
		}
		if !p.Empty() {
			if err := p.Apply(ctx, tx, "person", targetID, nil); err != nil {
				return err
			}
		}

		if err := archiveMergedAway(ctx, tx, "person", sourceID, targetID); err != nil {
			return err
		}

		auditID, err := storekit.Audit(ctx, tx, "merge", "person", sourceID,
			map[string]any{"merged_into_id": nil},
			map[string]any{"merged_into_id": targetID, "relinked": counts, "filled": p.After()})
		if err != nil {
			return err
		}
		// One event, its own verb: the context graph collapses two nodes,
		// which neither person.updated nor person.archived can say.
		if err := storekit.Emit(ctx, tx, auditID, "person.merged", "person", sourceID, map[string]any{
			"merged_from_id": sourceID,
			"merged_into_id": targetID,
			"relinked":       counts,
		}); err != nil {
			return err
		}

		out, err = readPerson(ctx, tx, targetID, false)
		return err
	})
	return out, err
}

// MergeOrganization merges organization source→target and returns the
// survivor. The org half additionally re-homes the hierarchy (A's
// children become B's) and the deal/partner attributions.
func (s *Store) MergeOrganization(ctx context.Context, sourceID, targetID ids.UUID) (crmcontracts.Organization, error) {
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return crmcontracts.Organization{}, err
	}
	if sourceID == targetID {
		return crmcontracts.Organization{}, &MergeSelfError{}
	}

	var out crmcontracts.Organization
	err := s.tx(ctx, func(tx pgx.Tx) error {
		source, target, err := mergePair(ctx, tx, "organization", sourceID, targetID, readOrgMergeState)
		if err != nil {
			return err
		}

		if _, err := relinkDemotingPrimary(ctx, tx, `
			UPDATE organization_domain a SET organization_id = $2,
			  is_primary = a.is_primary AND NOT EXISTS (
			    SELECT 1 FROM organization_domain b
			    WHERE b.organization_id = $2 AND b.is_primary AND b.archived_at IS NULL)
			WHERE a.organization_id = $1 AND a.archived_at IS NULL`, sourceID, targetID); err != nil {
			return err
		}
		if err := relinkOrgEdges(ctx, tx, sourceID, targetID); err != nil {
			return err
		}
		if _, err := relinkLinkRows(ctx, tx, "organization", sourceID, targetID); err != nil {
			return err
		}
		for _, stmt := range []string{
			`UPDATE deal SET organization_id = $2 WHERE organization_id = $1`,
			`UPDATE deal SET partner_org_id = $2 WHERE partner_org_id = $1`,
		} {
			if _, err := tx.Exec(ctx, stmt, sourceID, targetID); err != nil {
				return err
			}
		}

		// Hierarchy: if the survivor sits under the source, lift it to the
		// source's parent first — otherwise absorbing the source's
		// children would make B its own ancestor.
		if _, err := tx.Exec(ctx, `
			UPDATE organization SET parent_org_id =
			  (SELECT parent_org_id FROM organization WHERE id = $1)
			WHERE id = $2 AND parent_org_id = $1`, sourceID, targetID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE organization SET parent_org_id = $2 WHERE parent_org_id = $1`,
			sourceID, targetID); err != nil {
			return err
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
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE organization SET merged_into_id = $2 WHERE merged_into_id = $1`,
			sourceID, targetID); err != nil {
			return err
		}

		src, tgt := source.(crmcontracts.Organization), target.(crmcontracts.Organization)
		p := storekit.NewPatch()
		fillString(p, "legal_name", tgt.LegalName, src.LegalName)
		fillString(p, "industry", tgt.Industry, src.Industry)
		if targetIsPartner && (tgt.Classification == nil || *tgt.Classification != crmcontracts.OrganizationClassificationPartner) {
			// The partner invariant (A41): classification='partner' iff a
			// partner row exists — the survivor gained one, so it flips.
			p.Set("classification", tgt.Classification, "partner")
		}
		if !p.Empty() {
			if err := p.Apply(ctx, tx, "organization", targetID, nil); err != nil {
				return err
			}
		}

		if err := archiveMergedAway(ctx, tx, "organization", sourceID, targetID); err != nil {
			return err
		}

		auditID, err := storekit.Audit(ctx, tx, "merge", "organization", sourceID,
			map[string]any{"merged_into_id": nil},
			map[string]any{"merged_into_id": targetID, "filled": p.After()})
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "organization.merged", "organization", sourceID, map[string]any{
			"merged_from_id": sourceID,
			"merged_into_id": targetID,
		}); err != nil {
			return err
		}

		out, err = readOrganization(ctx, tx, targetID, false)
		return err
	})
	return out, err
}

// readPersonMergeState / readOrgMergeState load one end of a merge: a
// live row returns itself; an archived one returns its redirect pointer
// (nil when it was plain-archived, not merged).
func readPersonMergeState(ctx context.Context, tx pgx.Tx, id ids.UUID) (any, *ids.UUID, error) {
	p, err := readPerson(ctx, tx, id, true)
	if err != nil {
		return nil, nil, err
	}
	if p.ArchivedAt == nil {
		return p, nil, nil
	}
	return nil, (*ids.UUID)(p.MergedIntoId), apperrors.ErrNotFound
}

func readOrgMergeState(ctx context.Context, tx pgx.Tx, id ids.UUID) (any, *ids.UUID, error) {
	o, err := readOrganization(ctx, tx, id, true)
	if err != nil {
		return nil, nil, err
	}
	if o.ArchivedAt == nil {
		return o, nil, nil
	}
	return nil, (*ids.UUID)(o.MergedIntoId), apperrors.ErrNotFound
}

// mergePair resolves and validates both ends. The source must be live and
// visible; a source that was already merged away answers 409 with the
// pointer (the caller just proved they can address the row, so the
// outcome discloses nothing new — the AlreadyPromoted precedent). The
// target must be live too: merging is a read of the survivor it returns,
// so an out-of-scope target answers a bare conflict, and an archived one
// can survive nothing.
func mergePair(ctx context.Context, tx pgx.Tx, kind string, sourceID, targetID ids.UUID,
	read func(context.Context, pgx.Tx, ids.UUID) (any, *ids.UUID, error)) (source, target any, err error) {
	if err := auth.EnsureVisible(ctx, tx, kind, sourceID); err != nil {
		return nil, nil, err
	}
	source, mergedInto, err := read(ctx, tx, sourceID)
	if err != nil {
		if mergedInto != nil && !mergedInto.IsZero() {
			return nil, nil, &AlreadyMergedError{Kind: kind, IntoID: *mergedInto}
		}
		return nil, nil, err
	}

	visible, err := auth.VisibleTo(ctx, tx, kind, targetID)
	if err != nil {
		return nil, nil, err
	}
	if !visible {
		return nil, nil, apperrors.ErrConflict
	}
	target, _, err = read(ctx, tx, targetID)
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			return nil, nil, &MergedTargetError{Kind: kind}
		}
		return nil, nil, err
	}
	return source, target, nil
}

// relinkDemotingPrimary runs a relink UPDATE whose SET clause demotes the
// row's primary flag when the survivor already fills that primary slot.
func relinkDemotingPrimary(ctx context.Context, tx pgx.Tx, stmt string, sourceID, targetID ids.UUID) (int64, error) {
	tag, err := tx.Exec(ctx, stmt, sourceID, targetID)
	return tag.RowsAffected(), err
}

// relinkPersonEdges moves A's relationship edges to B: duplicates of an
// edge B already has archive, the rest relink with the current-primary
// employer flag demoted when B already has one.
func relinkPersonEdges(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.UUID) (int64, error) {
	if _, err := tx.Exec(ctx, `
		UPDATE relationship a SET archived_at = $3
		WHERE a.person_id = $1 AND a.archived_at IS NULL AND EXISTS (
		  SELECT 1 FROM relationship b
		  WHERE b.person_id = $2 AND b.kind = a.kind AND b.archived_at IS NULL
		    AND b.organization_id IS NOT DISTINCT FROM a.organization_id
		    AND b.deal_id IS NOT DISTINCT FROM a.deal_id)`,
		sourceID, targetID, time.Now().UTC()); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE relationship a SET person_id = $2,
		  is_current_primary = a.is_current_primary AND NOT EXISTS (
		    SELECT 1 FROM relationship b
		    WHERE b.person_id = $2 AND b.kind = 'employment'
		      AND b.is_current_primary AND b.archived_at IS NULL)
		WHERE a.person_id = $1 AND a.archived_at IS NULL`, sourceID, targetID)
	return tag.RowsAffected(), err
}

// relinkOrgEdges moves A's relationship edges to B across both org
// columns. Order matters: edges that would degenerate (A↔B partner
// edges, duplicates of what B already has) archive first, then the
// survivors relink.
func relinkOrgEdges(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.UUID) error {
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

// relinkLinkRows re-homes the pure link tables (activity_link,
// list_member, taggable): a link the survivor already holds drops A's
// copy — these rows carry no provenance of their own, so deletion loses
// nothing — and the rest relink.
func relinkLinkRows(ctx context.Context, tx pgx.Tx, entityType string, sourceID, targetID ids.UUID) (int64, error) {
	column := entityType + "_id" // person_id | organization_id
	if _, err := tx.Exec(ctx, `
		DELETE FROM activity_link a
		WHERE a.`+column+` = $1 AND EXISTS (
		  SELECT 1 FROM activity_link b
		  WHERE b.activity_id = a.activity_id AND b.`+column+` = $2)`,
		sourceID, targetID); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx,
		`UPDATE activity_link SET `+column+` = $2 WHERE `+column+` = $1`, sourceID, targetID)
	if err != nil {
		return 0, err
	}
	relinked := tag.RowsAffected()

	for _, t := range []struct{ table, key string }{
		{"list_member", "list_id"},
		{"taggable", "tag_id"},
	} {
		if _, err := tx.Exec(ctx, `
			DELETE FROM `+t.table+` a
			WHERE a.entity_type = $3 AND a.entity_id = $1 AND EXISTS (
			  SELECT 1 FROM `+t.table+` b
			  WHERE b.`+t.key+` = a.`+t.key+` AND b.entity_type = $3 AND b.entity_id = $2)`,
			sourceID, targetID, entityType); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE `+t.table+` SET entity_id = $2 WHERE entity_type = $3 AND entity_id = $1`,
			sourceID, targetID, entityType); err != nil {
			return 0, err
		}
	}
	return relinked, nil
}

// mergeConsent applies the restrictive rule: the survivor's consent may
// only tighten. A's withdrawal overrides B's state; A's grant never
// upgrades B (expanding contact rights must come from a captured consent
// event, not a data-hygiene action). The append-only proof log relinks
// in full, so the evidence trail survives on the one person.
func mergeConsent(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.UUID) error {
	if _, err := tx.Exec(ctx, `
		UPDATE person_consent b SET state = 'withdrawn'
		FROM person_consent a
		WHERE a.person_id = $1 AND b.person_id = $2
		  AND a.purpose_id = b.purpose_id
		  AND a.state = 'withdrawn' AND b.state <> 'withdrawn'`,
		sourceID, targetID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM person_consent a
		WHERE a.person_id = $1 AND EXISTS (
		  SELECT 1 FROM person_consent b
		  WHERE b.person_id = $2 AND b.purpose_id = a.purpose_id)`,
		sourceID, targetID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE person_consent SET person_id = $2 WHERE person_id = $1`,
		sourceID, targetID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx,
		`UPDATE consent_event SET person_id = $2 WHERE person_id = $1`,
		sourceID, targetID)
	return err
}

// archiveMergedAway retires the source row: archived + the redirect
// pointer, in one statement so a concurrent merge of the same source
// loses the race instead of double-writing.
func archiveMergedAway(ctx context.Context, tx pgx.Tx, table string, sourceID, targetID ids.UUID) error {
	tag, err := tx.Exec(ctx,
		`UPDATE `+table+` SET archived_at = $3, merged_into_id = $2
		 WHERE id = $1 AND archived_at IS NULL`,
		sourceID, targetID, time.Now().UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrConflict
	}
	return nil
}

// fillString sets a nullable text column from the source only when the
// survivor has none (fill-only survivorship).
func fillString(p *storekit.Patch, column string, target, source *string) {
	if target == nil && source != nil {
		p.Set(column, nil, *source)
	}
}
