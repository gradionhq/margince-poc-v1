// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Corroborated signature org-name promotion (PO-F-2a, ADR-0072/A118).
//
// A captured organization starts out named from its mail domain ("Gitex" for
// gitex.com) and stamped name_source='domain' — readable, but the company's own
// name for itself is usually in the signature blocks of the people who work
// there. Promoting that name is only safe with corroboration, because ONE
// signature is one sender's unverified claim about which company an entire
// record belongs to: an attacker who mails a connected mailbox from a domain
// nobody has written to yet would otherwise get to name the organization.
//
// So a name is promoted directly only when a source the SENDER DOES NOT CONTROL
// agrees: the site dossier's own stated name. Signatures never carry that
// authority, however many of them agree — the people at one organization share
// one mail domain, so two signatures are two mailboxes an actor who controls (or
// can forge From: for) that domain controls both of, and the capture path
// authenticates no From header. Counting them as independent sources would hand
// the naming decision back to exactly the attacker this rule exists to stop.
// Agreeing signatures still rank a claim above a lone one; they just stage a 🟡
// proposal instead of writing, which is the same answer with a human in it.
//
// The promotion never wins against a stronger source: the write is a CAS on
// name_source='domain' under a row lock, so a human edit (or a dossier name)
// landing first simply makes the promotion a no-op. Weaker never overwrites
// stronger, and the loser is silent rather than an error.

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The corroboration a promotion rests on — recorded on the audit row so the
// reason a name changed is readable years later, not re-derived.
const (
	// OrgNameCorroborationDossier: the organization's own site states this name.
	// The only corroboration that authorizes an unattended write — a sender
	// cannot edit the victim's website.
	OrgNameCorroborationDossier = "dossier"
	// OrgNameCorroborationSignatures: two or more people at the organization
	// signed with it. It outranks a lone signature but still stages for a human:
	// those people share one mail domain, so the agreement is one forgeable
	// source repeated, not two independent ones.
	OrgNameCorroborationSignatures = "signatures"
	// OrgNameCorroborationNone: one person said so and nothing agrees — this
	// verdict is staged for a human, never written.
	OrgNameCorroborationNone = "uncorroborated"
)

// orgNamePromotionSource is the DM-CONV-11 channel on the audit row.
const orgNamePromotionSource = "signature_promotion"

// SignatureOrgName is one person's accepted `org_name` signature evidence:
// the company that person's own mail signature names.
type SignatureOrgName struct {
	PersonID ids.PersonID
	Value    string
}

// OrgNameCandidate is one provisionally-named organization together with
// every name that could replace the domain-derived one.
type OrgNameCandidate struct {
	OrganizationID ids.OrganizationID
	DisplayName    string
	Signatures     []SignatureOrgName
	// DossierNames are the site-read profile values that state what the
	// company calls itself (display_name, legal_name).
	DossierNames []string
}

// OrgNameVerdict is what one candidate's evidence adds up to.
type OrgNameVerdict struct {
	// Name is the display name to write, verbatim as a signature spelled it.
	Name string
	// NameKey is Name's normalized form — the identity of the CLAIM rather
	// than of one spelling of it. A refusal keyed on this survives the
	// evidence moving; keyed on the payload it would not.
	NameKey string
	// Corroborated says whether it may be written without asking a human.
	Corroborated bool
	// Corroboration names WHICH source agreed (one of the constants above).
	Corroboration string
	// Persons are the people whose signatures carry the name — the evidence a
	// reviewer reads when the verdict is staged instead of applied.
	Persons []ids.PersonID
}

