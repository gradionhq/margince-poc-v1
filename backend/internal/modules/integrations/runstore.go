// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// insertRun writes the queued run. The live-run index does the work: a second
// trigger for the same subject, provider and inputs collides with a run
// already in flight, and the existing run is returned rather than a second
// purchase being made.
func (s *Store) insertRun(ctx context.Context, tx pgx.Tx, conn admittedConnection, in provider.QueueInput,
	snapshot provider.Snapshot, cats []provider.Category, fingerprint string) (string, bool, error) {

	snapJSON, err := json.Marshal(snapshot)
	if err != nil {
		return "", false, fmt.Errorf("integrations: freezing the configuration snapshot: %w", err)
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO provider_run
		  (subject_kind, person_id, provider, trigger, state, input_fingerprint,
		   external_correlation_id, connection_version, connection_epoch,
		   configuration_snapshot, requested_categories, requested_by)
		VALUES ('person', $1, $2, $3, 'queued', $4, gen_random_uuid(), $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING
		RETURNING id::text`,
		in.PersonID, in.Provider, string(in.Trigger), fingerprint,
		conn.version, conn.epoch, snapJSON, categoryStrings(cats), nullableUUID(in.RequestedBy)).Scan(&id)

	if errors.Is(err, pgx.ErrNoRows) {
		// The index refused it: a live run for these exact inputs already
		// exists. That is the duplicate-spend guard working, so hand back the
		// run in flight instead of buying the same answer again.
		var existing string
		if err := tx.QueryRow(ctx, `
			SELECT id::text FROM provider_run
			 WHERE person_id = $1 AND provider = $2 AND input_fingerprint = $3
			   AND state IN ('queued','submitting','in_progress','submission_unknown')`,
			in.PersonID, in.Provider, fingerprint).Scan(&existing); err != nil {
			return "", false, fmt.Errorf("integrations: resolving the live run: %w", err)
		}
		return existing, true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("integrations: writing the run: %w", err)
	}
	return id, false, nil
}

// insertSkipped records a run that never reached the provider, and why. It
// takes no reservation: nothing was spent, so nothing is held.
func (s *Store) insertSkipped(ctx context.Context, tx pgx.Tx, conn admittedConnection, in provider.QueueInput,
	snapshot provider.Snapshot, cats []provider.Category, reason provider.SkipReason) (provider.Run, error) {

	snapJSON, err := json.Marshal(snapshot)
	if err != nil {
		return provider.Run{}, fmt.Errorf("integrations: freezing the configuration snapshot: %w", err)
	}
	var id string
	// A skipped run carries a unique fingerprint so it never occupies the
	// live-run index: it is a record of a decision, not work in flight, and
	// must not block the next legitimate attempt.
	err = tx.QueryRow(ctx, `
		INSERT INTO provider_run
		  (subject_kind, person_id, provider, trigger, state, skip_reason,
		   input_fingerprint, external_correlation_id, connection_version,
		   connection_epoch, configuration_snapshot, requested_categories,
		   requested_by, completed_at)
		VALUES ('person', $1, $2, $3, 'skipped', $4, 'skipped:' || gen_random_uuid()::text,
		        gen_random_uuid(), $5, $6, $7, $8, $9, now())
		RETURNING id::text`,
		in.PersonID, in.Provider, string(in.Trigger), string(reason),
		conn.version, conn.epoch, snapJSON, categoryStrings(cats),
		nullableUUID(in.RequestedBy)).Scan(&id)
	if err != nil {
		return provider.Run{}, fmt.Errorf("integrations: recording the skipped run: %w", err)
	}
	return s.readRun(ctx, tx, id)
}

// markSkipped turns an already-inserted run into a skip, for the refusals
// that can only be discovered after the row exists (the reservation).
func (s *Store) markSkipped(ctx context.Context, tx pgx.Tx, runID string, reason provider.SkipReason) error {
	if _, err := tx.Exec(ctx, `
		UPDATE provider_run
		   SET state = 'skipped', skip_reason = $2, completed_at = now(),
		       input_fingerprint = 'skipped:' || gen_random_uuid()::text
		 WHERE id = $1`, runID, string(reason)); err != nil {
		return fmt.Errorf("integrations: recording the skip: %w", err)
	}
	return nil
}

// readRun loads one run and its reservations.
func (s *Store) readRun(ctx context.Context, tx pgx.Tx, runID string) (provider.Run, error) {
	var r provider.Run
	var personID, skipReason, safeCode *string
	var snapJSON []byte
	var cats []string
	err := tx.QueryRow(ctx, `
		SELECT id::text, subject_kind, person_id::text, provider, trigger, state,
		       skip_reason, claims_unwritten, connection_version,
		       configuration_snapshot, requested_categories, last_safe_status_code,
		       submitted_at, completed_at, created_at, updated_at
		  FROM provider_run WHERE id = $1`, runID).
		Scan(&r.ID, &r.SubjectKind, &personID, &r.Provider, &r.Trigger, &r.State,
			&skipReason, &r.ClaimsUnwritten, &r.ConnectionVersion,
			&snapJSON, &cats, &safeCode,
			&r.SubmittedAt, &r.CompletedAt, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return provider.Run{}, apperrors.ErrNotFound
	}
	if err != nil {
		return provider.Run{}, fmt.Errorf("integrations: reading the run: %w", err)
	}
	if personID != nil {
		r.PersonID = *personID
	}
	if skipReason != nil {
		r.SkipReason = provider.SkipReason(*skipReason)
	}
	if safeCode != nil {
		r.SafeStatusCode = *safeCode
	}
	r.RequestedCategories = categoriesFrom(cats)
	if err := json.Unmarshal(snapJSON, &r.Snapshot); err != nil {
		return provider.Run{}, fmt.Errorf("integrations: reading the frozen snapshot: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT pool, reserved_credits, actual_credits
		  FROM provider_run_reservation WHERE run_id = $1 ORDER BY pool`, runID)
	if err != nil {
		return provider.Run{}, fmt.Errorf("integrations: reading the reservations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var res provider.Reservation
		if err := rows.Scan(&res.Pool, &res.Reserved, &res.Actual); err != nil {
			return provider.Run{}, fmt.Errorf("integrations: scanning a reservation: %w", err)
		}
		r.Reservations = append(r.Reservations, res)
	}
	if err := rows.Err(); err != nil {
		return provider.Run{}, fmt.Errorf("integrations: reading the reservations: %w", err)
	}
	return r, nil
}

// GetRun reads one run for a subject. The person gate is what authorizes it:
// a run is a fact about that person, so seeing it requires seeing them.
func (s *Store) GetRun(ctx context.Context, personID, runID string) (provider.Run, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return provider.Run{}, err
	}
	var out provider.Run
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		// Existence-hiding: a run belonging to a person this caller cannot
		// see answers 404, never 403.
		if err := auth.EnsureVisible(ctx, tx, "person", uuidOf(&personID)); err != nil {
			return err
		}
		run, err := s.readRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if run.PersonID != personID {
			return apperrors.ErrNotFound
		}
		out = run
		return nil
	})
	if err != nil {
		return provider.Run{}, err
	}
	return out, nil
}

func nullableUUID(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
