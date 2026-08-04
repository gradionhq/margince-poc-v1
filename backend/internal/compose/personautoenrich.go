// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Filling a person from what their employer's site ALREADY published.
//
// The deep read fills a published person onto a record the workspace already
// has (deepreadautoapply.go), and stages a stranger as a lead. Both happen
// during the read. A person who arrives AFTERWARDS — a rep typing a name, a
// first mail landing, an import — is never matched against what that site
// said, so the role printed beside their name on the about page goes unused
// and the workspace holds a contact it could have described.
//
// That is the bug the LinkedIn matcher was written to fix, in the same shape:
// matching only at read time means every later arrival is a match nobody
// would ever make. The answer is the same one — THE TRIGGER IS THE EVENT, NOT
// THE WRITER. person.created and person.updated reach the outbox because the
// write shape puts them there, so manual entry, capture, site read, merge and
// import all land here without any of them knowing this consumer exists, and
// a writer added tomorrow is covered on the day it emits its first event.
//
// What the site published is still on file as the staged lead proposals the
// read left behind: each carries the name, role, published email, LinkedIn
// URL and the verbatim snippet it was read from. So the match reads those
// rather than re-crawling a page that has not changed.
//
// A match resolves TWO things at once, which is why they are one pass: the
// person's empty fields get filled, and the proposal that would have asked a
// human to create a lead for somebody the CRM now holds stops being a
// question. Leaving it would spend the approval queue on a duplicate of the
// row that is already there.

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// autoEnrichActor is the provenance a fill from this pass carries. It is
// distinct from the deep read's own actor so a reader can tell a value that
// arrived with the crawl from one this pass matched onto them later.
const autoEnrichActor = "system:person_auto_enrich"

// PersonAutoEnrich fills a newly-known person from what their employer's site
// already published.
type PersonAutoEnrich struct {
	pool      *pgxpool.Pool
	people    *people.Store
	approvals *approvals.Service
	log       *slog.Logger
}

// NewPersonAutoEnrich builds the consumer over the stores it composes.
func NewPersonAutoEnrich(pool *pgxpool.Pool, store *people.Store, approvalsSvc *approvals.Service, log *slog.Logger) *PersonAutoEnrich {
	return &PersonAutoEnrich{pool: pool, people: store, approvals: approvalsSvc, log: log}
}

// HandleEvent routes one envelope. An event this consumer does not care about
// answers nil, so the group keeps flowing rather than wedging on somebody
// else's traffic.
//
// Recomputing is idempotent — the fill is fill-only-empty and the withdrawal
// reports whether the offer was still live — so the at-least-once bus costs
// nothing: a redelivered event re-runs a match that has already been made and
// changes no row.
func (g *PersonAutoEnrich) HandleEvent(ctx context.Context, env events.Envelope) error {
	if env.Entity.ID == ids.Nil || env.Entity.Type != "person" {
		return nil
	}
	switch env.Type {
	// Every event that can make a person newly matchable. An archive needs no
	// reaction: the match requires a live row, so an archived contact stops
	// being a candidate without anything being recomputed.
	case "person.created", "person.updated", "person.merged", "person.restored":
	default:
		return nil
	}
	return g.enrich(g.systemContext(ctx, env), ids.From[ids.PersonKind](env.Entity.ID))
}

// systemContext binds the workspace and the system principal this pass writes
// under. The fill is not a human's edit and must not be recorded as one, and
// the correlation id carries through so the fill traces back to the event
// that caused it.
func (g *PersonAutoEnrich) systemContext(ctx context.Context, env events.Envelope) context.Context {
	ctx = principal.WithWorkspaceID(ctx, env.WorkspaceID)
	ctx = principal.WithCorrelationID(ctx, env.Trace.CorrelationID)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: autoEnrichActor,
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
}