// DecideOrgName weighs one candidate's signatures against its dossier and the
// name the organization already carries. It reports no verdict (ok=false) when
// nothing proposes a different name.
//
// Pure, so the rule is testable without a database and reads as the rule
// rather than as a query plan.
func DecideOrgName(c OrgNameCandidate) (OrgNameVerdict, bool) {
	groups := groupSignatureNames(c.Signatures, NormalizeOrgName(c.DisplayName))
	if len(groups) == 0 {
		return OrgNameVerdict{}, false
	}
	winner := bestNameClaim(groups, dossierKeys(c.DossierNames))
	corroboration := winner.corroboration
	return OrgNameVerdict{
		Name:    dominantSpelling(winner.spelling),
		NameKey: winner.key,
		// ONLY the dossier authorizes an unattended write. Agreeing signatures
		// are the same mail domain speaking twice, and the sender chose it.
		Corroborated:  corroboration == OrgNameCorroborationDossier,
		Corroboration: corroboration,
		Persons:       sortedPersonIDs(winner.persons),
	}, true
}

// nameClaim is one company name several signatures agree on, with the spellings
// they used and the people who used them.
type nameClaim struct {
	key      string
	spelling map[string]int
	persons  map[ids.PersonID]bool
	// corroboration is filled by bestNameClaim, which is where the dossier is
	// known; until then a claim is just a claim.
	corroboration string
}

// dossierKeys reduces the site-stated names to the normalized set a signature
// can agree with.
func dossierKeys(names []string) map[string]bool {
	keys := make(map[string]bool, len(names))
	for _, n := range names {
		if key := NormalizeOrgName(n); key != "" {
			keys[key] = true
		}
	}
	return keys
}

// groupSignatureNames folds the signatures into one claim per normalized name:
// "Acme GmbH" and "ACME" are one claim made twice, which is exactly what
// corroboration counts. A signature restating the name already on the record
// proposes nothing and is dropped — it cannot corroborate a change either.
func groupSignatureNames(signatures []SignatureOrgName, current string) map[string]*nameClaim {
	claims := map[string]*nameClaim{}
	for _, s := range signatures {
		key := NormalizeOrgName(s.Value)
		if key == "" || key == current {
			continue
		}
		c, ok := claims[key]
		if !ok {
			c = &nameClaim{key: key, spelling: map[string]int{}, persons: map[ids.PersonID]bool{}}
			claims[key] = c
		}
		c.spelling[s.Value]++
		c.persons[s.PersonID] = true
	}
	return claims
}

// bestNameClaim ranks the claims and returns the winner with its corroboration
// resolved: corroborated beats uncorroborated, more people beats fewer, and the
// normalized name breaks the remaining tie — so two workers reading the same
// evidence always reach the same answer rather than renaming the organization
// back and forth.
func bestNameClaim(claims map[string]*nameClaim, dossier map[string]bool) *nameClaim {
	ranked := make([]*nameClaim, 0, len(claims))
	for _, c := range claims {
		c.corroboration = corroborationFor(c, dossier)
		ranked = append(ranked, c)
	}
	sort.Slice(ranked, func(i, j int) bool {
		ci := ranked[i].corroboration != OrgNameCorroborationNone
		cj := ranked[j].corroboration != OrgNameCorroborationNone
		if ci != cj {
			return ci
		}
		if len(ranked[i].persons) != len(ranked[j].persons) {
			return len(ranked[i].persons) > len(ranked[j].persons)
		}
		return ranked[i].key < ranked[j].key
	})
	return ranked[0]
}

// corroborationFor names the second source that agrees with a claim, if any.
func corroborationFor(c *nameClaim, dossier map[string]bool) string {
	switch {
	case dossier[c.key]:
		return OrgNameCorroborationDossier
	case len(c.persons) >= 2:
		return OrgNameCorroborationSignatures
	default:
		return OrgNameCorroborationNone
	}
}

// dominantSpelling picks the raw form to write: the one most signatures used,
// ties broken lexicographically so the choice is reproducible.
func dominantSpelling(counts map[string]int) string {
	spellings := make([]string, 0, len(counts))
	for s := range counts {
		spellings = append(spellings, s)
	}
	sort.Slice(spellings, func(i, j int) bool {
		if counts[spellings[i]] != counts[spellings[j]] {
			return counts[spellings[i]] > counts[spellings[j]]
		}
		return spellings[i] < spellings[j]
	})
	return spellings[0]
}

