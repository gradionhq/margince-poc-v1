// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Recovering the CCs and meeting attendees of activities captured before
// participants were recorded structurally (ADR-0078 / ACT-DDL-3).
//
// The two-end backfill (activities.BackfillParticipantsBatch) recovers who the
// message was BETWEEN. It deliberately stops there, because naming everyone
// else means re-reading the stored original — a different kind of work, with
// its own parser per provider and its own failure mode. This is that pass.
//
// It lives in compose because it spans two modules that may not import each
// other: the parsers belong to capture (mail and calendar each read their own
// format), and nothing in one module may reach into a sibling. The write goes
// back through capture.StampFurtherParticipants rather than a second copy of
// the resolution SQL, so a recovered row and a captured one are identical to
// the interaction graph that reads them.
//
// The pass is bounded by a durable marker per activity rather than by a
// cursor. Most messages have no CCs at all, so "did this one have any" cannot
// be asked of the result — without the marker the loop would re-parse the same
// thousands of originals forever. participantreplay's own migration says why
// the two-end backfill needs no such thing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gcal"
	"github.com/gradionhq/margince/backend/internal/modules/capture/mailmap"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The verdicts the marker table records. Only `participants` wrote rows; the
// other three say why an activity produced none, which is what keeps a
// re-parse from being attempted forever.
const (
	replayWroteParticipants = "participants"
	replayFoundNone         = "none"
	replayUnreadable        = "unreadable"
	replayNoOwner           = "no_owner"
)

// The connectors whose stored originals this pass can re-read. A source system
// absent from here is marked unreadable rather than skipped: skipping would
// re-select it on every pass, and the honest record is that this parser has no
// reading of that format.
const (
	sourceGmail = "gmail"
	sourceIMAP  = "imap"
	sourceGCal  = "gcal"
)

// replayCandidate is one activity whose original is still on file.
type replayCandidate struct {
	activityID ids.ActivityID
	kind       string
	source     string
	payload    []byte
	// owner is the mailbox address the connection reads, taken from the
	// connection's own account label rather than the granting user's login
	// address — those differ, and it is the MAILBOX the headers name.
	owner string
}

// replayParticipantsBatch re-reads up to limit stored originals and returns how
// many activities it settled — written, empty or refused alike, because every
// one of them is progress the next pass will not repeat.
func replayParticipantsBatch(ctx context.Context, pool *pgxpool.Pool, limit int, log *slog.Logger) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("compose: the participant replay needs a positive batch limit, got %d", limit)
	}
	var settled int
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		candidates, err := selectReplayCandidates(ctx, tx, limit)
		if err != nil {
			return err
		}
		for _, c := range candidates {
			outcome, err := replayOne(ctx, tx, c)
			if err != nil {
				return err
			}
			if err := markReplayed(ctx, tx, c.activityID, outcome); err != nil {
				return err
			}
			settled++
		}
		if settled > 0 {
			log.DebugContext(ctx, "participant replay: settled a batch of stored originals", "activities", settled)
		}
		return nil
	})
	return settled, err
}

// selectReplayCandidates finds interaction activities whose original is still
// stored and which have not been re-read yet.
//
// The owner arm mirrors the two-end backfill's class 2b rule: a provider with
// exactly one connection identifies the mailbox, and with two it does not,
// because nothing on the activity row separates them. An ambiguous mailbox
// yields no owner here rather than a guess — parsing against the wrong address
// would file the mailbox owner as a participant of their own conversation.
func selectReplayCandidates(ctx context.Context, tx pgx.Tx, limit int) ([]replayCandidate, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id, a.kind, a.source_system, rc.payload,
		       coalesce((
		         SELECT c.account_label
		           FROM capture_connection c
		          WHERE c.provider = a.source_system
		            AND NOT EXISTS (
		                SELECT 1 FROM capture_connection other
		                 WHERE other.provider = c.provider AND other.id <> c.id)
		          LIMIT 1), '')
		  FROM activity a
		  JOIN raw_capture rc
		    ON rc.source_system = a.source_system AND rc.source_id = a.source_id
		 WHERE a.archived_at IS NULL
		   AND a.source_system <> ''
		   AND NOT EXISTS (
		       SELECT 1 FROM activity_participant_replay r WHERE r.activity_id = a.id)
		 ORDER BY a.id
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("compose: selecting activities whose participants can be replayed: %w", err)
	}
	defer rows.Close()

	var out []replayCandidate
	for rows.Next() {
		var c replayCandidate
		if err := rows.Scan(&c.activityID, &c.kind, &c.source, &c.payload, &c.owner); err != nil {
			return nil, fmt.Errorf("compose: reading a replay candidate: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("compose: reading the replay candidates: %w", err)
	}
	return out, nil
}

// replayOne parses one stored original and writes whatever further
// participants it names, returning the verdict to record.
//
// A parse failure is a verdict, not an error: the payload is years-old
// provider output, and one message this parser cannot decompose must not stop
// the pass from reaching the rest.
func replayOne(ctx context.Context, tx pgx.Tx, c replayCandidate) (string, error) {
	if c.owner == "" {
		return replayNoOwner, nil
	}
	raw, err := decodeStoredOriginal(c.payload)
	if err != nil {
		return replayUnreadable, nil
	}
	var participants []connector.MessageParticipant
	switch c.source {
	case sourceGmail, sourceIMAP:
		participants, err = mailmap.ParticipantsOf(raw, c.owner)
	case sourceGCal:
		participants, err = gcal.ParticipantsOf(raw, c.owner)
	default:
		return replayUnreadable, nil
	}
	if err != nil {
		return replayUnreadable, nil
	}
	if len(participants) == 0 {
		return replayFoundNone, nil
	}
	if err := capture.StampFurtherParticipants(ctx, tx, c.activityID, c.kind, participants); err != nil {
		return "", err
	}
	return replayWroteParticipants, nil
}

// decodeStoredOriginal unwraps what the sink put in raw_capture.payload.
//
// The column is jsonb and a provider's original need not be JSON: capture
// stores a non-JSON payload (an RFC822 message) as a JSON *string*, and a JSON
// one (a calendar event resource) as itself. Both spellings arrive here, so
// the string case is unwrapped and anything else is handed on as it stands.
func decodeStoredOriginal(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, errors.New("compose: the stored original is empty")
	}
	if !strings.HasPrefix(strings.TrimSpace(string(payload)), `"`) {
		return payload, nil
	}
	var text string
	if err := json.Unmarshal(payload, &text); err != nil {
		return nil, fmt.Errorf("compose: unwrapping the stored original: %w", err)
	}
	return []byte(text), nil
}

// markReplayed records that this activity has been re-read, so no later pass
// selects it again.
func markReplayed(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, outcome string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO activity_participant_replay (workspace_id, activity_id, outcome)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2)
		ON CONFLICT (workspace_id, activity_id) DO NOTHING`,
		activityID, outcome); err != nil {
		return fmt.Errorf("compose: recording that an activity's participants were replayed: %w", err)
	}
	return nil
}
