// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The capture auto-create engine (ADR-0063): mail names a counterparty, and
// this ensures a person — and, unless suppressed, their company and the
// employment edge — exists for it, all through the ONE dedupe chokepoint
// (PO-F-1/PO-F-2) in one transaction (the §9 single-tx exception: person +
// organization + relationship + link are one atomic decision here).
// Exact match reuses; fuzzy CREATES ANYWAY and records a dedupe_candidate
// for the review queue (capture never blocks on a human,
// DEDUPE_FUZZY_AUTOMERGE is pinned never); no match creates. Connector-
// created rows start visibility='owner' until a human promotes them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The repeated storage vocabulary of this engine, named once.
const (
	entityPerson       = "person"
	entityOrganization = "organization"
	fieldFullName      = "full_name"
	fieldDisplayName   = "display_name"
	fieldEmail         = "email"
	emailTypeWork      = "work"
)

// ErrCounterpartySuppressed marks an erased address (A13): deletion sticks,
// the counterparty is not re-created. The capture pipeline counts it as a
// deliberate skip.
var ErrCounterpartySuppressed = errors.New("people: counterparty address is on the erasure suppression list")

// EnsureCounterpartyInput is one captured message's counterparty.
type EnsureCounterpartyInput struct {
	Email       string // required; lowercased here
	DisplayName string // header display name — untrusted text
	Domain      string // lowercased mail domain

	OwnerID    ids.UUID       // the connecting human — owner of created rows
	ActivityID ids.ActivityID // the captured activity to link (person-only)
	Source     string         // provenance channel, e.g. "gmail:<message-id>"
	CapturedBy string         // "connector:<name>"

	// SuppressOrg skips company derivation (free-mail counterparty): the
	// person is still created — alice@gmail.com is a person, "Gmail" is
	// not her employer.
	SuppressOrg bool
}

// EnsureCounterpartyResult reports what the ensure did — every flag maps to
// rows the caller can count honestly.
type EnsureCounterpartyResult struct {
	PersonID       ids.PersonID
	PersonCreated  bool
	OrganizationID *ids.OrganizationID
	OrgCreated     bool
	DedupeRecorded bool
}

// EnsureCounterparty resolves-or-creates the person (and company) behind one
// captured message and links the activity to the person. Idempotent by
// construction: the exact tier lands repeats on the same row, and the link
// insert is conflict-free on replay.
func (s *Store) EnsureCounterparty(ctx context.Context, in EnsureCounterpartyInput) (EnsureCounterpartyResult, error) {
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if in.Email == "" {
		return EnsureCounterpartyResult{}, errors.New("people: a counterparty needs an email")
	}
	var res EnsureCounterpartyResult
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		suppressed, err := storekit.EmailSuppressed(ctx, tx, in.Email)
		if err != nil {
			return err
		}
		if suppressed {
			return ErrCounterpartySuppressed
		}
		if err := s.ensurePerson(ctx, tx, in, &res); err != nil {
			return err
		}
		if !in.SuppressOrg && in.Domain != "" {
			if err := s.ensureOrgAndEmployment(ctx, tx, in, &res); err != nil {
				return err
			}
		}
		return s.linkActivityToPerson(ctx, tx, in, res.PersonID)
	})
	if err != nil {
		return EnsureCounterpartyResult{}, err
	}
	return res, nil
}

// ensurePerson runs PO-F-1 and creates when it does not exactly match; a
// fuzzy hit creates AND records the pair for the review queue.
func (s *Store) ensurePerson(ctx context.Context, tx pgx.Tx, in EnsureCounterpartyInput, res *EnsureCounterpartyResult) error {
	if err := auth.Require(ctx, entityPerson, principal.ActionCreate); err != nil {
		return err
	}
	name := counterpartyName(in.DisplayName, in.Email)
	match, err := DedupePerson(ctx, tx, PersonCandidate{FullName: name, Emails: []string{in.Email}})
	if err != nil {
		return err
	}
	if match.Decision == DecisionExactCollision {
		res.PersonID = match.PersonID
		return nil
	}

	wsID := workspaceID(ctx)
	id := ids.New[ids.PersonKind]()
	quarantined := quarantineSuspect(in.DisplayName, in.Domain)
	if _, err := tx.Exec(ctx, `
		INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by, visibility, quarantined_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'owner', CASE WHEN $7 THEN now() ELSE NULL END)`,
		id, wsID, name, in.OwnerID, in.Source, in.CapturedBy, quarantined); err != nil {
		return fmt.Errorf("people: insert captured person: %w", err)
	}
	if err := insertPersonEmails(ctx, tx, wsID, id, in.Source, in.CapturedBy,
		[]PersonEmailInput{{Email: in.Email, EmailType: emailTypeWork, IsPrimary: true}}); err != nil {
		return err
	}
	auditID, err := storekit.Audit(ctx, tx, "create", entityPerson, id.UUID, nil, map[string]any{fieldFullName: name})
	if err != nil {
		return err
	}
	if err := storekit.Emit(ctx, tx, auditID, "person.created", entityPerson, id.UUID, map[string]any{fieldFullName: name}); err != nil {
		return err
	}
	res.PersonID = id
	res.PersonCreated = true

	if match.Decision == DecisionFuzzyReview {
		recorded, err := recordDedupeCandidate(ctx, tx, entityPerson, id.UUID, match.PersonID.UUID, match.Confidence,
			map[string]any{fieldFullName: name, fieldEmail: in.Email}, in.Source, in.CapturedBy)
		if err != nil {
			return err
		}
		res.DedupeRecorded = recorded
	}
	return nil
}

