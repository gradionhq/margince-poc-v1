// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// MergeLead folds one open lead into another (ADR-0118/A169 §2: "merging two
// leads keeps the older record and carries provenance"). It is the ONE lead
// merge in the system — the review queue's merge disposition runs it. Which
// record survives is the reviewer's call (the queue names the winner); the
// survivor keeps what it holds and takes only what it lacks, the loser's
// timeline and consent move over, and the loser is archived with the pointer
// to where it went. A lead never merges into a person here — promotion
// (ADR-0008 §4) is the only bridge.
func (s *Store) MergeLead(ctx context.Context, sourceID, targetID ids.LeadID) (crmcontracts.Lead, error) {
	if err := auth.Require(ctx, entityLead, principal.ActionUpdate); err != nil {
		return crmcontracts.Lead{}, err
	}
	if sourceID == targetID {
		return crmcontracts.Lead{}, &MergeSelfError{}
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Lead{}, err
	}
	active, err := s.activeColumns(ctx, entityLead)
	if err != nil {
		return crmcontracts.Lead{}, err
	}
	var out crmcontracts.Lead
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = mergeLeadTx(ctx, tx, sourceID, targetID, active, by)
		return err
	})
	return out, err
}

// mergeLeadTx locks both ends, resolves them, moves the loser's references,
// fills the survivor's gaps, retires the loser and lands the write shape —
// all inside the caller's transaction, under the pair lock that keeps the
// survivor live until commit.
func mergeLeadTx(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.LeadID, active []fieldcatalog.Column, by string) (crmcontracts.Lead, error) {
	_, tgtLock, err := storekit.LockPair(ctx, tx, entityLead, sourceID.UUID, targetID.UUID)
	if err != nil {
		return crmcontracts.Lead{}, err
	}
	src, tgt, err := mergePair(ctx, tx, entityLead, sourceID, targetID, readLeadMergeState)
	if err != nil {
		return crmcontracts.Lead{}, err
	}
	if err := carryLeadActivitiesToLead(ctx, tx, sourceID, targetID); err != nil {
		return crmcontracts.Lead{}, err
	}
	if err := carryLeadConsentToLead(ctx, tx, sourceID, targetID, by); err != nil {
		return crmcontracts.Lead{}, err
	}
	if err := carryLeadMembershipsToLead(ctx, tx, sourceID, targetID); err != nil {
		return crmcontracts.Lead{}, err
	}
	if err := retireStaleCandidates(ctx, tx, sourceID); err != nil {
		return crmcontracts.Lead{}, err
	}
	// The loser retires BEFORE the survivor takes its keys: email is unique
	// among LIVE leads, so a fill that ran first would collide with the row
	// about to be archived.
	if err := archiveMergedAway(ctx, tx, entityLead, sourceID.UUID, targetID.UUID); err != nil {
		return crmcontracts.Lead{}, fmt.Errorf("retire merged-away lead: %w", err)
	}
	p := buildLeadSurvivorshipPatch(tgt, src)
	if !p.Empty() {
		if err := p.ApplyLocked(ctx, tx, tgtLock); err != nil {
			return crmcontracts.Lead{}, fmt.Errorf("apply lead survivorship fill: %w", err)
		}
	}
	auditID, err := storekit.Audit(ctx, tx, "merge", entityLead, sourceID.UUID,
		map[string]any{auditKeyMergedInto: nil},
		map[string]any{auditKeyMergedInto: targetID, auditKeyFilled: p.After()})
	if err != nil {
		return crmcontracts.Lead{}, fmt.Errorf("audit lead merge: %w", err)
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, sourceID.UUID, crmcontracts.PublicEventLeadMerged{
		MergedFromId: openapi_types.UUID(sourceID.UUID), MergedIntoId: openapi_types.UUID(targetID.UUID),
	}); err != nil {
		return crmcontracts.Lead{}, fmt.Errorf("emit lead.merged: %w", err)
	}
	out, err := readLead(ctx, tx, targetID, storekit.LiveOnly, active)
	if err != nil {
		return crmcontracts.Lead{}, fmt.Errorf("read surviving lead: %w", err)
	}
	return out, nil
}

// readLeadMergeState reads one end of a lead merge: the live lead, or — for
// an archived one — where it went, so the caller can say "already merged
// into X" rather than a bare not-found. Only an OPEN lead merges: a promoted
// or disqualified lead is archived and answers not-found here, which is the
// right refusal — its story is told by the promotion or the disqualification.
func readLeadMergeState(ctx context.Context, tx pgx.Tx, id ids.LeadID) (crmcontracts.Lead, *ids.UUID, error) {
	lead, err := readLead(ctx, tx, id, storekit.IncludeArchived, nil)
	if err != nil {
		return crmcontracts.Lead{}, nil, err
	}
	if lead.ArchivedAt == nil {
		return lead, nil, nil
	}
	var mergedInto *ids.UUID
	if err := tx.QueryRow(ctx, `SELECT merged_into_id FROM lead WHERE id = $1`, id).Scan(&mergedInto); err != nil {
		return crmcontracts.Lead{}, nil, fmt.Errorf("read lead merge pointer: %w", err)
	}
	return crmcontracts.Lead{}, mergedInto, apperrors.ErrNotFound
}

