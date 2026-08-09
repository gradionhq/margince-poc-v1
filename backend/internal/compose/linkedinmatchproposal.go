// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A LinkedIn match a human has to judge is an APPROVAL, not a queue of its own
// (founder decision, 2026-08-02).
//
// The first build gave the suggest tier its own list, its own confirm and
// reject endpoints and its own card. That was a second inbox: the product
// already has one place where a proposal waits for a person, it already
// records who decided what and when, and a member who works through their
// morning approvals should not also have to remember a settings tab.
//
// So the tier stages here instead. The ghost row keeps only the OUTCOME
// (`unmatched` until decided, `confirmed` once the effect runs); the pending
// state lives in the approval, which ADR-0036 makes the authority object.
//
// Rejection is durable because the approval row persists. The matcher skips a
// ghost that already carries a decided proposal, so refusing "André is Andre"
// once means never being asked again — including after a re-import, which is
// the case that matters when somebody refreshes a five-thousand-row export.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/diffhash"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// linkedInMatchKind is the staging kind. One per suggested match, not one per
// import: the decisions are independent, and a batch proposal would force a
// member to take thirty links to get the three they wanted.
const linkedInMatchKind = "linkedin_match"

// linkedInMatchProposal is what the inbox renders and the effect executes.
//
// It carries the ghost's OWN strings — the name and employer LinkedIn
// exported — because that is what a human judges the guess on. It does NOT
// carry anything else about the connection: a ghost is a third party who never
// agreed to be in this CRM, and a staged payload is read by anyone who can
// decide it.
type linkedInMatchProposal struct {
	ConnectionID ids.UUID `json:"connection_id"`
	PersonID     ids.UUID `json:"person_id"`
	// ConnectionName and ConnectionCompany are the export's own spelling. The
	// folded forms the matcher compared on are deliberately absent: nobody can
	// decide "andreas muller · simio".
	ConnectionName    string `json:"connection_name"`
	ConnectionCompany string `json:"connection_company,omitempty"`
	PersonName        string `json:"person_name"`
}

// withGhostOwnerAsSubject records the acting member as the subject the
// proposals are staged for. A context with no human actor cannot stage: a
// self-only proposal nobody is recorded for is one nobody can ever decide.
func withGhostOwnerAsSubject(ctx context.Context) (context.Context, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return nil, apperrors.ErrPermissionDenied
	}
	actor.OnBehalfOf = actor.UserID
	return principal.WithActor(ctx, actor), nil
}

// linkedInMatchStager is the seam people.Handlers calls after an import. It
// holds the approvals service so the transport does not have to, and builds it
// ONCE: the registration list is a dozen effects over a dozen stores, and
// rebuilding it per upload produces the same service every time.
func linkedInMatchStager(pool *pgxpool.Pool) func(context.Context) error {
	svc, store := approvalsServiceWithEffects(pool), people.NewStore(pool)
	return func(ctx context.Context) error {
		_, err := StageLinkedInMatches(ctx, svc, store)
		return err
	}
}

// StageLinkedInMatches proposes every undecided name-and-employer match this
// member's network produced.
//
// It runs under the ghost owner's own authority — the caller establishes that,
// as every other pass over these rows does — so a contact outside their row
// scope never becomes a proposal they can see.
func StageLinkedInMatches(ctx context.Context, svc *approvals.Service, store *people.Store) (int, error) {
	pending, err := store.PendingLinkedInMatches(ctx)
	if err != nil {
		return 0, err
	}
	return stagePendingLinkedInMatches(ctx, svc, pending)
}

// StageLinkedInMatchesForPerson is the same pass narrowed to the matches about
// ONE contact.
//
// The rule both entry points keep is that a pass proposes over the SAME scope it
// matched. Matching against a single arrival can only have raised questions
// about that arrival, so this is the complete answer for that caller as well as
// the bounded one: proposing the member's entire outstanding set instead would
// run once per person event and only ever rejoin rows that already exist.
func StageLinkedInMatchesForPerson(ctx context.Context, svc *approvals.Service, store *people.Store, person ids.UUID) (int, error) {
	pending, err := store.PendingLinkedInMatchesForPerson(ctx, person)
	if err != nil {
		return 0, err
	}
	return stagePendingLinkedInMatches(ctx, svc, pending)
}

