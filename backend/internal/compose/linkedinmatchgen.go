// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The cg:linkedin-match consumer: a ghost attaches the moment its contact
// exists (ADR-0078 §8b).
//
// A workspace does not learn its contacts all at once. The LinkedIn export is
// uploaded during onboarding; the people it could match are created over the
// following hours and weeks — by mail capture, by a site read, by a rep typing
// a name in. Matching only at upload time meant every one of those arrivals
// was a match nobody would ever make.
//
// The trigger is the event, not the writer. person.created and person.updated
// reach the outbox because the write shape puts them there, so manual entry,
// capture, site read, merge and import all land here without any of them
// knowing this consumer exists — and a NEW writer added tomorrow is covered on
// the day it emits its first event. Asking each writer to remember to call the
// matcher would guarantee that one of them forgets.
//
// Organization events matter for the same reason and a sharper one: most
// unmatched ghosts are waiting on an employer, not on a name, so an account
// appearing unblocks a batch of them at once.
//
// It lives in compose because the call crosses modules — the events are the
// people module's own, but the seam that reacts to them is nobody's private
// business.
//
// Both halves are idempotent, so the at-least-once bus costs nothing — for two
// different reasons, which is worth saying because only one of them is the old
// one. A redelivered event re-runs a match that changes no row, because only
// UNMATCHED ghosts are ever matched. The proposal that now follows it considers
// SUGGESTED ones too, which is the point of proposing at all, and is idempotent
// because staging takes the proposal's identity lock and joins the live offer
// rather than minting a second copy of the same question.

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
)

// The outbox entity types this consumer reacts to. Declared here rather than
// reusing the FlipCRM import vocabulary: these name an event envelope's entity,
// and borrowing the incumbent-import constants sends a reader to the overlay
// path looking for why this consumer lives there.
const (
	matchEntityPerson       = "person"
	matchEntityOrganization = "organization"
)

// LinkedInMatchGen attaches LinkedIn ghosts as the CRM learns who exists, and
// asks a human about the ones a string comparison cannot settle.
type LinkedInMatchGen struct {
	store *people.Store
	// pool and authority are what lets each pass run under the GHOST OWNER's
	// authority rather than a system principal's. Nil means the old
	// system-principal shape, which no wired role uses: the constructor takes
	// both, and the tests that leave them nil exercise the per-person path,
	// which already runs under its caller.
	pool      *pgxpool.Pool
	authority authz.Resolver
	// approvals is built ONCE, here, rather than per event or per owner: the
	// registration list is a dozen effects over a dozen stores and produces the
	// same service every time, while this consumer runs on every contact write
	// in the workspace.
	approvals *approvals.Service
	log       *slog.Logger
}

// NewLinkedInMatchGen builds the matcher consumer over the people store.
func NewLinkedInMatchGen(pool *pgxpool.Pool, store *people.Store, authority authz.Resolver, log *slog.Logger) *LinkedInMatchGen {
	return &LinkedInMatchGen{
		store: store, pool: pool, authority: authority,
		approvals: approvalsServiceWithEffects(pool), log: log,
	}
}

// HandleEvent routes one envelope to a match. An event this consumer does not
// care about answers nil, so the group keeps flowing rather than wedging on
// somebody else's traffic.
func (g *LinkedInMatchGen) HandleEvent(ctx context.Context, env events.Envelope) error {
	if env.Entity.ID == ids.Nil {
		return nil
	}
	ctx = g.matchContext(ctx, env)

	switch env.Entity.Type {
	case matchEntityPerson:
		switch env.Type {
		// Every event that can make a live person row matchable. An archive needs no
		// reaction: both match arms require archived_at IS NULL, so an archived
		// contact stops being a candidate without anything being recomputed.
		// both match arms already require archived_at IS NULL, so an archive
		// needs no reaction, and a merge arrives as an update on the target.
		case "person.created", "person.updated", "person.merged", "person.restored":
			return g.matchPerson(ctx, env.WorkspaceID, env.Entity.ID)
		}
	case matchEntityOrganization:
		switch env.Type {
		// An account appearing or being renamed changes which company strings
		// resolve, and that is what most unmatched ghosts are waiting on. The
		// pass is workspace-wide because a new account can unblock ghosts
		// belonging to any member.
		case "organization.created", "organization.updated", "organization.merged":
			return g.matchWorkspace(ctx, env.WorkspaceID)
		}
	}
	return nil
}

