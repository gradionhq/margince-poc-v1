// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

// The buyer-facing preference center + RFC 8058 one-click unsubscribe
// (B-E11.32): the no-login surface over THIS module's consent engine. A
// recipient reaches it through an unguessable preference_token carried in
// the List-Unsubscribe URL; the token resolves to (workspace, person)
// before any session exists, and every choice rides the normal consent
// write shape (proof row + audit + consent.changed) with a distinct
// `preference_center` source. The token holder proved control of the
// mailbox by receiving the token, so — unlike the fully-anonymous booking
// form — an explicit re-grant is the data subject's own opt-in, not a
// consent hijack; a withdrawal always goes through.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// PurposeTransactional is the one locked purpose: operational mail about a
// live deal has a lawful lane that a marketing opt-out must not silence
// (data-model §3.4 per-purpose separation; UC-E11-07 step 6). The
// preference center refuses to change it.
const PurposeTransactional = "transactional"

// LockedPurpose reports whether a purpose may not be changed from the
// public preference surface. Locked purposes also carry no unsubscribe
// header — there is nothing to unsubscribe from.
func LockedPurpose(key string) bool {
	return strings.TrimSpace(strings.ToLower(key)) == PurposeTransactional
}

// PreferenceRef is a token's resolution: which workspace, whose consent.
type PreferenceRef struct {
	WorkspaceID ids.UUID
	PersonID    ids.UUID
}

// PurposeChoice is one row of the preference center: the purpose, the
// recipient's current state, and whether it is locked.
type PurposeChoice struct {
	Key    string
	Label  string
	State  string
	Locked bool
}

// ResolvePreferenceToken answers the token→tenant lookup the public
// middleware binds the workspace from. preference_token is deliberately
// outside RLS (it IS the resolver — 0048); an unknown or revoked token
// reads as absent.
func (s *Store) ResolvePreferenceToken(ctx context.Context, token string) (PreferenceRef, error) {
	var ref PreferenceRef
	err := database.WithInfraTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT workspace_id, person_id FROM preference_token WHERE token = $1 AND revoked_at IS NULL`,
			token).Scan(&ref.WorkspaceID, &ref.PersonID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	})
	if err != nil {
		return PreferenceRef{}, err
	}
	return ref, nil
}

// PreferenceTokenForEmail resolves a recipient address to their live
// preference token, minting one lazily on first use, so the send path can
// build the List-Unsubscribe URL. An address no person carries yields no
// token (found=false): the send would fail the consent gate anyway, so
// nothing is disclosed. RLS scopes the email lookup to the workspace.
func (s *Store) PreferenceTokenForEmail(ctx context.Context, email string) (token string, found bool, err error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return "", false, err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var personID ids.UUID
		lookup := tx.QueryRow(ctx, `
			SELECT pe.person_id
			FROM person_email pe
			JOIN person p ON p.id = pe.person_id AND p.archived_at IS NULL
			WHERE lower(pe.email) = $1
			LIMIT 1`, email).Scan(&personID)
		if errors.Is(lookup, pgx.ErrNoRows) {
			return nil // not a known recipient in this workspace: no token, no header
		}
		if lookup != nil {
			return lookup
		}
		found = true
		token, err = ensurePreferenceTokenTx(ctx, tx, personID)
		return err
	})
	if err != nil {
		return "", false, err
	}
	return token, found, nil
}

// ensurePreferenceTokenTx returns the person's live token, minting one if
// none exists. The partial unique index guarantees at most one live token
// per person; a concurrent minter that wins the INSERT is read back rather
// than duplicated.
func ensurePreferenceTokenTx(ctx context.Context, tx pgx.Tx, personID ids.UUID) (string, error) {
	var token string
	err := tx.QueryRow(ctx, `
		SELECT token FROM preference_token
		WHERE workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
		  AND person_id = $1 AND revoked_at IS NULL`, personID).Scan(&token)
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	fresh, err := newPreferenceToken()
	if err != nil {
		return "", err
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO preference_token (workspace_id, person_id, token)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2)
		ON CONFLICT (workspace_id, person_id) WHERE revoked_at IS NULL DO NOTHING
		RETURNING token`, personID, fresh).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent send minted it first — read the winner.
		return token, tx.QueryRow(ctx, `
			SELECT token FROM preference_token
			WHERE workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
			  AND person_id = $1 AND revoked_at IS NULL`, personID).Scan(&token)
	}
	return token, err
}

func newPreferenceToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("consent: preference token entropy: %w", err)
	}
	return "pref_" + base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// PublicPurposeStates is the preference center's read: every tracked
// purpose with the recipient's current state and its locked flag. The
// system principal the public middleware binds is unbounded, so the read
// answers for the resolved person; a caller without the token never
// reaches this method.
func (s *Store) PublicPurposeStates(ctx context.Context, personID ids.UUID) ([]PurposeChoice, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return nil, err
	}
	var out []PurposeChoice
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", personID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT cp.key, cp.label, coalesce(pc.state, 'unknown')
			FROM consent_purpose cp
			LEFT JOIN person_consent pc ON pc.purpose_id = cp.id AND pc.person_id = $1
			WHERE cp.archived_at IS NULL
			ORDER BY cp.key`, personID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c PurposeChoice
			if err := rows.Scan(&c.Key, &c.Label, &c.State); err != nil {
				return err
			}
			c.Locked = LockedPurpose(c.Key)
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// PublicSetConsent records one per-purpose choice made from the preference
// center. A locked purpose is refused; every other change rides Record —
// same proof row, audit, and consent.changed event as any other consent
// write — with a distinct `preference_center` source. The mailbox-proving
// token holder is the data subject, so NeverOverrideExisting is NOT set:
// an explicit re-grant is their own opt-in, and a withdrawal always
// applies.
func (s *Store) PublicSetConsent(ctx context.Context, personID ids.UUID, purposeKey, newState string, wording *string) (State, error) {
	purposeKey = strings.TrimSpace(strings.ToLower(purposeKey))
	if LockedPurpose(purposeKey) {
		return State{}, &ValidationError{Field: "purpose_key", Reason: "transactional consent is locked and cannot be changed from the preference center"}
	}
	purposeID, err := s.purposeByKey(ctx, purposeKey)
	if err != nil {
		return State{}, err
	}
	source := "preference_center"
	return s.Record(ctx, RecordInput{
		PersonID:   personID,
		PurposeID:  purposeID,
		NewState:   newState,
		Source:     &source,
		PolicyText: wording,
	})
}

// purposeByKey resolves a purpose key to its id within the bound
// workspace; an unknown key is a client fault, not a 500.
func (s *Store) purposeByKey(ctx context.Context, key string) (ids.UUID, error) {
	var id ids.UUID
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT id FROM consent_purpose WHERE key = $1 AND archived_at IS NULL`, key).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return &ValidationError{Field: "purpose_key", Reason: "not a tracked consent purpose"}
		}
		return err
	})
	return id, err
}
