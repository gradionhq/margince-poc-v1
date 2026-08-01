// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The per-domain organization verdict (organization_domain_disposition): what a
// mail domain is allowed to create, asked once and answered once.
//
// This file is the ledger — reads, the pending record the ensure ladder writes,
// and the retry cursor the sweep drives. The verdict itself is decided by the
// triage site read in compose; ResolveDomainTriage (domaintriageresolve.go) is
// where an answer becomes rows.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The verdicts a domain can carry. Only Pending leaves the question open.
const (
	// DomainPending — asked, unanswered; no organization exists for it yet.
	DomainPending = "pending"
	// DomainCompany — answered yes; the row names the organization it created.
	DomainCompany = "company"
	// DomainPersonal — a natural person's own domain. A person, never a company.
	DomainPersonal = "personal"
	// DomainProvider — a mailbox or hosting vendor. Its site belongs to a real
	// company that is emphatically not the sender's employer (live.fr is
	// Microsoft's), so it must not become their organization either.
	DomainProvider = "provider"
	// DomainNoSite — nothing identified a company. Whether one nonetheless
	// EXISTS depends on which path got here, and organization_id says which: a
	// site that could not be read falls to the sender's name and creates the
	// company when that name does not explain the domain, while a landing page
	// the classifier read as parked creates nothing. Both are settled, and both
	// mean the same thing for the next message — stop asking.
	DomainNoSite = "no_site"
)

// What produced a verdict.
const (
	DomainSourceSiteRead  = "site_read"
	DomainSourceHeuristic = "heuristic"
	DomainSourceHuman     = "human"
)

// The retry bound and backoff for a domain whose triage keeps failing — the
// same shape and the same reasoning as the auto-enrich sweep's cursor: a site
// that will not load must not be re-crawled on every message that arrives.
const (
	domainTriageMaxAttempts = 2
	domainTriageBackoff     = 7 * 24 * time.Hour
	// triageReadStaleAfter is when a dossier that still claims to be running
	// stops being believed. Comfortably past any real crawl — the worker's own
	// job timeout is minutes — so it only ever catches a read whose terminal
	// write never landed.
	triageReadStaleAfter = 6 * time.Hour
)

// DomainDisposition is one domain's standing verdict.
type DomainDisposition struct {
	Domain         string
	Status         string
	Source         string
	OwnerID        ids.UUID
	OrganizationID *ids.OrganizationID
	Attempts       int
}

// Settled reports whether the question is answered — anything but pending.
func (d DomainDisposition) Settled() bool { return d.Status != "" && d.Status != DomainPending }

// DueDomain is one domain the sweep should triage. The domain is all the sweep
// needs: the human who will own whatever the verdict creates is read from the
// disposition row at that point, not carried here.
type DueDomain struct {
	Domain string
}

// readDispositionTx reads a domain's verdict on the caller's transaction and
// LOCKS it, so two ensures cannot both open the same question and the verdict
// cannot be written while one is deciding what to do about it.
//
// What the lock does NOT do is order an ensure against a verdict completely: the
// ensure runs its organization dedupe BEFORE it gets here, so a resolve that
// commits in between plants its employment edges without seeing that ensure's
// still-uncommitted person. That person ends up with the company but no edge
// until their next message, which the ensure then attaches — self-healing, and
// cheaper than holding the lock across the dedupe for every captured message.
//
// Reports ok=false when the domain has never been asked about.
func readDispositionTx(ctx context.Context, tx pgx.Tx, domain string) (DomainDisposition, bool, error) {
	var d DomainDisposition
	var source *string
	var orgID *ids.UUID
	err := tx.QueryRow(ctx, `
		SELECT domain, status, source, coalesce(owner_id, '00000000-0000-0000-0000-000000000000'::uuid), organization_id, attempts
		FROM organization_domain_disposition
		WHERE domain = $1
		FOR UPDATE`, domain).Scan(&d.Domain, &d.Status, &source, &d.OwnerID, &orgID, &d.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return DomainDisposition{}, false, nil
	}
	if err != nil {
		return DomainDisposition{}, false, fmt.Errorf("people: reading the disposition of %s: %w", domain, err)
	}
	if source != nil {
		d.Source = *source
	}
	if orgID != nil {
		typed := ids.From[ids.OrganizationKind](*orgID)
		d.OrganizationID = &typed
	}
	return d, true, nil
}

