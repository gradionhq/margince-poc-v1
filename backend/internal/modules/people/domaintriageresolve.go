// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Where a triage verdict becomes rows. The triage read decides what a domain
// is; this settles the ledger and, for a company, creates the organization the
// capture path deliberately did not create earlier — named from the dossier
// rather than from the raw domain label, with an employment edge for every
// person who has accumulated on that domain while the question was open.
//
// One transaction: the verdict, the organization, its domain, the edges, the
// dossier's findings and the dossier binding all commit together or not at all.
// A ledger row reading 'company' beside no organization would be a lie the
// ensure ladder then acts on.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ResolveDomainTriageInput is one answered triage: the verdict, what produced
// it, and — for a company — the dossier to name the organization from and fill
// it with.
type ResolveDomainTriageInput struct {
	Domain   string
	Status   string
	Source   string
	Evidence string
	ReadID   ids.UUID

	// DossierName is the company name the site stated. Empty falls back to the
	// domain's registrable label, which is what the pre-triage path always
	// used — a worse name, but never a fabricated one.
	DossierName string
	SeedURL     string
	Fields      []DeepReadField
	Facts       []DeepReadFact
}

// ResolveDomainTriageResult reports what the verdict actually did.
type ResolveDomainTriageResult struct {
	OrganizationID *ids.OrganizationID
	OrgCreated     bool
	EdgesPlanted   int
}

// ResolveDomainTriage settles a domain's verdict. A non-company answer writes
// the ledger and stops; a company answer creates or adopts the organization and
// wires everything the deferred ensures could not.
//
// Idempotent on replay: the dedupe lands a re-run on the organization the first
// run created, the edge insert is conflict-free, and the field apply fills only
// what is still empty. A worker that dies mid-verdict and retries therefore
// converges rather than duplicating.
func (s *Store) ResolveDomainTriage(ctx context.Context, in ResolveDomainTriageInput) (ResolveDomainTriageResult, error) {
	var res ResolveDomainTriageResult
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		res, err = s.resolveDomainTriageTx(ctx, tx, in)
		return err
	})
	if err != nil {
		return ResolveDomainTriageResult{}, err
	}
	return res, nil
}

func (s *Store) resolveDomainTriageTx(ctx context.Context, tx pgx.Tx, in ResolveDomainTriageInput) (ResolveDomainTriageResult, error) {
	// The lock every concurrent ensure on this domain waits behind, taken
	// before anything is decided so no ensure can slip between the read and
	// the write and conclude the question is still open.
	prior, known, err := readDispositionTx(ctx, tx, in.Domain)
	if err != nil {
		return ResolveDomainTriageResult{}, err
	}
	if !known {
		return ResolveDomainTriageResult{}, fmt.Errorf("people: %s has no open disposition to resolve", in.Domain)
	}
	if in.Status != DomainCompany {
		return ResolveDomainTriageResult{}, settleDisposition(ctx, tx, in, nil)
	}

	res, err := s.adoptOrCreateTriagedOrg(ctx, tx, in, prior)
	if err != nil {
		return ResolveDomainTriageResult{}, err
	}
	if res.EdgesPlanted, err = plantDomainEmployment(ctx, tx, in.Domain, *res.OrganizationID); err != nil {
		return ResolveDomainTriageResult{}, err
	}
	if len(in.Fields) > 0 || len(in.Facts) > 0 {
		if err := s.ApplyDeepReadTx(ctx, tx, DeepReadProposal{
			OrganizationID: *res.OrganizationID,
			SourceURL:      in.SeedURL,
			SiteReadID:     in.ReadID,
			Fields:         in.Fields,
			Facts:          in.Facts,
		}); err != nil {
			return ResolveDomainTriageResult{}, err
		}
	}
	if err := bindTriageDossier(ctx, tx, in.ReadID, *res.OrganizationID); err != nil {
		return ResolveDomainTriageResult{}, err
	}
	return res, settleDisposition(ctx, tx, in, res.OrganizationID)
}

