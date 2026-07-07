// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
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
// Consent merges restrictively where the two records disagree: A's
// withdrawal propagates to B (with an appended proof event — a state
// change without proof would break the Art. 7(1) invariant), and where
// B already holds a row for a purpose, B's state stands — A's grant
// never overrides it. For purposes B has NO row for, A's rows travel to
// B together with their original proof chain: a merge asserts the two
// records are the same human, so a consent that human granted remains
// proven (the same carry-through the lead→person promotion does).

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
		var err error
		out, err = s.mergePersonTx(ctx, tx, sourceID, targetID)
		return err
	})
	return out, err
}

// mergePersonTx locks both ends, resolves them, relinks every
// reference, fills the survivor's gaps, retires the source, and runs
// the write shape — all inside the caller's transaction. The pair lock
// is what makes the target check hold until commit: without it a
// concurrent merge(target→elsewhere) could archive the survivor
// mid-merge, leaving relinked children pointing at a dead record.
func (s *Store) mergePersonTx(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.UUID) (crmcontracts.Person, error) {
	_, tgtLock, err := storekit.LockPair(ctx, tx, "person", sourceID, targetID)
	if err != nil {
		return crmcontracts.Person{}, err
	}
	src, tgt, err := mergePair(ctx, tx, "person", sourceID, targetID, readPersonMergeState)
	if err != nil {
		return crmcontracts.Person{}, err
	}
	counts, err := relinkPersonReferences(ctx, tx, sourceID, targetID)
	if err != nil {
		return crmcontracts.Person{}, err
	}
	p := buildSurvivorshipPatch(tgt, src)
	if !p.Empty() {
		if err := p.ApplyLocked(ctx, tx, tgtLock); err != nil {
			return crmcontracts.Person{}, fmt.Errorf("apply survivorship fill: %w", err)
		}
	}
	if err := archiveMergedAway(ctx, tx, "person", sourceID, targetID); err != nil {
		return crmcontracts.Person{}, fmt.Errorf("retire merged-away person: %w", err)
	}
	auditID, err := storekit.Audit(ctx, tx, "merge", "person", sourceID,
		map[string]any{"merged_into_id": nil},
		map[string]any{"merged_into_id": targetID, "relinked": counts, "filled": p.After()})
	if err != nil {
		return crmcontracts.Person{}, fmt.Errorf("audit person merge: %w", err)
	}
	// One event, its own verb: the context graph collapses two nodes,
	// which neither person.updated nor person.archived can say.
	if err := storekit.Emit(ctx, tx, auditID, "person.merged", "person", sourceID, map[string]any{
		"merged_from_id": sourceID,
		"merged_into_id": targetID,
		"relinked":       counts,
	}); err != nil {
		return crmcontracts.Person{}, fmt.Errorf("emit person.merged: %w", err)
	}
	out, err := readPerson(ctx, tx, targetID, storekit.LiveOnly)
	if err != nil {
		return crmcontracts.Person{}, fmt.Errorf("read surviving person: %w", err)
	}
	return out, nil
}

// buildSurvivorshipPatch folds A's values onto B fill-only: B never loses
// what it already holds; only its empty fields take A's non-empty values.
func buildSurvivorshipPatch(target, source crmcontracts.Person) *storekit.Patch {
	p := storekit.NewPatch()
	fillString(p, "first_name", target.FirstName, source.FirstName)
	fillString(p, "last_name", target.LastName, source.LastName)
	fillString(p, "title", target.Title, source.Title)
	if target.ConvertedFromLeadId == nil && source.ConvertedFromLeadId != nil {
		p.Set("converted_from_lead_id", nil, ids.UUID(*source.ConvertedFromLeadId))
	}
	if (target.Social == nil || len(*target.Social) == 0) && source.Social != nil && len(*source.Social) > 0 {
		p.Set("social", nil, storekit.JSONArg(*source.Social))
	}
	return p
}

