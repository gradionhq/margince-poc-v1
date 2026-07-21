// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The dossier's claim-and-close half: the worker takes a queued read with
// BeginSiteRead (siteread.go), then reports its outcome here in ONE guarded
// UPDATE. Split from the dossier's lifecycle for size, and along the seam
// that already exists — everything in this file is the terminal write and
// the report it records.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// SiteReadClaim is what BeginSiteRead's CAS hands the worker: the claimed
// dossier's own identity, so the crawl derives from the row, not the job.
type SiteReadClaim struct {
	OrganizationID *ids.UUID
	TargetKind     string
	SeedURL        string
	RequestedBy    string
}

// FinishSiteReadInput is the worker's completed crawl report.
type FinishSiteReadInput struct {
	Status        string // done | partial | failed
	Pages         []SiteReadPage
	Skipped       []SiteReadSkip
	StoppedReason *string
	FactCount     int
	ProposalIDs   []ids.UUID
	ProfileFields []DeepReadField
	Facts         []DeepReadFact
	People        []SiteReadPerson
	LegalEntities []SiteReadLegalEntity
	Warnings      []string
	ProposalHash  string
}

// FinishSiteRead records the crawl's outcome in one guarded UPDATE from
// running to a terminal status. No auth.Require, same as BeginSiteRead:
// the worker runs under the job's workspace context, not a human
// principal — the gate ran at StartSiteRead. A read that is not running
// (already finished, or never begun) is ErrNotFound.
func (s *Store) FinishSiteRead(ctx context.Context, readID ids.UUID, in FinishSiteReadInput) error {
	if !finishedSiteReadStatuses[in.Status] {
		return fmt.Errorf("people: %q is not a terminal site-read status (done|partial|failed)", in.Status)
	}
	if in.StoppedReason != nil && !siteReadStopReasons[*in.StoppedReason] {
		return fmt.Errorf("people: %q is not a site-read stop reason (budget|page_cap|byte_cap|deadline)", *in.StoppedReason)
	}
	pages, err := marshalSiteReadList(in.Pages)
	if err != nil {
		return fmt.Errorf("people: site-read pages: %w", err)
	}
	skipped, err := marshalSiteReadList(in.Skipped)
	if err != nil {
		return fmt.Errorf("people: site-read skips: %w", err)
	}
	proposals := in.ProposalIDs
	if proposals == nil {
		proposals = []ids.UUID{} // the column is NOT NULL: no proposals is the empty set
	}
	profileFields, err := marshalSiteReadList(in.ProfileFields)
	if err != nil {
		return fmt.Errorf("people: site-read profile fields: %w", err)
	}
	facts, err := marshalSiteReadList(in.Facts)
	if err != nil {
		return fmt.Errorf("people: site-read facts: %w", err)
	}
	people, err := marshalSiteReadList(in.People)
	if err != nil {
		return fmt.Errorf("people: site-read people: %w", err)
	}
	entities, err := marshalSiteReadList(in.LegalEntities)
	if err != nil {
		return fmt.Errorf("people: site-read legal entities: %w", err)
	}
	warnings, err := marshalSiteReadList(in.Warnings)
	if err != nil {
		return fmt.Errorf("people: site-read warnings: %w", err)
	}
	grounded := len(in.ProfileFields) > 0 || len(in.Facts) > 0
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE site_read
			SET status = $2, pages = $3, skipped = $4, stopped_reason = $5,
			    fact_count = $6, proposal_ids = $7, profile_fields = $8, facts = $9,
			    people = $10, warnings = $11, proposal_hash = $12,
			    legal_entities = $15,
			    draft_version = draft_version + 1, pages_read = $13, phase = NULL,
			    first_grounded_at = CASE WHEN $14 THEN COALESCE(first_grounded_at, now()) ELSE first_grounded_at END,
			    finished_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'running'`,
			readID, in.Status, pages, skipped, in.StoppedReason, in.FactCount, proposals,
			profileFields, facts, people, warnings, in.ProposalHash, len(in.Pages), grounded, entities)
		if err != nil {
			return fmt.Errorf("finish site read: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return apperrors.ErrNotFound
		}
		return nil
	})
}
