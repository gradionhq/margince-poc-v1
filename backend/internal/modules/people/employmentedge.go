// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Attaching a captured person to a company: whether the company is still one
// records may be attached to, and the edge itself.
//
// Both halves are here because both are about the same rule — capture SUGGESTS
// an employer and never reassigns one — and because the ensure ladder reaches
// them from two places now: the ordinary attach, and the re-look a capture does
// after losing a race with a triage verdict.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// attachToSettledVerdict handles a domain answered WHILE this ensure was
// running. Taking the disposition lock is what ordered the two, and the dedupe
// that said "no organization" ran before it — so a verdict that committed in
// between created a company this path has not seen. Attaching here is the
// difference between the person getting their employer now and waiting for
// their next message.
//
// A refusal attaches nothing, and neither does a company that has since been
// archived: the ledger names an organization, it does not promise one is still
// there to join.
func attachToSettledVerdict(ctx context.Context, tx pgx.Tx, in EnsureCounterpartyInput, prior DomainDisposition, res *EnsureCounterpartyResult) error {
	if prior.OrganizationID == nil {
		return nil
	}
	live, err := organizationIsLive(ctx, tx, *prior.OrganizationID)
	if err != nil || !live {
		return err
	}
	res.OrganizationID = prior.OrganizationID
	return plantEmploymentEdge(ctx, tx, in, res.PersonID, *prior.OrganizationID)
}

// organizationIsLive reports whether an organization is still one records may be
// attached to — neither archived nor merged away — locking the row so the answer
// cannot change under the insert that follows it.
func organizationIsLive(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (bool, error) {
	var live bool
	err := tx.QueryRow(ctx, `
		SELECT archived_at IS NULL AND merged_into_id IS NULL
		  FROM organization WHERE id = $1 FOR UPDATE`, orgID).Scan(&live)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("people: checking whether organization %s is still live: %w", orgID, err)
	}
	return live, nil
}

// plantEmploymentEdge attaches one person to one company, and only when they
// have no current primary employer: capture SUGGESTS an employer, it never
// reassigns somebody's. The current-primary partial unique is the structural
// guard; the NOT EXISTS keeps a concurrent race from surfacing as a 500.
func plantEmploymentEdge(ctx context.Context, tx pgx.Tx, in EnsureCounterpartyInput, personID ids.PersonID, orgID ids.OrganizationID) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO relationship (workspace_id, kind, person_id, organization_id, is_current_primary, source, captured_by)
		SELECT $1, 'employment', $2, $3, true, $4, $5
		WHERE NOT EXISTS (
			SELECT 1 FROM relationship
			WHERE kind = 'employment' AND person_id = $2 AND is_current_primary AND archived_at IS NULL)
		ON CONFLICT DO NOTHING`,
		workspaceID(ctx), personID, orgID, in.Source, in.CapturedBy); err != nil {
		return fmt.Errorf("people: insert employment edge: %w", err)
	}
	return nil
}

// adoptDispositionForOrg settles a domain onto the organization that already
// exists for it. It is the one thing that reverses a triage refusal: a wrong
// `personal` verdict is otherwise permanent, and the only correction available
// to a human is to create the company themselves — which this makes stick.
//
// A no-op for a domain nobody ever asked about, so the ordinary case of mail
// arriving at a long-known company writes nothing.
func adoptDispositionForOrg(ctx context.Context, tx pgx.Tx, domain string, orgID ids.OrganizationID) error {
	if _, err := tx.Exec(ctx, `
		UPDATE organization_domain_disposition
		   SET status = $2, source = $3, organization_id = $4,
		       evidence = 'a human put a company on this domain',
		       next_attempt_at = NULL, updated_at = now()
		 WHERE domain = $1
		   AND (status <> $2 OR organization_id IS DISTINCT FROM $4)
		   -- Only a REFUSAL is a human's to overturn. A no_site row already
		   -- carrying the organization triage itself created is not: claiming
		   -- that as "a human put a company on this domain" would rewrite the
		   -- provenance of a machine decision nobody overruled, and the
		   -- evidence field exists to say why a company was refused.
		   AND NOT (source = $5 AND organization_id IS NOT DISTINCT FROM $4)`,
		domain, DomainCompany, DomainSourceHuman, orgID, DomainSourceHeuristic); err != nil {
		return fmt.Errorf("people: recording that a human settled %s: %w", domain, err)
	}
	return nil
}

// deferOrgToTriage handles a domain with no organization behind it yet: decide
// whether the question is even worth asking, and if it is, open it.
//
// Nothing is created here on purpose. The person is already committed and the
// message is already on their timeline; what is withheld is a company row that
// nothing yet justifies. ADR-0063's create-on-sight is what manufactured
// "Kestner" from a man's own domain, and no later evidence removed it.
func (s *Store) deferOrgToTriage(ctx context.Context, tx pgx.Tx, in EnsureCounterpartyInput, base string, res *EnsureCounterpartyResult) error {
	// Consumer mail is answered by the domain itself and needs no crawl to
	// settle. The sink's tier ladder usually catches it first; this repeats the
	// check because the verdict engine and the review-queue accept reach this
	// same chokepoint without passing through that ladder.
	consumerMail, err := s.consumerMailMatcher(ctx, tx)
	if err != nil {
		return err
	}
	if consumerMail.IsConsumer(base) {
		return nil
	}
	prior, known, err := readDispositionTx(ctx, tx, base)
	if err != nil {
		return err
	}
	if known && prior.Settled() {
		return attachToSettledVerdict(ctx, tx, in, prior, res)
	}
	if known {
		// The question is open and somebody already asked it — the crawl is
		// coming, or the sweep will re-ask. Nothing further from this message.
		return nil
	}
	opened, err := recordPendingDispositionTx(ctx, tx, base, in.OwnerID)
	if err != nil {
		return err
	}
	res.TriagePending, res.TriageDomain = opened, base
	return nil
}
