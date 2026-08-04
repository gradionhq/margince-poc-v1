// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Writing signals from what the correspondence already says (SIG-F-3, the
// signals chapter's Producers section).
//
// The signal table has existed since migration 0047 with a card above it, and
// nothing in the product ever wrote a row — the only writer was the human-only
// POST /signals. So every account answered "no signal", including one whose own
// mail says the contract ended.
//
// This file is the deterministic half. `ghosted_thread` is a comparison rather
// than a judgment — the newest interaction is ours, nobody answered it, and the
// account is one worth chasing — so it needs no model and cannot be wrong about
// anything a reader cannot check for themselves.
//
// A signal is an OBSERVATION, so it is written directly under the write shape
// and attributed to this producer. What follows FROM one — a lifecycle change,
// a deal, a task — is a structural claim about the record and stages for a
// human. The line is not about confidence: a wrong signal is a card someone
// dismisses, a wrong structural write is a record someone has to find and undo.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ghostedThresholdDays (SIG-PARAM-6) is twice the no_reply suggestion's window,
// on purpose: the suggestion nudges, the signal records. A fortnight of silence
// after we spoke last is a fact about the relationship; a week is a reminder.
const ghostedThresholdDays = 14

// kindGhostedThread is spelled once: the fingerprint, the INSERT and the audit
// must agree, and three literals would drift the first time one was renamed.
const kindGhostedThread = "ghosted_thread"

// ghostedCandidate is one account the deterministic rule fired on.
type ghostedCandidate struct {
	OrganizationID ids.UUID
	ActivityID     ids.UUID
	Subject        string
	At             time.Time
}

// scanGhostedThreads finds the accounts whose newest interaction is ours and
// unanswered past the threshold, and which are still worth chasing.
//
// "Worth chasing" is the guard that keeps this from becoming noise: an
// unanswered fortnight on an account nobody is working is not an observation
// about a relationship, it is the absence of one.
func scanGhostedThreads(ctx context.Context, tx pgx.Tx, now time.Time) ([]ghostedCandidate, error) {
	cutoff := now.AddDate(0, 0, -ghostedThresholdDays)
	rows, err := tx.Query(ctx, `
		WITH newest AS (
			SELECT DISTINCT ON (l.organization_id)
			       l.organization_id, a.id, a.subject, a.direction, a.occurred_at
			  FROM activity a
			  JOIN activity_link l ON l.activity_id = a.id AND l.entity_type = 'organization'
			 WHERE a.archived_at IS NULL
			   AND a.kind IN ('email','call','meeting')
			   -- An interaction with no recorded direction cannot say who spoke
			   -- last, so it is skipped rather than guessed at — the same rule
			   -- PO-F-4 applies to the engagement state.
			   AND a.direction IS NOT NULL
			   AND a.occurred_at <= $1
			 ORDER BY l.organization_id, a.occurred_at DESC, a.id DESC
		)
		SELECT n.organization_id, n.id, coalesce(n.subject, ''), n.occurred_at
		  FROM newest n
		  JOIN organization o ON o.id = n.organization_id AND o.archived_at IS NULL
		 WHERE n.direction = 'outbound'
		   AND n.occurred_at < $2
		   AND (o.lifecycle IN ('prospect','opportunity','customer')
		        OR EXISTS (SELECT 1 FROM deal d
		                    WHERE d.organization_id = o.id AND d.status = 'open'
		                      AND d.archived_at IS NULL))`,
		now, cutoff)
	if err != nil {
		return nil, fmt.Errorf("scan ghosted threads: %w", err)
	}
	defer rows.Close()
	var out []ghostedCandidate
	for rows.Next() {
		var found ghostedCandidate
		if err := rows.Scan(&found.OrganizationID, &found.ActivityID, &found.Subject, &found.At); err != nil {
			return nil, err
		}
		out = append(out, found)
	}
	return out, rows.Err()
}

// signalFingerprint identifies a signal by what it fired ON, so a producer that
// runs hourly raises nothing new on an unchanged account, and a dismissal
// survives every later pass over the same evidence.
func signalFingerprint(kind string, orgID ids.UUID, evidence ...ids.UUID) string {
	sum := sha256.New()
	sum.Write([]byte(kind))
	sum.Write([]byte(orgID.String()))
	for _, id := range evidence {
		sum.Write([]byte(id.String()))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// WriteGhostedSignals is the deterministic producer pass: compose computes
// WHICH accounts the rule fired on — a question that spans activity,
// organization and deal, which is why it lives here — and the signals module
// writes the rows, because a module owns its own table.
//
// It returns how many signals it wrote, which is what a caller logs: a pass
// that wrote nothing on a busy workspace is worth noticing.
func WriteGhostedSignals(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, now time.Time) (int, error) {
	candidates, err := scanGhostedThreads(ctx, tx, now)
	if err != nil {
		return 0, err
	}
	written := 0
	for _, found := range candidates {
		days := int(now.Sub(found.At).Hours() / 24)
		raised, err := signals.RecordDerived(ctx, tx, wsID, signals.DerivedSignal{
			Kind:           kindGhostedThread,
			OrganizationID: found.OrganizationID,
			Summary:        fmt.Sprintf("We wrote %d days ago and nobody has answered.", days),
			Severity:       "warn",
			Fingerprint:    signalFingerprint(kindGhostedThread, found.OrganizationID, found.ActivityID),
			Evidence: []signals.DerivedEvidence{
				{Snippet: found.Subject, ActivityID: found.ActivityID},
			},
			Audit: map[string]any{paramKind: kindGhostedThread, "days_silent": days},
		}, now)
		if err != nil {
			return written, err
		}
		if raised {
			written++
		}
	}
	return written, nil
}
