// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"errors"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// fieldKind names the activity kind in the write shape's audit and outbox
// payloads (the one spelling of the payload key).
const (
	fieldKind    = "kind"
	fieldSubject = "subject"
)

// activityCapturedPayload builds the activity.captured event for the
// direct-log path (this package's only emit site of the event's two) — it
// never names a source_system, which is exclusive to the capture
// auto-create path (capture/sink.go's own local builder).
func activityCapturedPayload(kind, channelProvider string) crmcontracts.PublicEventActivityCaptured {
	p := crmcontracts.PublicEventActivityCaptured{Kind: kind}
	// Carried only when there is one, so the envelope's omitempty leaves it
	// absent rather than publishing an empty transport. A subscriber reads the
	// pair: since ADR-0107/A158 the kind alone no longer says what carried a
	// message, and without this field they could not tell one transport from
	// another at all.
	if channelProvider != "" {
		p.ChannelProvider = &channelProvider
	}
	return p
}

type LogActivityInput struct {
	Kind string
	// ChannelProvider names the messaging transport that carried this activity —
	// a channel_provider row — and is empty for anything that did not travel on
	// one. Separate from Kind because they answer separate questions: what sort
	// of interaction happened, versus how it travelled (ADR-0107/A158).
	//
	// Non-empty exactly when Kind is KindMessage, which the database enforces in
	// both directions; a mismatch is a 422 from the CHECK, not a silent write.
	ChannelProvider string
	Subject         *string
	Body            *string
	OccurredAt      *time.Time
	Direction       *string
	// MeetingStatus is meaningful only for kind meeting; the mapping refuses
	// it on any other kind. The lead status ladder reads booked/held as
	// engagement, so a hand-logged meeting moves a lead the same as a synced
	// one.
	MeetingStatus *string
	DueAt         *time.Time
	RemindAt      *time.Time
	AssigneeID    *ids.UserID
	HostUserID    *ids.UserID
	SourceSystem  *string
	SourceID      *string
	// ThreadKey files this activity under a conversation. Empty stores NULL.
	// It is written at insert time or not at all: the (source_system,
	// source_id) upsert both capture and this path key on does nothing when
	// the row already exists, so neither leg can revise the other's value.
	ThreadKey string
	// CounterpartyEmail is the address this message was with, normalized —
	// the column capture's correspondence-positive gate (ADR-0072 §1) reads.
	// CounterpartyOutboundAttested says the workspace itself sent to that
	// address; it is affirmative intent toward them, and it is what spares
	// their later mail from suppression. Both obey the same write-once rule
	// as ThreadKey, for the same reason.
	CounterpartyEmail            string
	CounterpartyOutboundAttested bool
	Links                        []ActivityLinkInput
	Source                       string
}

// LogActivity writes the activity + links; the last_activity_at clocks
// (data-model §7) are maintained in the schema, not here. Idempotent on
// (source_system, source_id): replaying a capture returns the existing
// activity.
func (s *Store) LogActivity(ctx context.Context, in LogActivityInput) (crmcontracts.Activity, bool, error) {
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return crmcontracts.Activity{}, false, err
	}
	var out crmcontracts.Activity
	created := true
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, created, err = s.logActivityAndReadTranscript(ctx, tx, in)
		return err
	})
	return out, created, err
}

// LogActivityTx is LogActivity's transaction-accepting variant (the C5
// shared-tx shape): a caller that must commit an activity note atomically
// with a sibling module's own write (the extraction accept-write's deal
// update, compose/extractionaccept.go) drives it inside the ONE
// transaction it already opened, so a note failure rolls the sibling
// write back too, instead of letting LogActivity open (and commit) a
// second transaction of its own.
func (s *Store) LogActivityTx(ctx context.Context, tx pgx.Tx, in LogActivityInput) (crmcontracts.Activity, bool, error) {
	if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
		return crmcontracts.Activity{}, false, err
	}
	return s.logActivityAndReadTranscript(ctx, tx, in)
}

// logActivityAndReadTranscript is the ONE place the write and the reading are
// joined, so every door reaches both.
//
// It sits here rather than in each entry point because there are three of
// them: LogActivity opens its own transaction, LogActivityTx runs inside a
// caller's, and both are reached from the tool surface, from REST, and from
// the extension core. The first cut hooked only LogActivity, which meant
// POST /v1/activities and the extension's own logging stored a transcript and
// silently never read it — the exact defect this feature exists to close,
// reintroduced on two of its three doors.
func (s *Store) logActivityAndReadTranscript(
	ctx context.Context, tx pgx.Tx, in LogActivityInput,
) (crmcontracts.Activity, bool, error) {
	out, created, err := logActivityInTx(ctx, tx, in)
	if err != nil {
		return out, created, err
	}
	return out, created, s.readTranscriptOnLanding(ctx, tx, out, created)
}