// adoptOrCreateTriagedOrg returns the organization the verdict belongs to. It
// looks for an existing one FIRST: a human may have typed the company in while
// the crawl ran, and a second row for the same domain would be exactly the
// duplicate the dedupe chokepoint exists to prevent.
func (s *Store) adoptOrCreateTriagedOrg(ctx context.Context, tx pgx.Tx, in ResolveDomainTriageInput, prior DomainDisposition) (ResolveDomainTriageResult, error) {
	if err := auth.Require(ctx, entityOrganization, principal.ActionCreate); err != nil {
		return ResolveDomainTriageResult{}, err
	}
	match, err := DedupeOrganization(ctx, tx, OrganizationCandidate{Domains: []string{in.Domain}})
	if err != nil {
		return ResolveDomainTriageResult{}, err
	}
	if match.Decision == DecisionExactCollision {
		return ResolveDomainTriageResult{OrganizationID: &match.OrganizationID}, nil
	}

	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return ResolveDomainTriageResult{}, err
	}
	// The site stated a name, so this organization is born with the name a
	// human would have typed rather than a title-cased domain label — and with
	// the provenance to say so, which keeps a later dossier from overwriting it.
	displayName, nameSource := in.DossierName, nameSourceDossier
	if displayName == "" {
		displayName, nameSource = DisplayNameFromDomain(in.Domain), nameSourceDomain
	}
	if displayName == "" {
		displayName, nameSource = in.Domain, nameSourceDomain
	}

	wsID := workspaceID(ctx)
	orgID := ids.New[ids.OrganizationKind]()
	if _, err := tx.Exec(ctx, `
		INSERT INTO organization (id, workspace_id, display_name, name_source, owner_id, source, captured_by, visibility)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'owner')`,
		orgID, wsID, displayName, nameSource, prior.OwnerID, domainTriageSource(in.Domain), by); err != nil {
		return ResolveDomainTriageResult{}, fmt.Errorf("people: creating the organization triage confirmed for %s: %w", in.Domain, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO organization_domain (workspace_id, organization_id, domain, is_primary, source, captured_by)
		VALUES ($1, $2, lower($3), true, $4, $5)`,
		wsID, orgID, in.Domain, domainTriageSource(in.Domain), by); err != nil {
		return ResolveDomainTriageResult{}, fmt.Errorf("people: recording the domain of the organization for %s: %w", in.Domain, err)
	}
	auditID, err := storekit.Audit(ctx, tx, "create", entityOrganization, orgID.UUID, nil, map[string]any{
		fieldDisplayName: displayName, auditKeyNameSource: nameSource, auditKeyDomain: in.Domain,
	})
	if err != nil {
		return ResolveDomainTriageResult{}, err
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, orgID.UUID,
		crmcontracts.PublicEventOrganizationCreated{DisplayName: &displayName}); err != nil {
		return ResolveDomainTriageResult{}, err
	}
	return ResolveDomainTriageResult{OrganizationID: &orgID, OrgCreated: true}, nil
}

// plantDomainEmployment gives every live person on the domain their employment
// edge at once. They accumulated while the question was open — each ensure
// created the person and deliberately left the company undecided — so this is
// where the whole backlog is wired, not only the sender who happened to trigger
// the verdict.
//
// It never reassigns: someone whose current employer a human already recorded
// keeps it, exactly as the capture ensure never overrides one.
func plantDomainEmployment(ctx context.Context, tx pgx.Tx, domain string, orgID ids.OrganizationID) (int, error) {
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO relationship (workspace_id, kind, person_id, organization_id, is_current_primary, source, captured_by)
		SELECT $1, 'employment', p.id, $2, true, $3, $4
		FROM person p
		WHERE p.archived_at IS NULL
		  AND p.merged_into_id IS NULL
		  AND EXISTS (
			SELECT 1 FROM person_email pe
			WHERE pe.person_id = p.id
			  AND (split_part(pe.email, '@', 2) = $5
			       OR split_part(pe.email, '@', 2) LIKE '%.' || $5))
		  AND NOT EXISTS (
			SELECT 1 FROM relationship r
			WHERE r.kind = 'employment' AND r.person_id = p.id
			  AND r.is_current_primary AND r.archived_at IS NULL)
		ON CONFLICT DO NOTHING`,
		workspaceID(ctx), orgID, domainTriageSource(domain), by, domain)
	if err != nil {
		return 0, fmt.Errorf("people: planting the employment edges for %s: %w", domain, err)
	}
	return int(tag.RowsAffected()), nil
}

// bindTriageDossier attaches the triage read to the organization it produced.
// confirmed_at is what the row's target-shape CHECK requires alongside the
// organization, and it is honest here: the verdict IS the confirmation.
func bindTriageDossier(ctx context.Context, tx pgx.Tx, readID ids.UUID, orgID ids.OrganizationID) error {
	if _, err := tx.Exec(ctx, `
		UPDATE site_read
		   SET organization_id = $2, confirmed_at = now(), updated_at = now()
		 WHERE id = $1 AND target_kind = $3 AND organization_id IS NULL`,
		readID, orgID, TargetKindDomainTriage); err != nil {
		return fmt.Errorf("people: binding the triage dossier to its organization: %w", err)
	}
	return nil
}

// settleDisposition writes the answer and closes the retry cursor, so the
// domain drops out of the sweep's due scan for good.
func settleDisposition(ctx context.Context, tx pgx.Tx, in ResolveDomainTriageInput, orgID *ids.OrganizationID) error {
	var readID *ids.UUID
	if !in.ReadID.IsZero() {
		readID = &in.ReadID
	}
	if _, err := tx.Exec(ctx, `
		UPDATE organization_domain_disposition
		   SET status = $2, source = $3, evidence = NULLIF($4, ''),
		       organization_id = $5, site_read_id = $6,
		       next_attempt_at = NULL, updated_at = now()
		 WHERE domain = $1`,
		in.Domain, in.Status, in.Source, in.Evidence, orgID, readID); err != nil {
		return fmt.Errorf("people: settling the disposition of %s: %w", in.Domain, err)
	}
	return nil
}

// The audit-payload keys a triage-created organization carries. auditKeyDomain
// is deliberately its own constant and not nameSourceDomain: one is a payload
// field name, the other a provenance value, and they collide only by spelling.
const (
	auditKeyNameSource = "name_source"
	auditKeyDomain     = "domain"
)

// domainTriageSource is the provenance string rows created by a verdict carry,
// naming the domain whose triage produced them.
func domainTriageSource(domain string) string { return "domain_triage:" + domain }