// ensureOrgAndEmployment runs PO-F-2 on the mail domain, creating the
// company when unknown, and plants the employment edge unless the person
// already has a current primary employer.
func (s *Store) ensureOrgAndEmployment(ctx context.Context, tx pgx.Tx, in EnsureCounterpartyInput, res *EnsureCounterpartyResult) error {
	if err := auth.Require(ctx, entityOrganization, principal.ActionCreate); err != nil {
		return err
	}
	match, err := DedupeOrganization(ctx, tx, OrganizationCandidate{Domains: []string{in.Domain}})
	if err != nil {
		return err
	}
	orgID := match.OrganizationID
	if match.Decision != DecisionExactCollision {
		wsID := workspaceID(ctx)
		orgID = ids.New[ids.OrganizationKind]()
		// The domain IS the honest name until enrichment learns better —
		// inventing a prettier one here would be fabrication.
		if _, err := tx.Exec(ctx, `
			INSERT INTO organization (id, workspace_id, display_name, owner_id, source, captured_by, visibility)
			VALUES ($1, $2, $3, $4, $5, $6, 'owner')`,
			orgID, wsID, in.Domain, in.OwnerID, in.Source, in.CapturedBy); err != nil {
			return fmt.Errorf("people: insert captured organization: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO organization_domain (workspace_id, organization_id, domain, is_primary, source, captured_by)
			VALUES ($1, $2, lower($3), true, $4, $5)`,
			wsID, orgID, in.Domain, in.Source, in.CapturedBy); err != nil {
			return fmt.Errorf("people: insert captured organization domain: %w", err)
		}
		auditID, err := storekit.Audit(ctx, tx, "create", entityOrganization, orgID.UUID, nil, map[string]any{fieldDisplayName: in.Domain})
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "organization.created", entityOrganization, orgID.UUID, map[string]any{fieldDisplayName: in.Domain}); err != nil {
			return err
		}
		res.OrgCreated = true
	}
	res.OrganizationID = &orgID

	// The employment edge: only when the person has no current primary
	// employer — capture suggests, it never reassigns someone's company
	// (the current-primary partial unique is the structural guard; the
	// NOT EXISTS keeps a concurrent race from surfacing as a 500).
	if _, err := tx.Exec(ctx, `
		INSERT INTO relationship (workspace_id, kind, person_id, organization_id, is_current_primary, source, captured_by)
		SELECT $1, 'employment', $2, $3, true, $4, $5
		WHERE NOT EXISTS (
			SELECT 1 FROM relationship
			WHERE kind = 'employment' AND person_id = $2 AND is_current_primary AND archived_at IS NULL)
		ON CONFLICT DO NOTHING`,
		workspaceID(ctx), res.PersonID, orgID, in.Source, in.CapturedBy); err != nil {
		return fmt.Errorf("people: insert employment edge: %w", err)
	}
	return nil
}

// linkActivityToPerson attaches the captured activity to the person —
// person-only by decision (the org rolls up through employment, a direct
// org link would double-count the same mail).
func (s *Store) linkActivityToPerson(ctx context.Context, tx pgx.Tx, in EnsureCounterpartyInput, personID ids.PersonID) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
		SELECT $1, $2, 'person', $3
		WHERE NOT EXISTS (
			SELECT 1 FROM activity_link WHERE activity_id = $2 AND entity_type = 'person' AND person_id = $3)`,
		workspaceID(ctx), in.ActivityID, personID); err != nil {
		return fmt.Errorf("people: linking activity to person: %w", err)
	}
	return nil
}

// recordDedupeCandidate stores the pair canonically (lower id left,
// DH-DDL-1); the unique pair index makes a re-detection a no-op — reported
// as recorded=false so counters stay honest.
func recordDedupeCandidate(ctx context.Context, tx pgx.Tx, entityType string, a, b ids.UUID, confidence float64, evidence map[string]any, source, by string) (bool, error) {
	left, right := a, b
	if right.String() < left.String() {
		left, right = right, left
	}
	payload, err := json.Marshal(evidence)
	if err != nil {
		return false, err
	}
	leftCol, rightCol := "left_person_id", "right_person_id"
	if entityType == entityOrganization {
		leftCol, rightCol = "left_org_id", "right_org_id"
	}
	tag, err := tx.Exec(ctx, fmt.Sprintf(`
		INSERT INTO dedupe_candidate (workspace_id, entity_type, %s, %s, confidence, evidence, source, captured_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT DO NOTHING`, leftCol, rightCol),
		workspaceID(ctx), entityType, left, right, confidence, payload, source, by)
	if err != nil {
		return false, fmt.Errorf("people: recording dedupe candidate: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// counterpartyName is the display name we can honestly store: the header
// name when present, else the address's local part — never empty (person
// pins full_name NOT NULL).
func counterpartyName(displayName, email string) string {
	name := strings.TrimSpace(displayName)
	if name != "" {
		return name
	}
	if local, _, ok := strings.Cut(email, "@"); ok && local != "" {
		return local
	}
	return email
}

// quarantineSuspect flags the cheap impersonation tells (ADR-0063): a
// punycode domain (homoglyph vector) or a display name that embeds an
// address on a DIFFERENT domain ("ceo@acme.com <attacker@evil.example>").
// Flagged rows carry quarantined_at for the review surface; capture still
// records them — hiding suspicious mail would be worse than labeling it.
func quarantineSuspect(displayName, domain string) bool {
	if strings.HasPrefix(domain, "xn--") || strings.Contains(domain, ".xn--") {
		return true
	}
	name := strings.ToLower(displayName)
	at := strings.Index(name, "@")
	if at < 0 {
		return false
	}
	embedded := name[at+1:]
	if end := strings.IndexAny(embedded, " >,;"); end >= 0 {
		embedded = embedded[:end]
	}
	embedded = strings.Trim(embedded, ".")
	return embedded != "" && embedded != strings.ToLower(domain)
}