// matchPerson re-runs the match for ONE contact, once per member with ghosts,
// each under their OWN authority.
//
// Per owner for the same reason the workspace pass is: the system actor is
// unbounded, so a single pass would match every member's ghosts against a
// contact none of them may be able to see, and report it back through
// match_status. Scoping to one person bounds the COST; it does not bound who
// is told.
func (g *LinkedInMatchGen) matchPerson(ctx context.Context, workspace, person ids.UUID) error {
	return forEachGhostOwner(ctx, g.pool, g.authority, workspace,
		func(ownerCtx context.Context, owner ids.UUID) error {
			matched, err := g.store.MatchLinkedInConnectionsForPerson(ownerCtx, owner, person)
			if err != nil {
				return err
			}
			// Proposed in the SAME pass, over the same contact that was
			// matched. A suggestion the matcher writes and nobody is asked
			// about is a suggestion that does not exist: the ghost row carries
			// only the outcome, and the pending question lives in the approval.
			staged, err := StageLinkedInMatchesForPerson(ownerCtx, g.approvals, g.store, person)
			if err != nil {
				return err
			}
			if matched.Confirmed+matched.Suggested+staged > 0 {
				g.log.InfoContext(ownerCtx, "linkedin match: a contact met their ghost",
					"person", person.String(), "owner", owner.String(),
					"confirmed", matched.Confirmed, "suggested", matched.Suggested, "staged", staged)
			}
			return nil
		})
}

// matchWorkspace re-runs the match for every member with undecided ghosts, each
// under their OWN authority. Whose ghosts get matched is then the same question
// as whose records they can see, which is what makes the answer independent of
// who triggered the event.
func (g *LinkedInMatchGen) matchWorkspace(ctx context.Context, workspace ids.UUID) error {
	return forEachGhostOwner(ctx, g.pool, g.authority, workspace,
		func(ownerCtx context.Context, owner ids.UUID) error {
			matched, err := g.store.MatchLinkedInConnections(ownerCtx, owner)
			if err != nil {
				return err
			}
			// The whole network was matched here, so the whole outstanding set
			// is what this pass owes a proposal over — the same scope rule the
			// per-person arm above follows in the narrow direction.
			//
			// This arm therefore pays the per-event cost the narrow one
			// refuses: one staging attempt per outstanding suggestion, per
			// owner, per organization event. It is accepted rather than
			// overlooked. A new or renamed account is exactly what unblocks
			// ghosts belonging to many different contacts at once, so there is
			// no narrower read that would still be complete — unlike the
			// person arm, where the arrival names its own scope.
			staged, err := StageLinkedInMatches(ownerCtx, g.approvals, g.store)
			if err != nil {
				return err
			}
			if matched.Confirmed+matched.Suggested+staged > 0 {
				g.log.InfoContext(ownerCtx, "linkedin match: an account unblocked ghosts",
					"owner", owner.String(),
					"confirmed", matched.Confirmed, "suggested", matched.Suggested, "staged", staged)
			}
			return nil
		})
}

// matchContext binds the envelope's workspace and the maintenance principal the
// OWNER enumeration runs under. The per-owner passes replace this actor with
// the member's own authority before any record is read — this one only reaches
// linkedin_connection and the roster.
func (g *LinkedInMatchGen) matchContext(ctx context.Context, env events.Envelope) context.Context {
	ctx = principal.WithWorkspaceID(ctx, env.WorkspaceID)
	ctx = principal.WithCorrelationID(ctx, env.Trace.CorrelationID)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:linkedin_match",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
}