// recordPendingDispositionTx opens the question for a domain nothing is yet
// known about: the person is created, the organization is not, and this row is
// what the triage read will answer.
//
// Idempotent by construction — two senders arriving on the same new domain both
// land on the one row, and neither disturbs an answer that already exists.
//
// It reports whether THIS call opened the question. Only the opener is worth a
// crawl and worth counting: a hundred messages from one unjudged domain are one
// question, and treating each as new would enqueue a hundred reads and report a
// hundred companies on the backfill that produced one.
func recordPendingDispositionTx(ctx context.Context, tx pgx.Tx, domain string, ownerID ids.UUID) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO organization_domain_disposition (workspace_id, domain, status, owner_id)
		VALUES ($1, $2, 'pending', $3)
		ON CONFLICT (workspace_id, domain) DO NOTHING`,
		workspaceID(ctx), domain, ownerID)
	if err != nil {
		return false, fmt.Errorf("people: opening the disposition question for %s: %w", domain, err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListDueDomains returns domains still owed a verdict whose next attempt is due
// and which have attempts left, oldest question first — a domain that has
// waited longest is the one a human is most likely wondering about. A domain
// with a triage read already in flight is excluded so the sweep cannot
// double-spend the daily budget on a crawl that is already running.
func (s *Store) ListDueDomains(ctx context.Context, limit int) ([]DueDomain, error) {
	var out []DueDomain
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT d.domain
			FROM organization_domain_disposition d
			WHERE d.status = 'pending'
			  -- A domain nobody is accountable for may not mint rows, so it is
			  -- not worth a crawl either (ResolveDomainTriage stamps the org's
			  -- owner from this column).
			  AND d.owner_id IS NOT NULL
			  AND d.next_attempt_at IS NOT NULL
			  AND d.next_attempt_at <= now()
			  AND d.attempts < $1
			  -- A read genuinely in flight excludes its domain, so the sweep
			  -- cannot double-spend the budget on a crawl already running. A
			  -- read STUCK in flight must not: if the write recording its
			  -- outcome failed, the row says 'running' for ever, and an
			  -- unbounded exclusion would strand the domain with no verdict and
			  -- nothing left able to give it one.
			  AND NOT EXISTS (
				SELECT 1 FROM site_read sr
				WHERE sr.target_kind = 'domain_triage'
				  AND sr.seed_url = $2 || d.domain
				  AND sr.status IN ('queued', 'deferred', 'running')
				  AND sr.updated_at > now() - make_interval(secs => $4))
			ORDER BY d.created_at
			LIMIT $3`, domainTriageMaxAttempts, TriageSeedScheme, limit, triageReadStaleAfter.Seconds())
		if err != nil {
			return fmt.Errorf("people: listing domains due for triage: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var d DueDomain
			if err := rows.Scan(&d.Domain); err != nil {
				return fmt.Errorf("people: scanning a domain due for triage: %w", err)
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MarkTriageQueued arms the retry cursor as a triage read is enqueued: one
// attempt spent, the next not due until the backoff elapses. A worker that
// dies without answering therefore costs a delay, never a hot loop.
func (s *Store) MarkTriageQueued(ctx context.Context, domain string) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return MarkTriageQueuedTx(ctx, tx, domain)
	})
}

// MarkTriageQueuedTx is MarkTriageQueued on a caller-owned transaction, for the
// trigger that enqueues the job and arms the cursor in one commit.
func MarkTriageQueuedTx(ctx context.Context, tx pgx.Tx, domain string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE organization_domain_disposition
		   SET attempts = attempts + 1,
		       last_attempt_at = now(),
		       next_attempt_at = now() + $2::interval,
		       updated_at = now()
		 WHERE domain = $1 AND status = 'pending'`,
		domain, domainTriageBackoff.String()); err != nil {
		return fmt.Errorf("people: arming the triage cursor for %s: %w", domain, err)
	}
	return nil
}

// ExhaustedDomains returns domains still pending that have used every attempt
// and are due. They must be SETTLED, not dropped: ListDueDomains stops offering
// them, so a question left open here is one no crawl will ever answer and no
// later message will re-ask — a person permanently without a company, and
// nothing on the row to say why.
func (s *Store) ExhaustedDomains(ctx context.Context, limit int) ([]DueDomain, error) {
	var out []DueDomain
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT domain FROM organization_domain_disposition
			WHERE status = 'pending'
			  AND owner_id IS NOT NULL
			  AND next_attempt_at IS NOT NULL
			  AND next_attempt_at <= now()
			  AND attempts >= $1
			ORDER BY created_at
			LIMIT $2`, domainTriageMaxAttempts, limit)
		if err != nil {
			return fmt.Errorf("people: listing domains that ran out of triage attempts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var d DueDomain
			if err := rows.Scan(&d.Domain); err != nil {
				return fmt.Errorf("people: scanning an exhausted domain: %w", err)
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PersonsOnDomain lists the live people the workspace records at a mail domain
// — the subjects of the sender-name heuristic, and the employees a company
// verdict plants edges for. Subdomain addresses count: someone at
// mail.acme.com works at the same place as someone at acme.com.
func PersonsOnDomain(ctx context.Context, tx pgx.Tx, domain string) ([]DomainPerson, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT p.full_name, split_part(pe.email, '@', 1)
		FROM person_email pe
		JOIN person p ON p.id = pe.person_id
		WHERE p.archived_at IS NULL
		  AND p.merged_into_id IS NULL
		  AND (split_part(pe.email, '@', 2) = $1
		       -- A literal suffix compare, never LIKE: the domain reaches here
		       -- from a forgeable header, and '%' in a LIKE pattern would match
		       -- every address in the workspace.
		       OR right(split_part(pe.email, '@', 2), length($1) + 1) = '.' || $1)`, domain)
	if err != nil {
		return nil, fmt.Errorf("people: listing the people on %s: %w", domain, err)
	}
	defer rows.Close()
	var out []DomainPerson
	for rows.Next() {
		var p DomainPerson
		if err := rows.Scan(&p.FullName, &p.EmailLocal); err != nil {
			return nil, fmt.Errorf("people: scanning a person on %s: %w", domain, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
