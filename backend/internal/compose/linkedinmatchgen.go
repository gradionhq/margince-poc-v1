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
// business. Recomputing is idempotent, so the at-least-once bus costs nothing:
// a redelivered event re-runs a match that has already been made and changes
// no row, because only UNMATCHED ghosts are ever considered.

import (
	"context"
	"log/slog"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// LinkedInMatchGen attaches LinkedIn ghosts as the CRM learns who exists.
type LinkedInMatchGen struct {
	store *people.Store
	log   *slog.Logger
}

// NewLinkedInMatchGen builds the matcher consumer over the people store.
func NewLinkedInMatchGen(store *people.Store, log *slog.Logger) *LinkedInMatchGen {
	return &LinkedInMatchGen{store: store, log: log}
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
	case flipObjectPerson:
		switch env.Type {
		// created and updated only. An ARCHIVED contact is deliberately absent:
		// both match arms already require archived_at IS NULL, so an archive
		// needs no reaction, and a merge arrives as an update on the target.
		case "person.created", "person.updated", "person.merged", "person.restored":
			return g.matchPerson(ctx, env.Entity.ID)
		}
	case flipObjectOrganization:
		switch env.Type {
		// An account appearing or being renamed changes which company strings
		// resolve, and that is what most unmatched ghosts are waiting on. The
		// pass is workspace-wide because a new account can unblock ghosts
		// belonging to any member.
		case "organization.created", "organization.updated", "organization.merged":
			return g.matchWorkspace(ctx)
		}
	}
	return nil
}

func (g *LinkedInMatchGen) matchPerson(ctx context.Context, person ids.UUID) error {
	matched, err := g.store.MatchLinkedInConnectionsForPerson(ctx, person)
	if err != nil {
		return err
	}
	if matched.Confirmed+matched.Suggested > 0 {
		g.log.InfoContext(ctx, "linkedin match: a contact met their ghost",
			"person", person.String(),
			"confirmed", matched.Confirmed, "suggested", matched.Suggested)
	}
	return nil
}

func (g *LinkedInMatchGen) matchWorkspace(ctx context.Context) error {
	matched, err := g.store.MatchLinkedInConnections(ctx, ids.Nil)
	if err != nil {
		return err
	}
	if matched.Confirmed+matched.Suggested > 0 {
		g.log.InfoContext(ctx, "linkedin match: an account unblocked ghosts",
			"confirmed", matched.Confirmed, "suggested", matched.Suggested)
	}
	return nil
}

// matchContext binds the envelope's workspace and a system principal. The
// matcher is maintenance, not a user action: it must consider every ghost and
// every contact the base tables hold, or which matches get made would depend on
// who happened to trigger the event.
func (g *LinkedInMatchGen) matchContext(ctx context.Context, env events.Envelope) context.Context {
	ctx = principal.WithWorkspaceID(ctx, env.WorkspaceID)
	ctx = principal.WithCorrelationID(ctx, env.Trace.CorrelationID)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:linkedin_match",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
}
