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
	"errors"
	"fmt"
	"time"

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
	// ClaimedAt is the lease this attempt holds: the started_at its own CAS
	// stamped, which is the value the reclaim predicate reads to decide the
	// read is abandoned. Every claim stamps a fresh one and a reclaim can only
	// happen once the previous lease has lapsed, so the lease is per-attempt
	// identity — how a write reserved for the CURRENT holder tells an attempt
	// that still holds the read from one whose read was handed on.
	ClaimedAt time.Time
}

// FinishSiteReadInput is the worker's completed crawl report.
type FinishSiteReadInput struct {
	Status string // done | partial | failed | cancelled
	// StatusCode and StatusDetail diagnose a FAILED read: the closed code an
	// operator groups by, and the one sentence they read. Both are required
	// when Status is "failed" and forbidden otherwise — a read that worked has
	// nothing to diagnose, and a failure with neither is the state this pair
	// exists to end.
	//
	// Distinct from StoppedReason, whose vocabulary is only budget/page_cap/
	// byte_cap/deadline: that says why a crawl stopped EARLY having read
	// pages, which is not why one failed to read any.
	StatusCode   string
	StatusDetail string
	// NextAttemptAt schedules another try. Set for causes that commonly clear
	// on their own — bot protection, a 5xx, a timeout — and nil for the ones
	// that will not, so a domain is not re-crawled forever over a 404.
	NextAttemptAt *time.Time
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

// The failure codes, named so callers cite the vocabulary instead of repeating
// its spellings. Every one appears in the CHECK migration 0221 installs.
const (
	SiteReadFailureBotBlocked   = "bot_blocked"
	SiteReadFailureServerError  = "http_server_error"
	SiteReadFailureTimeout      = "timeout"
	SiteReadFailureClientError  = "http_client_error"
	SiteReadFailureDNS          = "dns"
	SiteReadFailureTLS          = "tls"
	SiteReadFailureRobots       = "robots_disallowed"
	SiteReadFailureUnreadable   = "unreadable"
	SiteReadFailureInternal     = "internal"
	SiteReadFailureStaleReclaim = "stale_reclaim"
)

// SiteReadFailureCodes is the closed vocabulary a failed read is diagnosed
// with, and the retry policy that goes with each. True means the cause commonly
// clears on its own, so another attempt is worth scheduling.
//
// It mirrors the CHECK in migration 0221 deliberately: the database is the
// authority, and this map is what lets a wrong value fail as a named Go error
// naming the field, rather than as a constraint violation from three layers
// down that no operator can act on.
var SiteReadFailureCodes = map[string]bool{
	SiteReadFailureBotBlocked:  true,  // 403/429 from an edge or bot protection
	SiteReadFailureServerError: true,  // 5xx — the site's own fault, usually transient
	SiteReadFailureTimeout:     true,  // the site did not answer in time
	SiteReadFailureClientError: false, // 404 and friends: the page is simply not there
	SiteReadFailureDNS:         false, // the name does not resolve
	SiteReadFailureTLS:         false, // the certificate does not verify
	SiteReadFailureRobots:      false, // the site's own answer, not a failure to retry
	SiteReadFailureUnreadable:  false, // fetched, but nothing readable came back
	SiteReadFailureInternal:    false, // our bug, not the site's — fix it, don't retry
	// stale_reclaim is written by the triage sweep (RetireStaleTriageRead), not
	// by a crawl: a read that stopped reporting is retired so the DOMAIN can be
	// asked again. The domain's own disposition carries that retry, so the
	// dossier itself needs none.
	SiteReadFailureStaleReclaim: false,
}

// validateSiteReadOutcome enforces the shape the outcome CHECK requires, at the
// boundary where the caller can still be told which field is wrong.
func validateSiteReadOutcome(in FinishSiteReadInput) error {
	if in.Status != siteReadStatusFailed {
		if in.StatusCode != "" || in.StatusDetail != "" || in.NextAttemptAt != nil {
			return fmt.Errorf(
				"people: a %s site read carries no diagnosis (status_code/status_detail/next_attempt_at are for a failure)",
				in.Status)
		}
		return nil
	}
	if _, known := SiteReadFailureCodes[in.StatusCode]; !known {
		return fmt.Errorf("people: %q is not a site-read failure code", in.StatusCode)
	}
	if in.StatusDetail == "" {
		return errors.New("people: a failed site read needs a status_detail a human can act on")
	}
	return nil
}

// siteReadStatusFailed is the one terminal status that carries a diagnosis.
const siteReadStatusFailed = "failed"

// FinishSiteRead records the crawl's outcome in one guarded UPDATE from
// running to a terminal status. No auth.Require, same as BeginSiteRead:
// the worker runs under the job's workspace context, not a human
// principal — the gate ran at StartSiteRead. A read that is not running
// (already finished, or never begun) is ErrNotFound.
func (s *Store) FinishSiteRead(ctx context.Context, readID ids.UUID, in FinishSiteReadInput) error {
	if !finishedSiteReadStatuses[in.Status] {
		return fmt.Errorf("people: %q is not a terminal site-read status (done|partial|failed|cancelled)", in.Status)
	}
	if in.StoppedReason != nil && !siteReadStopReasons[*in.StoppedReason] {
		return fmt.Errorf("people: %q is not a site-read stop reason (budget|page_cap|byte_cap|deadline)", *in.StoppedReason)
	}
	if err := validateSiteReadOutcome(in); err != nil {
		return err
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
			    status_code = NULLIF($16, ''), status_detail = NULLIF($17, ''),
			    next_attempt_at = $18,
			    draft_version = draft_version + 1, pages_read = $13, phase = NULL,
			    first_grounded_at = CASE WHEN $14 THEN COALESCE(first_grounded_at, now()) ELSE first_grounded_at END,
			    finished_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'running'`,
			readID, in.Status, pages, skipped, in.StoppedReason, in.FactCount, proposals,
			profileFields, facts, people, warnings, in.ProposalHash, len(in.Pages), grounded, entities,
			in.StatusCode, in.StatusDetail, in.NextAttemptAt)
		if err != nil {
			return fmt.Errorf("finish site read: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return apperrors.ErrNotFound
		}
		return nil
	})
}