// relinkPersonReferences re-homes everything that points at the source
// person — emails and phones (primaries demote when the survivor holds
// the slot), relationship edges, the pure link tables, consent (the
// restrictive rule), the lead promotion pointer, and the merge redirect
// chain — and returns the accounting the person.merged event carries.
func relinkPersonReferences(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.UUID) (relinkCounts, error) {
	counts := relinkCounts{}
	var err error
	if counts.Emails, err = relinkDemotingPrimary(ctx, tx, `
		UPDATE person_email a SET person_id = $2,
		  is_primary = a.is_primary AND NOT EXISTS (
		    SELECT 1 FROM person_email b
		    WHERE b.person_id = $2 AND b.email_type = a.email_type
		      AND b.is_primary AND b.archived_at IS NULL)
		WHERE a.person_id = $1 AND a.archived_at IS NULL`, sourceID, targetID); err != nil {
		return counts, fmt.Errorf("relink emails: %w", err)
	}
	if counts.Phones, err = relinkDemotingPrimary(ctx, tx, `
		UPDATE person_phone a SET person_id = $2,
		  is_primary = a.is_primary AND NOT EXISTS (
		    SELECT 1 FROM person_phone b
		    WHERE b.person_id = $2 AND b.phone_type = a.phone_type
		      AND b.is_primary AND b.archived_at IS NULL)
		WHERE a.person_id = $1 AND a.archived_at IS NULL`, sourceID, targetID); err != nil {
		return counts, fmt.Errorf("relink phones: %w", err)
	}
	if counts.Relationships, err = relinkPersonEdges(ctx, tx, sourceID, targetID); err != nil {
		return counts, fmt.Errorf("relink relationships: %w", err)
	}
	if counts.ActivityLinks, err = relinkLinkRows(ctx, tx, "person", sourceID, targetID); err != nil {
		return counts, fmt.Errorf("relink activity/list/tag rows: %w", err)
	}
	if err := mergeConsent(ctx, tx, sourceID, targetID); err != nil {
		return counts, fmt.Errorf("merge consent: %w", err)
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
func readPersonMergeState(ctx context.Context, tx pgx.Tx, id ids.UUID) (crmcontracts.Person, *ids.UUID, error) {
	p, err := readPerson(ctx, tx, id, storekit.IncludeArchived)
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
func mergePair[T any](ctx context.Context, tx pgx.Tx, kind string, sourceID, targetID ids.UUID,
	read func(context.Context, pgx.Tx, ids.UUID) (T, *ids.UUID, error)) (source, target T, err error) {
	var zero T
	if err := auth.EnsureVisible(ctx, tx, kind, sourceID); err != nil {
		return zero, zero, err
	}
	source, mergedInto, err := read(ctx, tx, sourceID)
	if err != nil {
		if mergedInto != nil && !mergedInto.IsZero() {
			return zero, zero, &AlreadyMergedError{Kind: kind, IntoID: *mergedInto}
		}
		return zero, zero, err
	}

	visible, err := auth.VisibleTo(ctx, tx, kind, targetID)
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

// mergeConsent applies the merge rule the package comment states:
// where both records hold a row, B's state stands unless A withdrew —
// a withdrawal always wins and, like every consent state change, lands
// with an appended consent_event proof row in the same statement; A's
// remaining rows (purposes B has no state for) relink to B with their
// original proof chain intact.
func mergeConsent(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.UUID) error {
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		WITH flipped AS (
		  UPDATE person_consent b SET state = 'withdrawn', captured_at = $3, source = 'merge'
		  FROM person_consent a
		  WHERE a.person_id = $1 AND b.person_id = $2
		    AND a.purpose_id = b.purpose_id
		    AND a.state = 'withdrawn' AND b.state <> 'withdrawn'
		  RETURNING b.purpose_id
		)
		INSERT INTO consent_event (workspace_id, person_id, purpose_id, new_state, source,
		                           policy_text, policy_version, captured_at, captured_by)
		SELECT NULLIF(current_setting('app.workspace_id', true), '')::uuid, $2, purpose_id, 'withdrawn', 'merge',
		       'withdrawal carried over from a merged duplicate record', 'merge', $3, $4
		FROM flipped`,
		sourceID, targetID, time.Now().UTC(), by); err != nil {
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
	_, err = tx.Exec(ctx,
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