// stagePendingLinkedInMatches turns the candidates a match produced into
// proposals — the one place both scopes pass through.
func stagePendingLinkedInMatches(ctx context.Context, svc *approvals.Service, pending []people.PendingLinkedInMatch) (int, error) {
	// Staged ON BEHALF OF the member whose network produced it, so the audit
	// trail records whose export raised the question. It grants nothing and
	// withholds nothing: who may decide is the inbox's ordinary rule — the
	// grants the effect needs, and visibility of the CONTACT the proposal is
	// about. ADR-0078/A123 settles that deliberately: who-knows-whom is
	// workspace-shared metadata, guarded by "you only see edges for a person
	// you can see at all", which is exactly what that rule already applies.
	ctx, err := withGhostOwnerAsSubject(ctx)
	if err != nil {
		return 0, err
	}
	staged := 0
	for _, m := range pending {
		proposed, err := stageOneLinkedInMatch(ctx, svc, m)
		if err != nil {
			return staged, err
		}
		if proposed {
			staged++
		}
	}
	return staged, nil
}

func stageOneLinkedInMatch(ctx context.Context, svc *approvals.Service, m people.PendingLinkedInMatch) (bool, error) {
	canonical, hash, err := diffhash.Object(map[string]any{
		"connection_id": m.ConnectionID.String(), "person_id": m.PersonID.String(),
		"connection_name": m.ConnectionName, "connection_company": m.ConnectionCompany,
		"person_name": m.PersonName,
	})
	if err != nil {
		return false, err
	}
	// The identity is the CONNECTION, not the diff: a later export that changes
	// the employer string should supersede the stale proposal for the same
	// connection rather than compete with it in the inbox. JoinPending makes
	// the re-import path idempotent, which matters because a member refreshing
	// a five-thousand-row export re-runs this over every row.
	identity, err := json.Marshal(map[string]string{"connection_id": m.ConnectionID.String()})
	if err != nil {
		return false, err
	}
	// StageUnlessDeclined, not Stage. A refusal is durable, and the engine
	// already owns that memory: it takes the identity lock BEFORE reading the
	// declined set, which closes the gap a hand-rolled "read the decided ids,
	// then stage" leaves open — a human rejecting between the two would have
	// the same question re-asked immediately.
	_, proposed, err := svc.StageUnlessDeclined(ctx, approvals.StageInput{
		Kind:           linkedInMatchKind,
		ProposedChange: canonical,
		DiffHash:       hash,
		TargetType:     string(recordTypePerson),
		TargetID:       m.PersonID,
		Identity:       identity,
		JoinPending:    true,
		Summary: fmt.Sprintf("%s at %s looks like %s",
			m.ConnectionName, employerOrPlaceholder(m.ConnectionCompany), m.PersonName),
	})
	return proposed, err
}

func employerOrPlaceholder(s string) string {
	if s == "" {
		return "an unnamed employer"
	}
	return s
}

// linkedInMatchAcceptEffect links the connection to the contact and puts the
// LinkedIn address on the record — the same write the automatic exact-name
// path performs, released by a human instead of by a string comparison.
func linkedInMatchAcceptEffect(svc *approvals.Service, store *people.Store) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		// The single-use redemption IS the idempotency claim: whoever consumes
		// the approval executes, anyone else finds it consumed.
		if _, _, err := svc.Redeem(ctx, approvalID, linkedInMatchKind, diffHash); err != nil {
			return err
		}
		var p linkedInMatchProposal
		if err := json.Unmarshal(proposedChange, &p); err != nil {
			return fmt.Errorf("compose: unreadable LinkedIn match proposal: %w", err)
		}
		if _, ok := principal.Actor(ctx); !ok {
			return fmt.Errorf("compose: LinkedIn match effect without a deciding principal")
		}
		// Executed as the DECIDER, not as a machine: a member approving a match
		// is making the claim themselves, and the write must be gated by their
		// grants and recorded against them.
		return store.ApplyLinkedInMatch(ctx, p.ConnectionID, p.PersonID)
	}
}
