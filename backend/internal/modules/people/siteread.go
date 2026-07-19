// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The deep-read dossier (site_read, 0085): one row per async crawl of an
// organization's website, created queued when a human asks for the read,
// advanced by the worker (queued → running → deferred|done|partial|failed), and
// polled by the SPA. At most one read per organization is in flight
// (uq_site_read_inflight): a second click while one runs JOINS it
// instead of racing a rival crawl. The dossier itself is operational
// status, not a record fact — the facts a read produces land through
// the staged proposals it links, each carrying its own audit and event.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// SiteReadPage is one page the crawl read, classified by kind.
type SiteReadPage struct {
	URL  string `json:"url"`
	Kind string `json:"kind"`
}

// SiteReadSkip is one page the crawl saw but did not read, and why.
type SiteReadSkip struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

// SiteRead is the dossier as the SPA polls it.
type SiteRead struct {
	ID             ids.UUID
	OrganizationID *ids.OrganizationID
	TargetKind     string
	SeedURL        string
	Status         string
	StatusCode     *string
	StatusDetail   *string
	NextAttemptAt  *time.Time
	Pages          []SiteReadPage
	Skipped        []SiteReadSkip
	StoppedReason  *string
	FactCount      int
	ProposalIDs    []ids.UUID
	RequestedBy    string
	ProfileFields  []DeepReadField
	Facts          []DeepReadFact
	People         []SiteReadPerson
	Warnings       []string
	DraftVersion   int
	ProposalHash   string
	// Phase and PagesRead are the worker's live-progress hints while
	// Status is 'running' (crawling | extracting + committed page count);
	// the terminal report is the authority once Status ends.
	Phase       *string
	PagesRead   int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	ConfirmedAt *time.Time
}

// SiteReadPerson is one published person held in the operational dossier.
// It never becomes a contact or lead as part of company confirmation; compose
// stages each person separately after the anchor exists.
type SiteReadPerson struct {
	Name            string `json:"name"`
	Role            string `json:"role"`
	PublishedEmail  string `json:"published_email,omitempty"`
	LinkedinURL     string `json:"linkedin_url,omitempty"`
	EvidenceSnippet string `json:"evidence_snippet"`
	SourceURL       string `json:"source_url"`
}

// siteReadColumns is the ONE column list every dossier read scans —
// scanSiteRead pairs with it positionally.
const siteReadColumns = `id, organization_id, target_kind, seed_url, status, status_code, status_detail, next_attempt_at, pages, skipped,
	stopped_reason, fact_count, proposal_ids, requested_by, profile_fields, facts, people, warnings,
	draft_version, proposal_hash, phase, pages_read, created_at, updated_at, started_at, finished_at, confirmed_at`

// siteReadOrgKey names the audit payload's org reference once (the goconst
// pin): the same string in relationship.go is that file's column vocabulary —
// a different concept, deliberately not shared.
const siteReadOrgKey = "organization_id"

const siteReadBudgetDetail = "AI budget reached its current limit. This website read will resume automatically."

// finishedSiteReadStatuses are the terminal states a worker may report;
// anything else is a programming error caught before the row's CHECK.
var finishedSiteReadStatuses = map[string]bool{"done": true, "partial": true, "failed": true}

// siteReadStopReasons mirrors the row's stopped_reason CHECK so a bad
// worker value reads as an actionable error, not a constraint 500.
var siteReadStopReasons = map[string]bool{"budget": true, "page_cap": true, "byte_cap": true, "deadline": true}

// SiteReadEnqueue inserts the worker job through the dossier transaction.
// Compose supplies the River-backed implementation; keeping it as a callback
// preserves people-module ownership of the operational row.
type SiteReadEnqueue func(context.Context, pgx.Tx, SiteRead) error

// StartSiteRead creates the queued dossier for orgID, or JOINS the one
// already in flight — re-clicking "read the site" attaches the caller to
// the running read rather than racing a second crawl. joined reports
// which happened. Row-scoped: an org the caller cannot see is
// ErrNotFound (existence-hiding).
func (s *Store) StartSiteRead(ctx context.Context, orgID ids.OrganizationID, seedURL, requestedBy string) (SiteRead, bool, error) {
	return s.createOrJoinSiteRead(ctx, &orgID, "organization", seedURL, requestedBy, nil)
}

