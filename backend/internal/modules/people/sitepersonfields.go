// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Auto-filling a site person onto a person the workspace ALREADY has
// (ADR-0072/A118 phase 4B).
//
// A deep read of a company's team page publishes people. A stranger among them
// stages as a lead and stays staged (ADR-0008, NEVER-8) — that boundary does not
// move. But when the published person is unmistakably someone the workspace
// already records at that company, staging a lead offers a human a duplicate of
// a record they already have, and the role the site prints next to their name
// goes unused.
//
// "Unmistakably" is deliberately narrow, and it is the whole safety argument:
//
//   - an exact live email match among that organization's own employees, or
//   - exactly ONE employee of that organization whose name matches confidently.
//
// Zero matches, or more than one, means the site person is not identifiable and
// the lead stages exactly as before. The scope is the organization's employees
// rather than the workspace, because the site is claiming this person works
// THERE: filling a title from company X's site onto a person the CRM records at
// company Y is a conflict a human should see, not one a sweep should settle.
//
// Everything written is fill-only-empty and evidence-backed, so a human's answer
// is structurally untouchable and a re-read applies nothing twice.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// siteFieldSource is the DM-CONV-11 channel for site-read person fields.
const siteFieldSource = "site_read"

// siteConfidentNameMatch is how close a published name must be to an employee's
// before the two are treated as the same person. Above the dedupe REVIEW
// threshold on purpose: 0.72 is where a human is asked to compare two records,
// and this path asks nobody.
const siteConfidentNameMatch = 0.92

// SitePersonFields is one published person as the site printed them, and the
// page that printed it.
type SitePersonFields struct {
	Name            string
	Role            string
	PublishedEmail  string
	LinkedinURL     string
	EvidenceSnippet string
	SourceURL       string
}

// ApplySitePersonFields fills a matched employee's empty fields from what the
// company's own site publishes about them. It reports whether a person was
// matched at all — false means the caller stages the lead, which is the
// unchanged path for every stranger and every ambiguous name.
func (s *Store) ApplySitePersonFields(ctx context.Context, orgID ids.OrganizationID, in SitePersonFields) (bool, error) {
	var matched bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		matched, err = s.applySitePersonFieldsTx(ctx, tx, orgID, in)
		return err
	})
	if err != nil {
		return false, err
	}
	return matched, nil
}

func (s *Store) applySitePersonFieldsTx(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, in SitePersonFields) (bool, error) {
	if err := auth.Require(ctx, entityPerson, principal.ActionUpdate); err != nil {
		return false, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return false, errors.New("people: a site person needs the name the page published")
	}
	// The organization is a KNOWN row and this is a read of it: row-scope is
	// re-checked so a leaked org id buys nothing (existence-hiding 404).
	if err := auth.EnsureVisible(ctx, tx, entityOrganization, orgID.UUID); err != nil {
		return false, err
	}

	personID, ok, err := matchSitePerson(ctx, tx, orgID, in)
	if err != nil || !ok {
		return false, err
	}

	sourceRef := siteFieldSource + ":" + in.SourceURL
	applied, err := fillSitePersonFields(ctx, tx, personID, sourceRef, by, in)
	if err != nil {
		return false, err
	}
	if len(applied) == 0 {
		// Matched, but the site said nothing this person was missing. Reported
		// as matched all the same: the lead must not stage, because the person
		// is not a stranger and a duplicate is not an improvement.
		return true, nil
	}

	auditID, err := storekit.Audit(ctx, tx, actionUpdate, entityPerson, personID.UUID,
		nil, map[string]any{auditKeyFields: applied, "source_ref": sourceRef})
	if err != nil {
		return false, fmt.Errorf("people: auditing the site person fill: %w", err)
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, personID.UUID, crmcontracts.PublicEventPersonUpdated{
		ChangedFields: map[string]any{auditKeyFields: applied, auditKeySource: siteFieldSource},
	}); err != nil {
		return false, fmt.Errorf("people: emitting person.updated for the site fill: %w", err)
	}
	return true, nil
}