// logActivityInTx is LogActivity's transactional body, shared by the
// store-opened (LogActivity) and caller-opened (LogActivityTx) entry
// points.
func logActivityInTx(ctx context.Context, tx pgx.Tx, in LogActivityInput) (crmcontracts.Activity, bool, error) {
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Activity{}, false, err
	}
	occurredAt := time.Now().UTC()
	if in.OccurredAt != nil {
		occurredAt = in.OccurredAt.UTC()
	}

	replay, err := replayedActivity(ctx, tx, in)
	if err != nil {
		return crmcontracts.Activity{}, false, err
	}
	if replay != nil {
		return *replay, false, nil
	}

	id := ids.New[ids.ActivityKind]()
	_, err = tx.Exec(ctx,
		`INSERT INTO activity (id, kind, channel_provider, subject, body, occurred_at, direction, meeting_status,
		                       due_at, remind_at, assignee_id, host_user_id, source_system, source_id, source, captured_by,
		                       thread_key, counterparty_email, counterparty_outbound_attested)
		 VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, NULLIF($17, ''),
		         NULLIF($18, ''), $19)`,
		// NULLIF on channel_provider: the column FKs into channel_provider, and
		// '' names no provider, so anything without a transport stores NULL.
		id, in.Kind, in.ChannelProvider, in.Subject, in.Body, occurredAt, in.Direction, in.MeetingStatus,
		in.DueAt, in.RemindAt, in.AssigneeID, in.HostUserID, in.SourceSystem, in.SourceID, in.Source, by,
		in.ThreadKey, in.CounterpartyEmail, in.CounterpartyOutboundAttested)
	if err != nil {
		if storekit.IsUniqueViolation(err) {
			return crmcontracts.Activity{}, false, apperrors.ErrConflict
		}
		return crmcontracts.Activity{}, false, err
	}

	if err := insertActivityLinks(ctx, tx, id, in.Links); err != nil {
		return crmcontracts.Activity{}, false, err
	}
	// Who was in it (ACT-DDL-3). After the links, because the counterparty is
	// whichever person they name — and they have just been through the
	// row-scope gate, so nothing here needs to re-check them.
	if err := stampLoggedParticipants(ctx, tx, id, in.Kind, in.Direction, in.Links); err != nil {
		return crmcontracts.Activity{}, false, err
	}

	auditID, err := storekit.Audit(ctx, tx, "create", "activity", id.UUID, nil, map[string]any{fieldKind: in.Kind, fieldSubject: in.Subject})
	if err != nil {
		return crmcontracts.Activity{}, false, err
	}
	// activity.captured is the first-class verb — emitted instead of a
	// generic activity.created, never in addition (events.md §1).
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, activityCapturedPayload(in.Kind, in.ChannelProvider)); err != nil {
		return crmcontracts.Activity{}, false, err
	}
	out, err := readActivity(ctx, tx, id, storekit.LiveOnly)
	if err != nil {
		return crmcontracts.Activity{}, false, err
	}
	return out, true, nil
}

// replayedActivity resolves the (source_system, source_id) idempotency
// key: replaying a capture returns the existing activity. The replay
// path returns a record, so it is a read and carries the read's row
// scope: replaying someone else's external key must not hand over their
// activity. Out of scope answers the same 409 the unique-index race
// does — the key is taken, the record is not disclosed.
func replayedActivity(ctx context.Context, tx pgx.Tx, in LogActivityInput) (*crmcontracts.Activity, error) {
	if in.SourceSystem == nil || in.SourceID == nil {
		return nil, nil
	}
	var existing ids.ActivityID
	err := tx.QueryRow(ctx,
		`SELECT id FROM activity
		   WHERE source_system = $1 AND source_id = $2`,
		*in.SourceSystem, *in.SourceID).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// The row exists — the SELECT above just found it — so the only way
	// readActivity's own row-scope gate can answer ErrNotFound here is that
	// the key belongs to someone out of scope.
	out, err := readActivity(ctx, tx, existing, storekit.IncludeArchived)
	if errors.Is(err, apperrors.ErrNotFound) {
		return nil, apperrors.ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}
