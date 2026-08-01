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

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/freemail"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The repeated storage vocabulary of this engine, named once.
const (
	evidenceFieldKey  = "field"
	evidenceLeftKey   = "left_value"
	evidenceRightKey  = "right_value"
	evidenceScoreKey  = "score"
	evidenceSignalKey = "signal"
	// The fuzzy tier's evidence signals. "collide" is observable downstream:
	// the review queue renders the value as a row data attribute its
	// stylesheet selects on, so the string reaches the UI even though the
	// contract types the field as a bare string and nothing gates a rename.
	evidenceSignalCollide  = "collide"
	evidenceSignalOneSided = "one_sided"

	entityPerson       = "person"
	entityOrganization = "organization"
	fieldFullName      = "full_name"
	fieldDisplayName   = "display_name"
	fieldEmail         = "email"
	fieldPhone         = "phone"
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

	// TriagePending reports that this domain's organization question was left
	// open, and TriageDomain names the domain to ask about. The ensure only
	// RECORDS the question — enqueueing the crawl that answers it belongs to
	// compose, after this transaction commits, so a queue outage can never cost
	// the message that raised it. The sweep re-finds anything a missed trigger
	// dropped, so a lost signal costs latency and nothing else.
	TriagePending bool
	TriageDomain  string
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
		var err error
		res, err = s.EnsureCounterpartyTx(ctx, tx, in)
		return err
	})
	if err != nil {
		return EnsureCounterpartyResult{}, err
	}
	return res, nil
}

// EnsureCounterpartyTx is the same resolve-or-create on a transaction the CALLER
// owns, for the paths that must commit records together with the decision that
// authorized them — the ADR-0072 verdict engine resolving a deferred
// disposition, and the review-queue accept that redeems a staged proposal.
// Neither may leave a ledger row reading `real` while the records it promised
// rolled back, so neither can use the pool-owning form above.
//
// The caller is responsible for the workspace GUC; it is already set by the
// WithWorkspaceTx that produced tx.
func (s *Store) EnsureCounterpartyTx(ctx context.Context, tx pgx.Tx, in EnsureCounterpartyInput) (EnsureCounterpartyResult, error) {
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if in.Email == "" {
		return EnsureCounterpartyResult{}, errors.New("people: a counterparty needs an email")
	}
	var res EnsureCounterpartyResult
	suppressed, err := storekit.EmailSuppressed(ctx, tx, in.Email)
	if err != nil {
		return EnsureCounterpartyResult{}, err
	}
	if suppressed {
		return EnsureCounterpartyResult{}, ErrCounterpartySuppressed
	}
	if err := s.ensurePerson(ctx, tx, in, &res); err != nil {
		return EnsureCounterpartyResult{}, err
	}
	if !in.SuppressOrg && in.Domain != "" {
		if err := s.ensureOrgAndEmployment(ctx, tx, in, &res); err != nil {
			return EnsureCounterpartyResult{}, err
		}
	}
	if err := s.linkActivityToPerson(ctx, tx, in.ActivityID, res.PersonID); err != nil {
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
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventPersonCreated{FullName: name}); err != nil {
		return err
	}
	res.PersonID = id
	res.PersonCreated = true

	if match.Decision == DecisionFuzzyReview {
		// The detection-time snapshot the queue renders (DH-N-8): captured
		// NOW, against the incumbent as it looked when the score was
		// computed — never re-derived later.
		var incumbentName string
		if err := tx.QueryRow(ctx, `SELECT full_name FROM person WHERE id = $1`, match.PersonID).Scan(&incumbentName); err != nil {
			return fmt.Errorf("people: reading dedupe incumbent: %w", err)
		}
		evidence := []map[string]any{
			{evidenceFieldKey: fieldFullName, evidenceLeftKey: name, evidenceRightKey: incumbentName, evidenceSignalKey: evidenceSignalCollide, evidenceScoreKey: match.Confidence},
			{evidenceFieldKey: fieldEmail, evidenceLeftKey: in.Email, evidenceRightKey: nil, evidenceSignalKey: evidenceSignalOneSided},
		}
		recorded, err := recordDedupeCandidate(ctx, tx, entityPerson, id.UUID, match.PersonID.UUID, match.Confidence,
			evidence, in.Source, in.CapturedBy)
		if err != nil {
			return err
		}
		res.DedupeRecorded = recorded
	}
	return nil
}

