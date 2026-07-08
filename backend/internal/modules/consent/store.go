// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, now: time.Now}
}

type Purpose struct {
	ID                  ids.PurposeID
	WorkspaceID         ids.WorkspaceID
	Key                 string
	Label               string
	RequiresDoubleOptIn bool
	CreatedAt           time.Time
}

type State struct {
	PurposeID              ids.PurposeID
	PurposeKey             string
	State                  string
	LawfulBasis            *string
	DoubleOptInConfirmedAt *time.Time
	UpdatedAt              *time.Time
}

type ProofEvent struct {
	// ID is the consent_event proof row's id — an append-only ledger
	// entry, not a first-class entity in the kernel vocabulary, so it
	// stays untyped.
	ID          ids.UUID
	PurposeID   ids.PurposeID
	NewState    string
	LawfulBasis *string
	Source      *string
	CapturedBy  string
	OccurredAt  time.Time
}

// ListPurposes returns the workspace catalog. The catalog is
// config-sized (a handful of rows); the page shape exists for contract
// symmetry, not because anyone paginates it.
func (s *Store) ListPurposes(ctx context.Context) ([]Purpose, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return nil, err
	}
	var out []Purpose
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, workspace_id, key, label, requires_double_opt_in, created_at
			FROM consent_purpose WHERE archived_at IS NULL ORDER BY key`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Purpose
			if err := rows.Scan(&p.ID, &p.WorkspaceID, &p.Key, &p.Label, &p.RequiresDoubleOptIn, &p.CreatedAt); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// CreatePurpose defines one purpose. Purposes are compliance
// configuration — gated like pipeline config until the features/04
// matrix names a consent-config object (filed as feedback).
func (s *Store) CreatePurpose(ctx context.Context, key, label string, requiresDOI bool) (Purpose, error) {
	if err := auth.Require(ctx, "pipeline", principal.ActionCreate); err != nil {
		return Purpose{}, err
	}
	key = strings.TrimSpace(strings.ToLower(key))
	if key == "" || strings.TrimSpace(label) == "" {
		return Purpose{}, &ValidationError{Field: "key", Reason: "key and label are required"}
	}
	var p Purpose
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO consent_purpose (workspace_id, key, label, requires_double_opt_in)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)
			RETURNING id, workspace_id, key, label, requires_double_opt_in, created_at`,
			key, label, requiresDOI).
			Scan(&p.ID, &p.WorkspaceID, &p.Key, &p.Label, &p.RequiresDoubleOptIn, &p.CreatedAt)
		if constraint, ok := storekit.UniqueViolation(err); ok && constraint == "consent_purpose_key_unique" {
			return fmt.Errorf("purpose %q: %w", key, apperrors.ErrConflict)
		}
		return err
	})
	return p, err
}

// PersonConsent reads one person's per-purpose state plus the full
// proof log (Art. 7 demonstrability). The person is the read target —
// row scope gates the whole answer.
func (s *Store) PersonConsent(ctx context.Context, personID ids.PersonID) ([]State, []ProofEvent, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return nil, nil, err
	}
	var states []State
	var events []ProofEvent
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", personID.UUID); err != nil {
			return err
		}
		// Every tracked purpose appears — absent rows read as the honest
		// 'unknown', never as an implicit grant.
		rows, err := tx.Query(ctx, `
			SELECT cp.id, cp.key, coalesce(pc.state, 'unknown'), pc.lawful_basis, pc.captured_at
			FROM consent_purpose cp
			LEFT JOIN person_consent pc ON pc.purpose_id = cp.id AND pc.person_id = $1
			WHERE cp.archived_at IS NULL
			ORDER BY cp.key`, personID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var st State
			if err := rows.Scan(&st.PurposeID, &st.PurposeKey, &st.State, &st.LawfulBasis, &st.UpdatedAt); err != nil {
				rows.Close()
				return err
			}
			states = append(states, st)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		rows, err = tx.Query(ctx, `
			SELECT id, purpose_id, new_state, lawful_basis, source, captured_by, captured_at
			FROM consent_event WHERE person_id = $1 ORDER BY captured_at DESC, id DESC`, personID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ev ProofEvent
			if err := rows.Scan(&ev.ID, &ev.PurposeID, &ev.NewState, &ev.LawfulBasis, &ev.Source, &ev.CapturedBy, &ev.OccurredAt); err != nil {
				return err
			}
			events = append(events, ev)
		}
		return rows.Err()
	})
	return states, events, err
}

type RecordInput struct {
	PersonID         ids.PersonID
	PurposeID        ids.PurposeID
	NewState         string // granted | withdrawn
	LawfulBasis      *string
	Source           *string
	DoubleOptInToken *string
	// PolicyText/PolicyVersion carry the CaptureConsent passthrough of a
	// capture surface (feedback/14): the EXACT wording and version shown
	// to the subject, stored verbatim on the proof row (Art 7(1)
	// demonstrability). Nil keeps the API-surface defaults.
	PolicyText    *string
	PolicyVersion *string
	// NeverOverrideExisting is the anonymous-capture rule: a public
	// surface asserting "granted" must not flip a decision already on
	// record — above all a WITHDRAWAL, which an attacker knowing only an
	// email address could otherwise anonymously reverse. When set, an
	// existing different state is left untouched and returned as-is
	// (silently: refusing loudly would make the surface a consent-state
	// oracle).
	NeverOverrideExisting bool
}

