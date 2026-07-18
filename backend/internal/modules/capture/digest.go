// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The morning digest builder (CAP-DDL-6, ADR-0063): the nightly suite's
// last pass assembles, per connected user, what capture did in the last
// day and what awaits review — every number a count of persisted rows at
// build time, stored as the pre-assembled payload one indexed GET serves
// (CAP-WIRE-6). Reads cross module tables freely (reads are governed by
// RLS, not ownership); writes only capture_digest.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// DigestPayload is the stored CAP-DDL-6 payload — the wire shape verbatim.
type DigestPayload struct {
	Date        string          `json:"date"`
	GeneratedAt time.Time       `json:"generated_at"`
	Capture     DigestCapture   `json:"capture"`
	Review      DigestReview    `json:"review"`
	Connectors  []DigestConnRow `json:"connectors"`
}

// DigestCapture is what landed in the window.
type DigestCapture struct {
	MessagesSynced       int `json:"messages_synced"`
	ActivitiesCreated    int `json:"activities_created"`
	PeopleCreated        int `json:"people_created"`
	OrganizationsCreated int `json:"organizations_created"`
}

// DigestReview is what awaits the human.
type DigestReview struct {
	DedupeOpen       int            `json:"dedupe_open"`
	ApprovalsPending int            `json:"approvals_pending"`
	Classify         DigestClassify `json:"classify"`
}

// DigestClassify is the window's label tally.
type DigestClassify struct {
	Commitments int `json:"commitments"`
	Meetings    int `json:"meetings"`
	Noise       int `json:"noise"`
}

// DigestConnRow is one connector's health line.
type DigestConnRow struct {
	Provider       string     `json:"provider"`
	Status         string     `json:"status"`
	LastSyncedAt   *time.Time `json:"last_synced_at,omitempty"`
	LastErrorClass *string    `json:"last_sync_error_class,omitempty"`
}

// BuildDigests assembles one digest per user holding a live connection in
// the current workspace, for digestDate covering the window since the
// previous build day. Idempotent per (user, day): a re-run replaces the
// day's payload (the counts are as-of-now truths, not increments).
func (r *Registry) BuildDigests(ctx context.Context, digestDate time.Time) error {
	day := digestDate.Format(time.DateOnly)
	since := digestDate.AddDate(0, 0, -1)
	return database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		users, err := connectedUsers(ctx, tx)
		if err != nil {
			return err
		}
		for _, userID := range users {
			payload, err := r.buildDigestPayload(ctx, tx, userID, day, since)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("capture: encoding digest: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO capture_digest (workspace_id, user_id, digest_date, payload)
				VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)
				ON CONFLICT (workspace_id, user_id, digest_date) DO UPDATE SET payload = EXCLUDED.payload`,
				userID, day, raw); err != nil {
				return fmt.Errorf("capture: storing digest: %w", err)
			}
		}
		return nil
	})
}

// connectedUsers lists the workspace's users with a live capture
// connection — the digest audience.
func connectedUsers(ctx context.Context, tx pgx.Tx) ([]ids.UUID, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT user_id FROM capture_connection
		WHERE status IN ('connected','error') AND archived_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("capture: listing digest users: %w", err)
	}
	defer rows.Close()
	var out []ids.UUID
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *Registry) buildDigestPayload(ctx context.Context, tx pgx.Tx, userID ids.UUID, day string, since time.Time) (DigestPayload, error) {
	p := DigestPayload{Date: day, GeneratedAt: r.now().UTC()}
	// Workspace-level truths: what the pipeline did and what awaits review
	// is shared work, not per-owner arithmetic — the same numbers for every
	// digest reader in the workspace.
	err := tx.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM activity WHERE captured_by LIKE 'connector:%' AND kind = 'email' AND created_at >= $1),
		  (SELECT count(*) FROM person WHERE captured_by LIKE 'connector:%' AND created_at >= $1),
		  (SELECT count(*) FROM organization WHERE captured_by LIKE 'connector:%' AND created_at >= $1),
		  (SELECT count(*) FROM dedupe_candidate WHERE disposition = 'open' AND archived_at IS NULL),
		  (SELECT count(*) FROM approval WHERE status = 'pending'),
		  (SELECT count(*) FROM activity WHERE capture_label = 'commitment' AND capture_labeled_at >= $1),
		  (SELECT count(*) FROM activity WHERE capture_label = 'meeting' AND capture_labeled_at >= $1),
		  (SELECT count(*) FROM activity WHERE capture_label = 'noise' AND capture_labeled_at >= $1)`,
		since).Scan(
		&p.Capture.ActivitiesCreated, &p.Capture.PeopleCreated, &p.Capture.OrganizationsCreated,
		&p.Review.DedupeOpen, &p.Review.ApprovalsPending,
		&p.Review.Classify.Commitments, &p.Review.Classify.Meetings, &p.Review.Classify.Noise)
	if err != nil {
		return DigestPayload{}, fmt.Errorf("capture: digest counts: %w", err)
	}
	// Synced == landed: the capture key makes every landed message one row.
	p.Capture.MessagesSynced = p.Capture.ActivitiesCreated

	// The connector health strip is the USER's own connections (RC-8).
	rows, err := tx.Query(ctx, `
		SELECT c.provider, c.status, s.last_synced_at, s.last_error_class
		FROM capture_connection c
		LEFT JOIN capture_sync_state s ON s.connection_id = c.id
		WHERE c.user_id = $1 AND c.archived_at IS NULL
		ORDER BY c.provider`, userID)
	if err != nil {
		return DigestPayload{}, fmt.Errorf("capture: digest connector strip: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c DigestConnRow
		if err := rows.Scan(&c.Provider, &c.Status, &c.LastSyncedAt, &c.LastErrorClass); err != nil {
			return DigestPayload{}, err
		}
		p.Connectors = append(p.Connectors, c)
	}
	if p.Connectors == nil {
		p.Connectors = []DigestConnRow{}
	}
	return p, rows.Err()
}

// ReadDigest serves the calling user's digest: the requested day, or the
// latest when day is zero. No digest yet answers (nil, nil) — the
// transport's honest 404.
func (r *Registry) ReadDigest(ctx context.Context, userID ids.UUID, day *time.Time) (*DigestPayload, error) {
	var raw []byte
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT payload FROM capture_digest WHERE user_id = $1 ORDER BY digest_date DESC LIMIT 1`, userID)
		if day != nil {
			row = tx.QueryRow(ctx,
				`SELECT payload FROM capture_digest WHERE user_id = $1 AND digest_date = $2`,
				userID, day.Format(time.DateOnly))
		}
		err := row.Scan(&raw)
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("capture: reading digest: %w", err)
	}
	if raw == nil {
		// Absence IS the answer: no digest has been built yet — the
		// transport's honest 404, not an error.
		return nil, nil //nolint:nilnil // deliberate: state "none" precedes the first nightly build
	}
	var p DigestPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("capture: decoding stored digest: %w", err)
	}
	return &p, nil
}