// matchSitePerson resolves the published person to at most ONE employee of the
// organization: exact live email first, then a confident name match that must be
// unique. Ambiguity is not a tie to break — it is the answer "not identifiable",
// and it stages a lead like any stranger.
func matchSitePerson(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, in SitePersonFields) (ids.PersonID, bool, error) {
	if email := strings.ToLower(strings.TrimSpace(in.PublishedEmail)); email != "" {
		var id ids.PersonID
		err := tx.QueryRow(ctx, `
			SELECT p.id
			  FROM person p
			  JOIN person_email pe ON pe.person_id = p.id AND pe.email = $2 AND pe.archived_at IS NULL
			  JOIN relationship r ON r.person_id = p.id AND r.organization_id = $1
			   AND r.kind = 'employment' AND r.archived_at IS NULL
			 WHERE p.archived_at IS NULL AND p.merged_into_id IS NULL
			 LIMIT 1`, orgID, email).Scan(&id)
		if err == nil {
			return id, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ids.PersonID{}, false, fmt.Errorf("people: matching a site person by email: %w", err)
		}
	}

	rows, err := tx.Query(ctx, `
		SELECT p.id, p.full_name
		  FROM person p
		  JOIN relationship r ON r.person_id = p.id AND r.organization_id = $1
		   AND r.kind = 'employment' AND r.archived_at IS NULL
		 WHERE p.archived_at IS NULL AND p.merged_into_id IS NULL`, orgID)
	if err != nil {
		return ids.PersonID{}, false, fmt.Errorf("people: reading the organization's employees: %w", err)
	}
	defer rows.Close()
	var match ids.PersonID
	found := 0
	for rows.Next() {
		var id ids.PersonID
		var fullName string
		if err := rows.Scan(&id, &fullName); err != nil {
			return ids.PersonID{}, false, err
		}
		if nameSimilarity(in.Name, fullName) >= siteConfidentNameMatch {
			match, found = id, found+1
		}
	}
	if err := rows.Err(); err != nil {
		return ids.PersonID{}, false, err
	}
	if found != 1 {
		return ids.PersonID{}, false, nil
	}
	return match, true, nil
}

// fillSitePersonFields writes what the page published into the fields this
// person has not answered yet, and returns what actually landed.
//
// The published EMAIL is deliberately never written. It is a matching key here,
// not a fill: adding an address to an existing person changes who that record
// is reachable as, and the site is not authority for that.
func fillSitePersonFields(ctx context.Context, tx pgx.Tx, personID ids.PersonID, sourceRef, by string, in SitePersonFields) ([]string, error) {
	wsID := workspaceID(ctx)
	var applied []string
	write := func(field, value string) error {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}
		// The evidence row is the admission ticket, first verdict wins — the
		// same rule the signature pass writes under, so a site read can never
		// overwrite what a signature (or a human) already answered.
		tag, err := tx.Exec(ctx, `
			INSERT INTO person_profile_field (workspace_id, person_id, field, value, evidence_snippet, source_ref, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (person_id, field) DO NOTHING`,
			wsID, personID, field, value, in.EvidenceSnippet, sourceRef, siteFieldSource, by)
		if err != nil {
			return fmt.Errorf("people: site person evidence row (%s): %w", field, err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		if err := storekit.StampFields(ctx, tx, entityPerson, personID.UUID, sourceRef, by,
			[]storekit.FieldStamp{{Field: field}}); err != nil {
			return err
		}
		applied = append(applied, field)
		return nil
	}

	if err := write("role", in.Role); err != nil {
		return nil, err
	}
	if err := write("linkedin", in.LinkedinURL); err != nil {
		return nil, err
	}
	// The title column carries the role for display. Fill-only-empty: the NULL
	// predicate is the CAS, so an occupied title stands whoever set it.
	if role := strings.TrimSpace(in.Role); role != "" {
		tag, err := tx.Exec(ctx, `
			UPDATE person SET title = $2 WHERE id = $1 AND title IS NULL`, personID, role)
		if err != nil {
			return nil, fmt.Errorf("people: site person title fill: %w", err)
		}
		if tag.RowsAffected() > 0 {
			applied = append(applied, "title")
		}
	}
	return applied, nil
}