// StartSiteReadQueued is the production organization-enrichment start. The
// dossier and River job commit together, so no queued row can exist without
// work behind it.
func (s *Store) StartSiteReadQueued(ctx context.Context, orgID ids.OrganizationID, seedURL, requestedBy string, enqueue SiteReadEnqueue) (SiteRead, bool, error) {
	return s.createOrJoinSiteRead(ctx, &orgID, "organization", seedURL, requestedBy, enqueue)
}

// StartOnboardingSiteRead creates an unbound operational dossier. It writes no
// organization, profile field, fact, or lead before confirmation.
func (s *Store) StartOnboardingSiteRead(ctx context.Context, seedURL, requestedBy string, enqueue SiteReadEnqueue) (SiteRead, bool, error) {
	return s.createOrJoinSiteRead(ctx, nil, "onboarding", seedURL, requestedBy, enqueue)
}

func (s *Store) createOrJoinSiteRead(ctx context.Context, orgID *ids.OrganizationID, targetKind, seedURL, requestedBy string, enqueue SiteReadEnqueue) (SiteRead, bool, error) {
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		if targetKind != "onboarding" {
			return SiteRead{}, false, err
		}
		if createErr := auth.Require(ctx, "organization", principal.ActionCreate); createErr != nil {
			return SiteRead{}, false, createErr
		}
	}
	var out SiteRead
	var joined bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if orgID != nil {
			if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
				return err
			}
		}
		readID := ids.NewV7()
		// The in-flight uniqueness is arbitrated by uq_site_read_inflight
		// itself: DO NOTHING (instead of catching the violation) keeps the
		// transaction alive, so the join SELECT below sees the winning row
		// in the same tx — no second-transaction gap for it to finish in.
		inserted := tx.QueryRow(ctx, `
			INSERT INTO site_read (id, workspace_id, organization_id, target_kind, seed_url, requested_by)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT DO NOTHING
			RETURNING `+siteReadColumns,
			readID, workspaceID(ctx), orgID, targetKind, seedURL, requestedBy)
		var err error
		out, err = scanSiteRead(inserted)
		if err == nil {
			if enqueue != nil {
				if err := enqueue(ctx, tx, out); err != nil {
					return err
				}
			}
			// Audit-only: the closed catalog (events.md §5) defines no
			// site_read.* type; the facts the crawl produces are staged as
			// proposals, each emitting its own event when accepted.
			if _, err := storekit.Audit(ctx, tx, "create", "site_read", readID, nil, map[string]any{
				siteReadOrgKey: orgID, "target_kind": targetKind, "seed_url": seedURL, "requested_by": requestedBy,
			}); err != nil {
				return fmt.Errorf("audit site read start: %w", err)
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("start site read: %w", err)
		}
		joined = true
		inFlight := tx.QueryRow(ctx, `
			SELECT `+siteReadColumns+` FROM site_read
			WHERE target_kind = $1 AND organization_id IS NOT DISTINCT FROM $2
			  AND seed_url = $3 AND status IN ('queued','deferred','running')`, targetKind, orgID, seedURL)
		out, err = scanSiteRead(inFlight)
		if err != nil {
			return fmt.Errorf("join in-flight site read: %w", err)
		}
		return nil
	})
	if err != nil {
		return SiteRead{}, false, err
	}
	return out, joined, nil
}

// GetSiteRead reads one dossier, scoped to the organization the caller
// named: a read id that exists under another org — or an org the caller
// cannot see — is ErrNotFound (existence-hiding).
func (s *Store) GetSiteRead(ctx context.Context, orgID ids.OrganizationID, readID ids.UUID) (SiteRead, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return SiteRead{}, err
	}
	var out SiteRead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `
			SELECT `+siteReadColumns+` FROM site_read
			WHERE id = $1 AND organization_id = $2`, readID, orgID)
		var err error
		out, err = scanSiteRead(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get site read: %w", err)
		}
		return nil
	})
	if err != nil {
		return SiteRead{}, err
	}
	return out, nil
}