func sortedPersonIDs(set map[ids.PersonID]bool) []ids.PersonID {
	out := make([]ids.PersonID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// OrgNameCandidates lists provisionally-named organizations that have at least
// one employee signature naming a company, each with the evidence to judge it.
//
// One PAGE, keyed on the organization id the caller last saw — not the first N
// by age. The difference is the whole correctness of the sweep: most candidates
// reach a verdict that changes nothing (their signatures restate the name the
// record already carries, or the one name proposed is uncorroborated and waits
// on a human), and those rows stay candidates forever. A fixed prefix of a fixed
// ordering therefore fills with rows that will never resolve, and every
// organization behind them — including ones whose corroborated name is ready to
// apply today — is never looked at again. Paging to exhaustion is what stops
// that, and it costs nothing: the work per organization is one in-memory
// decision, no model call and no network.
func (s *Store) OrgNameCandidates(ctx context.Context, after ids.OrganizationID, limit int) ([]OrgNameCandidate, error) {
	var out []OrgNameCandidate
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var cursor *ids.OrganizationID
		if !after.IsZero() {
			cursor = &after
		}
		rows, err := tx.Query(ctx, `
			SELECT o.id, o.display_name
			FROM organization o
			WHERE o.name_source = 'domain'
			  AND o.archived_at IS NULL AND o.merged_into_id IS NULL
			  AND ($1::uuid IS NULL OR o.id > $1)
			  AND EXISTS (
				SELECT 1
				FROM relationship r
				JOIN person p ON p.id = r.person_id
				  AND p.archived_at IS NULL AND p.merged_into_id IS NULL
				JOIN person_profile_field f ON f.person_id = p.id AND f.field = 'org_name'
				WHERE r.organization_id = o.id AND r.kind = 'employment' AND r.archived_at IS NULL)
			ORDER BY o.id
			LIMIT $2`, cursor, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		candidates, byID, err := scanOrgNameCandidates(rows)
		if err != nil {
			return err
		}
		out = candidates
		if len(out) == 0 {
			return nil
		}
		orgIDs := make([]ids.OrganizationID, 0, len(out))
		for _, c := range out {
			orgIDs = append(orgIDs, c.OrganizationID)
		}
		if err := loadSignatureOrgNames(ctx, tx, orgIDs, out, byID); err != nil {
			return err
		}
		return loadDossierOrgNames(ctx, tx, orgIDs, out, byID)
	})
	if err != nil {
		return nil, fmt.Errorf("people: listing org-name promotion candidates: %w", err)
	}
	return out, nil
}

// scanOrgNameCandidates reads the candidate rows and the index the evidence
// loaders fill them through — one pass, so an organization's position is known
// before its signatures arrive.
func scanOrgNameCandidates(rows pgx.Rows) ([]OrgNameCandidate, map[ids.OrganizationID]int, error) {
	var out []OrgNameCandidate
	byID := map[ids.OrganizationID]int{}
	for rows.Next() {
		var c OrgNameCandidate
		if err := rows.Scan(&c.OrganizationID, &c.DisplayName); err != nil {
			return nil, nil, err
		}
		byID[c.OrganizationID] = len(out)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return out, byID, nil
}

func loadSignatureOrgNames(ctx context.Context, tx pgx.Tx, orgIDs []ids.OrganizationID,
	out []OrgNameCandidate, byID map[ids.OrganizationID]int,
) error {
	rows, err := tx.Query(ctx, `
		SELECT r.organization_id, p.id, f.value
		FROM relationship r
		JOIN person p ON p.id = r.person_id
		  AND p.archived_at IS NULL AND p.merged_into_id IS NULL
		JOIN person_profile_field f ON f.person_id = p.id AND f.field = 'org_name'
		WHERE r.organization_id = ANY($1) AND r.kind = 'employment' AND r.archived_at IS NULL`, orgIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var orgID ids.OrganizationID
		var sig SignatureOrgName
		if err := rows.Scan(&orgID, &sig.PersonID, &sig.Value); err != nil {
			return err
		}
		if i, ok := byID[orgID]; ok {
			out[i].Signatures = append(out[i].Signatures, sig)
		}
	}
	return rows.Err()
}

func loadDossierOrgNames(ctx context.Context, tx pgx.Tx, orgIDs []ids.OrganizationID,
	out []OrgNameCandidate, byID map[ids.OrganizationID]int,
) error {
	rows, err := tx.Query(ctx, `
		SELECT organization_id, value
		FROM organization_profile_field
		WHERE organization_id = ANY($1) AND field IN ('display_name', 'legal_name')`, orgIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var orgID ids.OrganizationID
		var value string
		if err := rows.Scan(&orgID, &value); err != nil {
			return err
		}
		if i, ok := byID[orgID]; ok {
			out[i].DossierNames = append(out[i].DossierNames, value)
		}
	}
	return rows.Err()
}

// PromoteOrgName writes a corroborated name onto a still-provisional
// organization in its own transaction — the sweep's entry point.
func (s *Store) PromoteOrgName(ctx context.Context, orgID ids.OrganizationID, name, corroboration string) (bool, error) {
	var promoted bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		promoted, err = s.PromoteOrgNameTx(ctx, tx, orgID, name, corroboration)
		return err
	})
	if err != nil {
		return false, err
	}
	return promoted, nil
}

