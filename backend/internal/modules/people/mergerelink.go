// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Re-homing everything that points at the merged-away person.
//
// Split from merge.go because it answers a different question: merge.go
// decides WHICH record survives and what the survivor inherits, and this
// decides where every satellite row now lives. A satellite this misses is not
// a broken merge — it is a row still pointing at a record no read returns,
// which is worse, because nothing fails and the data is simply gone from view.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// relinkPersonReferences re-homes everything that points at the source
// person — emails and phones (primaries demote when the survivor holds
// the slot), relationship edges, the pure link tables, consent (the
// restrictive rule), the lead promotion pointer, and the merge redirect
// chain — and returns the accounting the person.merged event carries.
func relinkPersonReferences(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.PersonID) (relinkCounts, error) {
	counts := relinkCounts{}
	var err error
	if counts.Emails, err = relinkDemotingPrimary(ctx, tx, `
		UPDATE person_email a SET person_id = $2,
		  is_primary = a.is_primary AND NOT EXISTS (
		    SELECT 1 FROM person_email b
		    WHERE b.person_id = $2 AND b.email_type = a.email_type
		      AND b.is_primary AND b.archived_at IS NULL)
		WHERE a.person_id = $1 AND a.archived_at IS NULL`, sourceID.UUID, targetID.UUID); err != nil {
		return counts, fmt.Errorf("relink emails: %w", err)
	}
	if counts.Phones, err = relinkDemotingPrimary(ctx, tx, `
		UPDATE person_phone a SET person_id = $2,
		  is_primary = a.is_primary AND NOT EXISTS (
		    SELECT 1 FROM person_phone b
		    WHERE b.person_id = $2 AND b.phone_type = a.phone_type
		      AND b.is_primary AND b.archived_at IS NULL)
		WHERE a.person_id = $1 AND a.archived_at IS NULL`, sourceID.UUID, targetID.UUID); err != nil {
		return counts, fmt.Errorf("relink phones: %w", err)
	}
	// The enrichment sidecar moves with the person. Left behind it is
	// invisible to every read of the survivor, so the evidence for their title
	// would vanish at a merge nobody expected to lose it — and the row would
	// then outlive the merged-away record's own archival.
	//
	// ON CONFLICT DO NOTHING because the uniqueness is (person, field): where
	// the survivor already holds that field, theirs is the one a human has
	// been reading, and the merged-away copy is dropped rather than allowed to
	// overwrite it.
	if _, err = tx.Exec(ctx, `
		INSERT INTO person_profile_field
		  (workspace_id, person_id, field, value, evidence_snippet, source_ref, confidence, source, captured_by)
		SELECT workspace_id, $2, field, value, evidence_snippet, source_ref, confidence, source, captured_by
		  FROM person_profile_field WHERE person_id = $1
		ON CONFLICT DO NOTHING`, sourceID.UUID, targetID.UUID); err != nil {
		return counts, fmt.Errorf("relink enrichment fields: %w", err)
	}
	if _, err = tx.Exec(ctx,
		`DELETE FROM person_profile_field WHERE person_id = $1`, sourceID.UUID); err != nil {
		return counts, fmt.Errorf("retire the merged-away enrichment fields: %w", err)
	}
	if counts.Relationships, err = relinkPersonEdges(ctx, tx, sourceID, targetID); err != nil {
		return counts, fmt.Errorf("relink relationships: %w", err)
	}
	if counts.ActivityLinks, err = relinkLinkRows(ctx, tx, "person", sourceID.UUID, targetID.UUID); err != nil {
		return counts, fmt.Errorf("relink activity/list/tag rows: %w", err)
	}
	if err := mergePersonSocial(ctx, tx, sourceID, targetID); err != nil {
		return counts, fmt.Errorf("merge social rows: %w", err)
	}
	if err := mergeConsent(ctx, tx, sourceID, targetID); err != nil {
		return counts, fmt.Errorf("merge consent: %w", err)
	}
	// The channel identities follow the survivor, or the human behind them
	// keeps writing into the record nobody reads any more. No survivor-wins
	// rule is needed here, unlike the email and phone slots above: the unique
	// key spans (provider, channel_user_id) WITHOUT person_id, so the two
	// halves cannot both hold the same identity live and the relink cannot
	// collide.
	if _, err := tx.Exec(ctx, `
		UPDATE person_channel_identity SET person_id = $2
		WHERE person_id = $1 AND archived_at IS NULL`, sourceID, targetID); err != nil {
		return counts, fmt.Errorf("relink channel identities: %w", err)
	}
	// The promotion outcome pointer follows the survivor so a
	// re-promote 409 names a live person.
	if _, err := tx.Exec(ctx,
		`UPDATE lead SET promoted_person_id = $2 WHERE promoted_person_id = $1`,
		sourceID, targetID); err != nil {
		return counts, fmt.Errorf("repoint lead promotions: %w", err)
	}
	// Earlier merged-away rows repoint too: the redirect chain stays
	// one hop deep, so following merged_into_id always lands live.
	if _, err := tx.Exec(ctx,
		`UPDATE person SET merged_into_id = $2 WHERE merged_into_id = $1`,
		sourceID, targetID); err != nil {
		return counts, fmt.Errorf("repoint earlier merges: %w", err)
	}
	return counts, nil
}