// enrich runs one person's pass: find their employer, read what that site
// published, and fill the person from the one entry that is unmistakably
// them.
func (g *PersonAutoEnrich) enrich(ctx context.Context, personID ids.PersonID) error {
	return database.WithWorkspaceTx(ctx, g.pool, func(tx pgx.Tx) error {
		orgID, ok, err := g.employerOf(ctx, tx, personID)
		if err != nil || !ok {
			// No employer means nothing to match against. That is the common
			// case for a fresh contact and is not a failure.
			return err
		}
		staged, err := g.stagedSitePeople(ctx, tx, orgID)
		if err != nil || len(staged) == 0 {
			return err
		}
		for _, sp := range staged {
			// ApplySitePersonFields owns the match rule and keeps it narrow:
			// an exact live email among that organization's own employees, or
			// exactly ONE employee whose name matches confidently. Zero or two
			// is not identifiable, and it declines rather than guessing.
			matched, err := g.people.ApplySitePersonFields(ctx, orgID, people.SitePersonFields{
				Name:            sp.proposal.Name,
				Role:            sp.proposal.Role,
				PublishedEmail:  sp.proposal.PublishedEmail,
				LinkedinURL:     sp.proposal.LinkedinURL,
				EvidenceSnippet: sp.proposal.EvidenceSnippet,
				SourceURL:       sp.proposal.SourceURL,
			})
			if err != nil {
				return err
			}
			if !matched {
				continue
			}
			// The proposal asked a human to create a lead for this person.
			// The person exists, so the question is answered by the world
			// rather than by the human, and the queue should not carry it.
			withdrawn, err := g.approvals.WithdrawInTx(ctx, tx, sp.approvalID,
				"the published person is already a contact in this workspace")
			if err != nil {
				return err
			}
			g.log.InfoContext(ctx, "person auto-enriched from the employer's site",
				"person", personID.String(), "organization", orgID.String(),
				"source", sp.proposal.SourceURL, "proposal_withdrawn", withdrawn)
		}
		return nil
	})
}

// employerOf resolves the person's current primary employer — the only
// company whose site may describe them. Filling a title from company X's site
// onto a person the CRM records at company Y is a conflict a human should
// see, not one a sweep settles.
func (g *PersonAutoEnrich) employerOf(ctx context.Context, tx pgx.Tx, personID ids.PersonID) (ids.OrganizationID, bool, error) {
	var orgID ids.OrganizationID
	err := tx.QueryRow(ctx, `
		SELECT organization_id FROM relationship
		WHERE person_id = $1 AND kind = 'employment' AND is_current_primary
		  AND archived_at IS NULL AND organization_id IS NOT NULL
		LIMIT 1`, personID).Scan(&orgID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ids.OrganizationID{}, false, nil
		}
		return ids.OrganizationID{}, false, err
	}
	return orgID, true, nil
}

// stagedSitePerson pairs a pending proposal with the payload it carries.
type stagedSitePerson struct {
	approvalID ids.ApprovalID
	proposal   siteLeadProposal
}

// stagedSitePeople reads what the employer's site published and nobody has
// decided yet.
//
// It reads the approval rows directly because this pass runs as a system
// principal: the module's own PendingForTarget is human-only by design, since
// it answers "what is in YOUR inbox", and this pass has no inbox. The scan is
// bounded so one organization's backlog cannot make a single person's event
// unbounded work.
//
// LIVE proposals only, and the expiry predicate is load-bearing twice over.
// Withdrawal works by expiring the row rather than by moving its status, so
// without it a redelivered event re-withdraws a proposal already withdrawn
// and writes a second audit row for one logical act. It also keeps the pass
// off stale ground: an offer whose TTL lapsed is a read nobody acted on, and
// filling a contact from it would assert a page that may have moved on.
//
// The consequence is a real bound on this pass — it can only fill from a read
// that is still on offer. A person who arrives long after their employer was
// crawled matches nothing here, and the honest answer for them is a fresh
// read rather than a stale proposal.
func (g *PersonAutoEnrich) stagedSitePeople(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) ([]stagedSitePerson, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, proposed_change FROM approval
		WHERE kind = $1 AND target_entity_type = $2 AND target_entity_id = $3
		  AND status = 'pending' AND expires_at > now()
		ORDER BY created_at DESC
		LIMIT 50`, siteLeadProposalKind, enrichTargetType, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []stagedSitePerson
	for rows.Next() {
		var id ids.ApprovalID
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var p siteLeadProposal
		if err := json.Unmarshal(raw, &p); err != nil {
			// A payload this consumer cannot read is somebody else's row
			// shape, not a reason to wedge the group.
			g.log.WarnContext(ctx, "skipping an unreadable site-lead proposal",
				"approval", id.String(), "err", err)
			continue
		}
		out = append(out, stagedSitePerson{approvalID: id, proposal: p})
	}
	return out, rows.Err()
}