// PromoteOrgNameTx is the same write on a caller's transaction — the review
// queue's accept redeems its approval and applies the name in one commit.
//
// Reports false, no error, when the organization is gone or no longer
// provisional: a human (or a dossier) naming it while the proposal sat in the
// inbox is the stronger source winning, which is the rule working, not a
// failure to report.
func (s *Store) PromoteOrgNameTx(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, name, corroboration string) (bool, error) {
	var current, source string
	// The row lock serializes this against a concurrent human edit: whoever
	// commits first is read by the other, so the CAS below cannot be decided
	// against a name that has already been replaced.
	err := tx.QueryRow(ctx, `
		SELECT display_name, name_source FROM organization
		WHERE id = $1 AND archived_at IS NULL
		FOR UPDATE`, orgID).Scan(&current, &source)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("people: reading the organization to promote: %w", err)
	}
	if source != nameSourceDomain || current == name {
		return false, nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE organization SET display_name = $2, name_source = $3
		WHERE id = $1 AND name_source = $4`,
		orgID, name, nameSourceSignature, nameSourceDomain)
	if err != nil {
		return false, fmt.Errorf("people: promoting the organization name: %w", err)
	}
	// The row lock above makes this unreachable, and it is checked anyway: an
	// audit row and an organization.updated event describing a rename that did
	// not happen are worse than the rename being skipped.
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	before := map[string]any{fieldDisplayName: current, "name_source": nameSourceDomain}
	after := map[string]any{
		fieldDisplayName: name, "name_source": nameSourceSignature,
		auditKeySource: orgNamePromotionSource, "corroboration": corroboration,
	}
	auditID, err := storekit.Audit(ctx, tx, actionUpdate, "organization", orgID.UUID, before, after)
	if err != nil {
		return false, fmt.Errorf("people: auditing the org-name promotion: %w", err)
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, orgID.UUID,
		crmcontracts.PublicEventOrganizationUpdated{ChangedFields: after}); err != nil {
		return false, fmt.Errorf("people: emitting organization.updated for the promotion: %w", err)
	}
	// The name this row was created under was a guess from its mail domain;
	// the one it just took is the company's own. That is the first moment a
	// twin captured from another domain can be recognised, so ask now.
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return false, err
	}
	if err := recheckOrgNameForDuplicates(ctx, tx, orgID, by); err != nil {
		return false, err
	}
	return true, nil
}
