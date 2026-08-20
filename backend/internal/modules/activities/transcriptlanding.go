// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Reading a transcript the moment it LANDS, rather than only when somebody
// asks for it.
//
// The extraction lane (ai.TaskTranscriptPropose) was fully built and had never
// run once — transcript_read held zero rows — because the only thing that ever
// enqueued it was POST /v1/activities/{id}/transcript-read, and nothing called
// that. A meeting logged over the tool surface sat unread, and the commitments
// somebody made in it were never proposed.

import (
	"context"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// readTranscriptOnLanding starts the reading when a transcript ARRIVES,
// instead of waiting for somebody to ask for it.
//
// The extraction lane was fully built and had never run once: transcript_read
// held zero rows. The only thing that ever enqueued it was
// POST /v1/activities/{id}/transcript-read, and nothing called that — so a
// meeting logged over the tool surface sat unread, and the tasks somebody
// promised in it were never proposed. A capability nobody can reach is not a
// capability.
//
// Three conditions, and each one is load-bearing:
//   - source_system = 'transcript' is the marker the retention selector and
//     the reader both key on. It is what says "this body is a recording of a
//     conversation" rather than notes about one.
//   - a body, because there is nothing to read without one.
//   - created, so an idempotent replay does not queue a second reading of a
//     transcript already read. uq_transcript_read_inflight would refuse it
//     anyway; refusing here means no wasted transaction and no conflict error
//     on a call that did nothing wrong.
//
// A nil enqueue is a legal composition — a deployment with no transcript brain
// wired — and skips silently rather than failing the write. The activity is
// what the caller asked for; the reading is what this installation can offer.
func (s *Store) readTranscriptOnLanding(
	ctx context.Context, tx pgx.Tx, activity crmcontracts.Activity, created bool,
) error {
	if s.transcriptEnqueue == nil || !startsAReading(activity, created) {
		return nil
	}
	// The GRANTING human, not the passport. deepreadprincipal recovers an
	// on-behalf user only from a `human:` value, and an MCP principal is
	// `agent:<passport>` — a reading requested by an agent id has nobody to
	// route its proposals to.
	requestedBy := requestingHuman(ctx)
	if requestedBy == "" {
		return nil
	}
	_, _, err := s.StartTranscriptReadQueuedTx(ctx, tx,
		ids.From[ids.ActivityKind](ids.UUID(activity.Id)), requestedBy, s.transcriptEnqueue)
	return err
}

// requestingHuman names the person a transcript reading is requested for.
//
// The HUMAN, never the passport: a reading's proposals are staged for somebody
// to accept, and deepreadprincipal recovers an on-behalf user only from a
// `human:` value. An MCP principal is `agent:<passport>`, so requesting as the
// agent would stage proposals with nobody to route them to.
//
// Empty when nobody is behind the call — a system principal or a connector.
// The transcript is stored either way; only the reading is skipped, because a
// reading whose outcome no one can accept is work with no destination.
func requestingHuman(ctx context.Context) string {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return ""
	}
	if !actor.OnBehalfOf.IsZero() {
		return "human:" + actor.OnBehalfOf.String()
	}
	if actor.Type == principal.PrincipalHuman && !actor.UserID.IsZero() {
		return "human:" + actor.UserID.String()
	}
	return ""
}

// startsAReading is the three-part test, spelled where it can be read and
// tested without a database behind it.
func startsAReading(activity crmcontracts.Activity, created bool) bool {
	if !created {
		return false
	}
	if activity.SourceSystem == nil || *activity.SourceSystem != transcriptSourceSystem {
		return false
	}
	return activity.Body != nil && *activity.Body != ""
}
