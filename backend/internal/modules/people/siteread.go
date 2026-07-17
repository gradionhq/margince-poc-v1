// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The deep-read dossier (site_read, 0085): one row per async crawl of an
// organization's website, created queued when a human asks for the read,
// advanced by the worker (queued → running → done|partial|failed), and
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
	OrganizationID ids.OrganizationID
	SeedURL        string
	Status         string
	Pages          []SiteReadPage
	Skipped        []SiteReadSkip
	StoppedReason  *string
	FactCount      int
	ProposalIDs    []ids.UUID
	RequestedBy    string
	CreatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
}

// siteReadColumns is the ONE column list every dossier read scans —
// scanSiteRead pairs with it positionally.
const siteReadColumns = `id, organization_id, seed_url, status, pages, skipped,
	stopped_reason, fact_count, proposal_ids, requested_by, created_at, started_at, finished_at`

// siteReadOrgKey names the audit payload's org reference once (the goconst
// pin): the same string in relationship.go is that file's column vocabulary —
// a different concept, deliberately not shared.
const siteReadOrgKey = "organization_id"

// finishedSiteReadStatuses are the terminal states a worker may report;
// anything else is a programming error caught before the row's CHECK.
var finishedSiteReadStatuses = map[string]bool{"done": true, "partial": true, "failed": true}

// siteReadStopReasons mirrors the row's stopped_reason CHECK so a bad
// worker value reads as an actionable error, not a constraint 500.
var siteReadStopReasons = map[string]bool{"budget": true, "page_cap": true, "byte_cap": true, "deadline": true}

// StartSiteRead creates the queued dossier for orgID, or JOINS the one
// already in flight — re-clicking "read the site" attaches the caller to
// the running read rather than racing a second crawl. joined reports
// which happened. Row-scoped: an org the caller cannot see is
// ErrNotFound (existence-hiding).
func (s *Store) StartSiteRead(ctx context.Context, orgID ids.OrganizationID, seedURL, requestedBy string) (SiteRead, bool, error) {
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return SiteRead{}, false, err
	}
	var out SiteRead
	var joined bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
			return err
		}
		readID := ids.NewV7()
		// The in-flight uniqueness is arbitrated by uq_site_read_inflight
		// itself: DO NOTHING (instead of catching the violation) keeps the
		// transaction alive, so the join SELECT below sees the winning row
		// in the same tx — no second-transaction gap for it to finish in.
		inserted := tx.QueryRow(ctx, `
			INSERT INTO site_read (id, workspace_id, organization_id, seed_url, requested_by)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (workspace_id, organization_id) WHERE status IN ('queued','running') DO NOTHING
			RETURNING `+siteReadColumns,
			readID, workspaceID(ctx), orgID, seedURL, requestedBy)
		var err error
		out, err = scanSiteRead(inserted)
		if err == nil {
			// Audit-only: the closed catalog (events.md §5) defines no
			// site_read.* type; the facts the crawl produces are staged as
			// proposals, each emitting its own event when accepted.
			if _, err := storekit.Audit(ctx, tx, "create", "site_read", readID, nil, map[string]any{
				siteReadOrgKey: orgID, "seed_url": seedURL, "requested_by": requestedBy,
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
			WHERE organization_id = $1 AND status IN ('queued','running')`, orgID)
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

// BeginSiteRead flips the dossier queued → running as the worker picks it
// up. No auth.Require here: the worker is not a human principal — the
// human's authority was checked at StartSiteRead, and the workspace-bound
// transaction (RLS) still scopes the write to the job's tenant. The
// guarded WHERE is the CAS: a read someone else already began (or that no
// longer exists) is ErrNotFound.
func (s *Store) BeginSiteRead(ctx context.Context, readID ids.UUID) (SiteReadClaim, error) {
	var claim SiteReadClaim
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// RETURNING hands the worker the CLAIMED row's own identity: the crawl
		// and the staged proposal derive from what the dossier says, never
		// from job args that could in principle diverge from it.
		err := tx.QueryRow(ctx, `
			UPDATE site_read SET status = 'running', started_at = now()
			WHERE id = $1 AND status = 'queued'
			RETURNING organization_id, seed_url, requested_by`, readID).
			Scan(&claim.OrganizationID, &claim.SeedURL, &claim.RequestedBy)
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

// SiteReadClaim is what BeginSiteRead's CAS hands the worker: the claimed
// dossier's own identity, so the crawl derives from the row, not the job.
type SiteReadClaim struct {
	OrganizationID ids.UUID
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
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE site_read
			SET status = $2, pages = $3, skipped = $4, stopped_reason = $5,
			    fact_count = $6, proposal_ids = $7, finished_at = now()
			WHERE id = $1 AND status = 'running'`,
			readID, in.Status, pages, skipped, in.StoppedReason, in.FactCount, proposals)
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
	var pagesRaw, skippedRaw []byte
	if err := row.Scan(&sr.ID, &sr.OrganizationID, &sr.SeedURL, &sr.Status, &pagesRaw, &skippedRaw,
		&sr.StoppedReason, &sr.FactCount, &sr.ProposalIDs, &sr.RequestedBy,
		&sr.CreatedAt, &sr.StartedAt, &sr.FinishedAt); err != nil {
		return SiteRead{}, err
	}
	if err := json.Unmarshal(pagesRaw, &sr.Pages); err != nil {
		return SiteRead{}, fmt.Errorf("site read %s carries unreadable pages: %w", sr.ID, err)
	}
	if err := json.Unmarshal(skippedRaw, &sr.Skipped); err != nil {
		return SiteRead{}, fmt.Errorf("site read %s carries unreadable skips: %w", sr.ID, err)
	}
	return sr, nil
}