// GetOnboardingSiteRead reads an unbound dossier without requiring an anchor
// row to exist. Workspace RLS and the normal organization read/create authority
// still gate the operational draft.
func (s *Store) GetOnboardingSiteRead(ctx context.Context, readID ids.UUID) (SiteRead, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		if createErr := auth.Require(ctx, "organization", principal.ActionCreate); createErr != nil {
			return SiteRead{}, createErr
		}
	}
	var out SiteRead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT `+siteReadColumns+` FROM site_read
			WHERE id = $1 AND target_kind = 'onboarding'`, readID)
		var err error
		out, err = scanSiteRead(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get onboarding site read: %w", err)
		}
		return nil
	})
	return out, err
}

// UpdateSiteReadProgress records the worker's live position — the phase
// and how many pages have committed — on a still-running dossier, so the
// SPA's poll shows movement during the crawl and the model call instead
// of a silent 'running'. Best-effort by contract: a read that is no
// longer running is simply not updated (the terminal write won), never
// an error. No auth.Require, same rationale as BeginSiteRead.
func (s *Store) UpdateSiteReadProgress(ctx context.Context, readID ids.UUID, phase string, pagesRead int) error {
	if phase != "crawling" && phase != "extracting" {
		return fmt.Errorf("people: %q is not a site-read phase (crawling|extracting)", phase)
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE site_read SET phase = $2, pages_read = $3, updated_at = now()
			WHERE id = $1 AND status = 'running'`, readID, phase, pagesRead); err != nil {
			return fmt.Errorf("update site read progress: %w", err)
		}
		return nil
	})
}