// buildLeadSurvivorshipPatch folds the loser's values onto the survivor
// fill-only: the survivor never loses what it holds. The exact keys move too
// — an address the loser had and the survivor lacks is the strongest thing
// the pair shares, and leaving it on an archived row would let a third
// capture of the same person land as a fresh lead.
func buildLeadSurvivorshipPatch(target, source crmcontracts.Lead) *storekit.Patch {
	p := storekit.NewPatch()
	fillString(p, "title", target.Title, source.Title)
	fillString(p, leadCompanyColumn, target.CompanyName, source.CompanyName)
	fillString(p, "candidate_org_key", target.CandidateOrgKey, source.CandidateOrgKey)
	fillString(p, "linkedin_url", target.LinkedinUrl, source.LinkedinUrl)
	if target.Email == nil && source.Email != nil {
		p.Set(leadEmailColumn, nil, string(*source.Email))
	}
	if target.OwnerId == nil && source.OwnerId != nil {
		p.Set(ownerIDColumn, nil, ids.UUID(*source.OwnerId))
	}
	return p
}

// carryLeadActivitiesToLead moves the loser's timeline onto the survivor.
// A row the survivor already links is dropped rather than duplicated
// (uq_activity_link would refuse it).
func carryLeadActivitiesToLead(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.LeadID) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM activity_link a
		WHERE a.lead_id = $1 AND EXISTS (
		  SELECT 1 FROM activity_link b
		  WHERE b.activity_id = a.activity_id AND b.entity_type = 'lead' AND b.lead_id = $2)`,
		sourceID, targetID); err != nil {
		return fmt.Errorf("drop already-linked lead activities: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE activity_link SET lead_id = $2 WHERE lead_id = $1`, sourceID, targetID); err != nil {
		return fmt.Errorf("carry lead activities: %w", err)
	}
	return nil
}

// carryLeadConsentToLead re-points the loser's consent onto the survivor,
// under the same rules the promotion carry keeps (carryLeadConsent):
//
//   - withdrawal wins: a withdrawn state on the loser flips the survivor's
//     grant for that purpose, with an appended consent_event as proof — a
//     merge must never turn an opt-out back into a grant;
//   - where the survivor already holds a row for a purpose its state stands
//     otherwise, and the colliding loser row is dropped;
//   - the rest re-point.
//
// The consent_event proof rows re-home WITH the state: the delivery gate
// reads the double-opt-in proof off the live lead, so a grant that moved
// while its confirmation stayed on the archived row would be a grant nobody
// can act on.
func carryLeadConsentToLead(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.LeadID, by string) error {
	if _, err := tx.Exec(ctx, `
		WITH flipped AS (
		  UPDATE person_consent b SET state = 'withdrawn', captured_at = $3, source = 'merge'
		  FROM person_consent a
		  WHERE a.lead_id = $1 AND b.lead_id = $2
		    AND a.purpose_id = b.purpose_id
		    AND a.state = 'withdrawn' AND b.state <> 'withdrawn'
		  RETURNING b.purpose_id
		)
		INSERT INTO consent_event (lead_id, purpose_id, new_state, source,
		                           policy_text, policy_version, captured_at, captured_by)
		SELECT $2, purpose_id, 'withdrawn', 'merge',
		       'withdrawal carried over from the merged-away lead', 'merge', $3, $4
		FROM flipped`,
		sourceID, targetID, time.Now().UTC(), by); err != nil {
		return fmt.Errorf("carry lead consent withdrawals: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM person_consent a
		WHERE a.lead_id = $1 AND EXISTS (
		  SELECT 1 FROM person_consent b WHERE b.lead_id = $2 AND b.purpose_id = a.purpose_id)`,
		sourceID, targetID); err != nil {
		return fmt.Errorf("drop colliding lead consent: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE person_consent SET lead_id = $2 WHERE lead_id = $1`, sourceID, targetID); err != nil {
		return fmt.Errorf("carry lead consent: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE consent_event SET lead_id = $2 WHERE lead_id = $1`, sourceID, targetID); err != nil {
		return fmt.Errorf("carry lead consent proof: %w", err)
	}
	return nil
}

// retireStaleCandidates archives every OTHER open pair naming the loser: its
// endpoint is gone, so the pair can no longer be decided, and a queue row
// that can only fail on merge is worse than none. Archived, not disposed —
// nobody judged those pairs, and the row survives as the pair-unique fact.
func retireStaleCandidates(ctx context.Context, tx pgx.Tx, loser ids.LeadID) error {
	if _, err := tx.Exec(ctx, `
		UPDATE dedupe_candidate SET archived_at = $2
		WHERE entity_type = 'lead' AND disposition = 'open' AND archived_at IS NULL
		  AND (left_lead_id = $1 OR right_lead_id = $1)`,
		loser, time.Now().UTC()); err != nil {
		return fmt.Errorf("retire stale lead candidates: %w", err)
	}
	return nil
}

// carryLeadMembershipsToLead moves list memberships and tags the survivor
// does not already have; the rest go with the archived row (§1.10 cleanup).
func carryLeadMembershipsToLead(ctx context.Context, tx pgx.Tx, sourceID, targetID ids.LeadID) error {
	for _, m := range []struct{ table, key string }{{"list_member", "list_id"}, {"taggable", "tag_id"}} {
		if _, err := tx.Exec(ctx, `
			DELETE FROM `+m.table+` a
			WHERE a.entity_type = 'lead' AND a.entity_id = $1 AND EXISTS (
			  SELECT 1 FROM `+m.table+` b
			  WHERE b.entity_type = 'lead' AND b.entity_id = $2 AND b.`+m.key+` = a.`+m.key+`)`,
			sourceID, targetID); err != nil {
			return fmt.Errorf("drop colliding lead %s rows: %w", m.table, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE `+m.table+` SET entity_id = $2 WHERE entity_type = 'lead' AND entity_id = $1`,
			sourceID, targetID); err != nil {
			return fmt.Errorf("carry lead %s rows: %w", m.table, err)
		}
	}
	return nil
}