// readPersonMergeState loads one end of a person merge: a live row
// returns itself; an archived one returns its redirect pointer (nil when
// it was plain-archived, not merged). readOrgMergeState (merge_organization.go)
// is its organization twin.
func readPersonMergeState(ctx context.Context, tx pgx.Tx, id ids.PersonID) (crmcontracts.Person, *ids.UUID, error) {
	// A merge-state read feeds the resolution decision, never the wire —
	// core columns suffice.
	p, err := readPerson(ctx, tx, id, storekit.IncludeArchived, nil)
	if err != nil {
		return crmcontracts.Person{}, nil, err
	}
	if p.ArchivedAt == nil {
		return p, nil, nil
	}
	return crmcontracts.Person{}, (*ids.UUID)(p.MergedIntoId), apperrors.ErrNotFound
}

// mergePair resolves and validates both ends. The source must be live and
// visible; a source that was already merged away answers 409 with the
// pointer (the caller just proved they can address the row, so the
// outcome discloses nothing new — the AlreadyPromoted precedent). The
// target must be live too: merging is a read of the survivor it returns,
// so an out-of-scope target answers a bare conflict, and an archived one
// can survive nothing.
func mergePair[T any, K ids.EntityKind](ctx context.Context, tx pgx.Tx, kind string, sourceID, targetID ids.ID[K],
	read func(context.Context, pgx.Tx, ids.ID[K]) (T, *ids.UUID, error),
) (source, target T, err error) {
	var zero T
	if err := auth.EnsureVisible(ctx, tx, kind, sourceID.UUID); err != nil {
		return zero, zero, err
	}
	source, mergedInto, err := read(ctx, tx, sourceID)
	if err != nil {
		if mergedInto != nil && !mergedInto.IsZero() {
			return zero, zero, &AlreadyMergedError{Kind: kind, IntoID: *mergedInto}
		}
		return zero, zero, err
	}

	visible, err := auth.VisibleTo(ctx, tx, kind, targetID.UUID)
	if err != nil {
		return zero, zero, err
	}
	if !visible {
		return zero, zero, apperrors.ErrConflict
	}
	target, _, err = read(ctx, tx, targetID)
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			return zero, zero, &MergedTargetError{Kind: kind}
		}
		return zero, zero, err
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

// mergePersonSocial re-homes A's social rows onto B: a platform the
// survivor already has keeps B's handle and drops A's (same
// survivor-wins rule as the primary-slot demotions), the rest relink.
func mergePersonSocial(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.PersonID) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM person_social a
		WHERE a.person_id = $1 AND EXISTS (
		  SELECT 1 FROM person_social b
		  WHERE b.person_id = $2 AND b.platform = a.platform)`,
		sourceID, targetID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx,
		`UPDATE person_social SET person_id = $2 WHERE person_id = $1`, sourceID, targetID)
	return err
}