// ensureOrgAndEmployment decides what this mail domain may create, and creates
// only that. It runs PO-F-2 on the domain, and where the domain is not yet
// understood it creates NOTHING and opens the question instead — the person
// already exists, and an organization invented from a domain label is the
// defect this ladder exists to stop.
//
// The order is load-bearing at every step:
//
//	an organization already on this domain  → attach; a human's row always wins
//	consumer mail                           → no company, and no question to ask
//	a settled verdict                       → obey it
//	anything else                           → open the question, create nothing
func (s *Store) ensureOrgAndEmployment(ctx context.Context, tx pgx.Tx, in EnsureCounterpartyInput, res *EnsureCounterpartyResult) error {
	if err := auth.Require(ctx, entityOrganization, principal.ActionCreate); err != nil {
		return err
	}
	base := freemail.Registrable(in.Domain)
	match, err := DedupeOrganization(ctx, tx, OrganizationCandidate{Domains: []string{in.Domain, base}})
	if err != nil {
		return err
	}
	if match.Decision != DecisionExactCollision {
		// No organization yet. Whether one may be created is not this path's
		// call any more.
		return s.deferOrgToTriage(ctx, tx, in, base, res)
	}
	orgID := match.OrganizationID
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

// deferOrgToTriage handles a domain with no organization behind it yet: decide
// whether the question is even worth asking, and if it is, open it.
//
// Nothing is created here on purpose. The person is already committed and the
// message is already on their timeline; what is withheld is a company row that
// nothing yet justifies. ADR-0063's create-on-sight is what manufactured
// "Herpertz" from a man's own domain, and no later evidence removed it.
func (s *Store) deferOrgToTriage(ctx context.Context, tx pgx.Tx, in EnsureCounterpartyInput, base string, res *EnsureCounterpartyResult) error {
	// Consumer mail is answered by the domain itself and needs no crawl to
	// settle. The sink's tier ladder usually catches it first; this repeats the
	// check because the verdict engine and the review-queue accept reach this
	// same chokepoint without passing through that ladder.
	consumerMail, err := s.freemail(ctx, tx)
	if err != nil {
		return err
	}
	if in.SuppressOrg || consumerMail.IsConsumer(base) {
		return nil
	}
	prior, known, err := readDispositionTx(ctx, tx, base)
	if err != nil {
		return err
	}
	if known && prior.Settled() {
		// personal, provider, no_site — all answered, all "no company from this
		// domain". A 'company' verdict cannot reach here: the dedupe above
		// would have found the organization it named.
		return nil
	}
	if known {
		res.TriagePending, res.TriageDomain = true, base
		return nil
	}
	if err := recordPendingDispositionTx(ctx, tx, base, in.OwnerID); err != nil {
		return err
	}
	res.TriagePending, res.TriageDomain = true, base
	return nil
}

// linkActivityToPerson attaches the captured activity to the person —
// person-only by decision (the org rolls up through employment, a direct
// org link would double-count the same mail). Shared with the channel ensure,
// which links the same way: it takes the activity id rather than either
// path's input so neither has to know the other's shape.
//
// Being the ONE point both paths reach, it is also where the person is settled
// against a merge. Every step above it — the dedupe ladder, the identity bind,
// the handle refresh — named its person from a read that a merge can invalidate
// before this insert runs, and no reader of activity_link walks merged_into_id,
// so a link written to the retired id leaves the message on a record nobody
// opens. Resolving it here covers both callers at once, and it is the last read
// before the write, so nothing can overtake it.
func (s *Store) linkActivityToPerson(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, personID ids.PersonID) error {
	// FOR UPDATE serializes this behind the merge's own LockPair, so the
	// redirect this reads is the committed one; one hop is enough because a
	// merge repoints its source's redirect rather than chaining (merge.go).
	// An archived survivor is still the right subject: the message happened,
	// and this call's caller logs faults rather than failing the capture, so
	// refusing here would drop the link silently rather than loudly.
	var canonical ids.PersonID
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(merged_into_id, id) FROM person WHERE id = $1 FOR UPDATE`,
		personID).Scan(&canonical); err != nil {
		return fmt.Errorf("people: resolving the person this activity belongs to: %w", err)
	}
	personID = canonical
	if _, err := tx.Exec(ctx, `
		INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
		SELECT $1, $2, 'person', $3
		WHERE NOT EXISTS (
			SELECT 1 FROM activity_link WHERE activity_id = $2 AND entity_type = 'person' AND person_id = $3)`,
		workspaceID(ctx), activityID, personID); err != nil {
		return fmt.Errorf("people: linking activity to person: %w", err)
	}
	return namePersonAmongParticipants(ctx, tx, activityID, personID)
}

// recordDedupeCandidate stores the pair canonically (lower id left,
// DH-DDL-1); the unique pair index makes a re-detection a no-op — reported
// as recorded=false so counters stay honest.
func recordDedupeCandidate(ctx context.Context, tx pgx.Tx, entityType string, a, b ids.UUID, confidence float64, evidence []map[string]any, source, by string) (bool, error) {
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
//
// Both tells are statements ABOUT the sender's mail domain, so with no domain
// there is nothing for either to contradict and the answer is no. Without that
// floor the second tell compares an embedded address against "" and matches
// every display name that merely contains an "@" — quarantining a record for a
// reason that cannot apply to it.
func quarantineSuspect(displayName, domain string) bool {
	if domain == "" {
		return false
	}
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