// Record sets one person×purpose state and appends the proof row —
// audited (consent_grant/consent_withdraw) and emitted (consent.changed)
// in the same transaction as every other mutation. Re-asserting the
// current state is idempotent: no second proof row, no second event.
func (s *Store) Record(ctx context.Context, in RecordInput) (State, error) {
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
		return State{}, err
	}
	if _, err := ParseRecordableState(in.NewState); err != nil {
		return State{}, err
	}
	actor, _ := principal.Actor(ctx)

	var out State
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", in.PersonID.UUID); err != nil {
			return err
		}
		purposeKey, requiresDOI, err := loadConsentPurpose(ctx, tx, in.PurposeID)
		if err != nil {
			return err
		}
		var current string
		err = tx.QueryRow(ctx,
			`SELECT state FROM person_consent WHERE person_id = $1 AND purpose_id = $2`,
			in.PersonID, in.PurposeID).Scan(&current)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if current == in.NewState {
			out = State{PurposeID: in.PurposeID, PurposeKey: purposeKey, State: current, LawfulBasis: in.LawfulBasis}
			return nil // idempotent re-assertion: no proof row, no event, no fresh token demanded
		}
		if in.NeverOverrideExisting && current != "" {
			out = State{PurposeID: in.PurposeID, PurposeKey: purposeKey, State: current}
			return nil // the decision on record stands; an anonymous capture cannot flip it
		}

		doiConfirmedAt, err := s.resolveDOIConfirmation(ctx, tx, in, requiresDOI)
		if err != nil {
			return err
		}

		capturedAt := s.now().UTC()
		if err := upsertConsentWithProof(ctx, tx, in, doiConfirmedAt, capturedAt, actor.ID); err != nil {
			return err
		}

		action := "consent_grant"
		if ConsentState(in.NewState) == StateWithdrawn {
			action = "consent_withdraw"
		}
		auditID, err := storekit.Audit(ctx, tx, action, "person", in.PersonID.UUID, map[string]any{"state": stateOrUnknown(current)}, map[string]any{
			"purpose": purposeKey, "state": in.NewState,
		})
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "consent.changed", "person", in.PersonID.UUID, map[string]any{
			"purpose_id": in.PurposeID, "purpose": purposeKey, "new_state": in.NewState,
		}); err != nil {
			return err
		}
		out = State{PurposeID: in.PurposeID, PurposeKey: purposeKey, State: in.NewState,
			LawfulBasis: in.LawfulBasis, DoubleOptInConfirmedAt: doiConfirmedAt, UpdatedAt: &capturedAt}
		return nil
	})
	return out, err
}

// loadConsentPurpose resolves the target purpose's key and DOI flag; an
// unknown or archived purpose is 404.
func loadConsentPurpose(ctx context.Context, tx pgx.Tx, purposeID ids.PurposeID) (key string, requiresDOI bool, err error) {
	err = tx.QueryRow(ctx,
		`SELECT key, requires_double_opt_in FROM consent_purpose WHERE id = $1 AND archived_at IS NULL`,
		purposeID).Scan(&key, &requiresDOI)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("purpose %s: %w", purposeID, apperrors.ErrNotFound)
	}
	if err != nil {
		return "", false, err
	}
	return key, requiresDOI, nil
}

// resolveDOIConfirmation enforces the German email norm: a DOI purpose's
// grant is only effective once the double-opt-in round-trip confirmed.
// The token must be one this server issued (hash-matched, unconsumed,
// unexpired) — consuming it here makes the confirmation single-use and
// unfabricatable rather than stored half-true. Non-DOI paths return nil.
func (s *Store) resolveDOIConfirmation(ctx context.Context, tx pgx.Tx, in RecordInput, requiresDOI bool) (*time.Time, error) {
	if ConsentState(in.NewState) != StateGranted || !requiresDOI {
		return nil, nil
	}
	if in.DoubleOptInToken == nil || *in.DoubleOptInToken == "" {
		return nil, &ValidationError{Field: "double_opt_in_token", Reason: "purpose requires a confirmed double opt-in"}
	}
	confirmed, err := s.consumeDOIToken(ctx, tx, in.PersonID, in.PurposeID, *in.DoubleOptInToken)
	if err != nil {
		return nil, err
	}
	return &confirmed, nil
}

// upsertConsentWithProof writes the state row and appends the immutable
// proof row — one concept: the current state is always backed by an
// append-only consent_event that says when, how, and by whom.
func upsertConsentWithProof(ctx context.Context, tx pgx.Tx, in RecordInput, doiConfirmedAt *time.Time, capturedAt time.Time, actorID string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO person_consent (workspace_id, person_id, purpose_id, state, lawful_basis, captured_at, source)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, $5, $6)
		ON CONFLICT (workspace_id, person_id, purpose_id)
		DO UPDATE SET state = EXCLUDED.state, lawful_basis = EXCLUDED.lawful_basis,
		              captured_at = EXCLUDED.captured_at, source = EXCLUDED.source`,
		in.PersonID, in.PurposeID, in.NewState, in.LawfulBasis, capturedAt, in.Source); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO consent_event (workspace_id, person_id, purpose_id, new_state, lawful_basis, source,
		                           policy_text, policy_version, double_opt_in_confirmed_at, captured_at, captured_by)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, $2, $3, $4, coalesce($5, 'api'), coalesce($6, 'recorded via API'), coalesce($7, 'v1'), $8, $9, $10)`,
		in.PersonID, in.PurposeID, in.NewState, in.LawfulBasis, in.Source,
		in.PolicyText, in.PolicyVersion, doiConfirmedAt, capturedAt, actorID)
	return err
}

func stateOrUnknown(state string) string {
	if state == "" {
		return "unknown"
	}
	return state
}

// ValidationError maps to a 422 at the transport.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return "consent: " + e.Field + ": " + e.Reason }