// UpdateSiteReadDraft exposes the grounded page lanes while the worker is
// still reading. The version and hash advance together, so a client can never
// confirm an older snapshot after new findings arrive.
func (s *Store) UpdateSiteReadDraft(ctx context.Context, readID ids.UUID, facts []DeepReadFact, found []SiteReadPerson, proposalHash string) error {
	factsRaw, err := marshalSiteReadList(facts)
	if err != nil {
		return fmt.Errorf("people: progressive site-read facts: %w", err)
	}
	peopleRaw, err := marshalSiteReadList(found)
	if err != nil {
		return fmt.Errorf("people: progressive site-read people: %w", err)
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE site_read
			SET facts = $2, people = $3, proposal_hash = $4,
			    draft_version = draft_version + 1, updated_at = now()
			WHERE id = $1 AND status = 'running'`, readID, factsRaw, peopleRaw, proposalHash); err != nil {
			return fmt.Errorf("update progressive site-read draft: %w", err)
		}
		return nil
	})
}

// BeginSiteRead flips an eligible queued/deferred dossier → running as the worker picks it
// up. No auth.Require here: the worker is not a human principal — the
// human's authority was checked at StartSiteRead, and the workspace-bound
// transaction (RLS) still scopes the write to the job's tenant. The
// guarded WHERE is the CAS: a read someone else already began (or that no
// longer exists) is ErrNotFound.
func (s *Store) BeginSiteRead(ctx context.Context, readID ids.UUID, reclaimAfter time.Duration) (SiteReadClaim, error) {
	if reclaimAfter <= 0 {
		return SiteReadClaim{}, errors.New("people: site-read reclaim interval must be positive")
	}
	var claim SiteReadClaim
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// RETURNING hands the worker the CLAIMED row's own identity: the crawl
		// and the staged proposal derive from what the dossier says, never
		// from job args that could in principle diverge from it.
		err := tx.QueryRow(ctx, `
			UPDATE site_read SET status = 'running', status_code = NULL, status_detail = NULL,
				next_attempt_at = NULL, started_at = now(), updated_at = now()
			WHERE id = $1 AND (status = 'queued' OR
			  (status = 'deferred' AND next_attempt_at <= now()) OR
			  (status = 'running' AND started_at < now() - ($2 * interval '1 microsecond')))
			RETURNING organization_id, target_kind, seed_url, requested_by`, readID, reclaimAfter.Microseconds()).
			Scan(&claim.OrganizationID, &claim.TargetKind, &claim.SeedURL, &claim.RequestedBy)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("begin site read: %w", err)
		}
		return nil
	})
	if err != nil {
		return SiteReadClaim{}, err
	}
	return claim, nil
}

// DeferSiteRead returns a running dossier to its durable carrier without
// discarding progress. The guarded transition prevents a late budget result
// from overwriting a terminal write by another worker.
func (s *Store) DeferSiteRead(ctx context.Context, readID ids.UUID, nextAttemptAt time.Time) error {
	if nextAttemptAt.IsZero() {
		return errors.New("people: site-read deferral requires a retry time")
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE site_read
			SET status = 'deferred', status_code = 'budget_deferred', status_detail = $2,
				next_attempt_at = $3, phase = NULL, updated_at = now()
			WHERE id = $1 AND status = 'running'`, readID, siteReadBudgetDetail, nextAttemptAt.UTC())
		if err != nil {
			return fmt.Errorf("defer site read: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return apperrors.ErrNotFound
		}
		return nil
	})
}

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
	warnings, err := marshalSiteReadList(in.Warnings)
	if err != nil {
		return fmt.Errorf("people: site-read warnings: %w", err)
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE site_read
			SET status = $2, pages = $3, skipped = $4, stopped_reason = $5,
			    fact_count = $6, proposal_ids = $7, profile_fields = $8, facts = $9,
			    people = $10, warnings = $11, proposal_hash = $12,
			    draft_version = draft_version + 1, pages_read = $13, phase = NULL,
			    finished_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'running'`,
			readID, in.Status, pages, skipped, in.StoppedReason, in.FactCount, proposals,
			profileFields, facts, people, warnings, in.ProposalHash, len(in.Pages))
		if err != nil {
			return fmt.Errorf("finish site read: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return apperrors.ErrNotFound
		}
		return nil
	})
}

// marshalSiteReadList serializes a page/skip list for its jsonb column,
// spelling an empty crawl as [] — the column's own vocabulary — never null.
func marshalSiteReadList[T any](list []T) ([]byte, error) {
	if len(list) == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(list)
}

// scanSiteRead reads one siteReadColumns row into the dossier shape.
func scanSiteRead(row pgx.Row) (SiteRead, error) {
	var sr SiteRead
	var pagesRaw, skippedRaw, profileRaw, factsRaw, peopleRaw, warningsRaw []byte
	if err := row.Scan(&sr.ID, &sr.OrganizationID, &sr.TargetKind, &sr.SeedURL, &sr.Status,
		&sr.StatusCode, &sr.StatusDetail, &sr.NextAttemptAt, &pagesRaw, &skippedRaw,
		&sr.StoppedReason, &sr.FactCount, &sr.ProposalIDs, &sr.RequestedBy,
		&profileRaw, &factsRaw, &peopleRaw, &warningsRaw, &sr.DraftVersion, &sr.ProposalHash,
		&sr.Phase, &sr.PagesRead, &sr.CreatedAt, &sr.UpdatedAt, &sr.StartedAt, &sr.FinishedAt, &sr.ConfirmedAt); err != nil {
		return SiteRead{}, err
	}
	if err := json.Unmarshal(pagesRaw, &sr.Pages); err != nil {
		return SiteRead{}, fmt.Errorf("site read %s carries unreadable pages: %w", sr.ID, err)
	}
	if err := json.Unmarshal(skippedRaw, &sr.Skipped); err != nil {
		return SiteRead{}, fmt.Errorf("site read %s carries unreadable skips: %w", sr.ID, err)
	}
	if err := json.Unmarshal(profileRaw, &sr.ProfileFields); err != nil {
		return SiteRead{}, fmt.Errorf("site read %s carries unreadable profile fields: %w", sr.ID, err)
	}
	if err := json.Unmarshal(factsRaw, &sr.Facts); err != nil {
		return SiteRead{}, fmt.Errorf("site read %s carries unreadable facts: %w", sr.ID, err)
	}
	if err := json.Unmarshal(peopleRaw, &sr.People); err != nil {
		return SiteRead{}, fmt.Errorf("site read %s carries unreadable people: %w", sr.ID, err)
	}
	if err := json.Unmarshal(warningsRaw, &sr.Warnings); err != nil {
		return SiteRead{}, fmt.Errorf("site read %s carries unreadable warnings: %w", sr.ID, err)
	}
	return sr, nil
}
