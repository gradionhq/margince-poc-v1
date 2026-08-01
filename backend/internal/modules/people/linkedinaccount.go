// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The member's own LinkedIn account (ADR-0078 §2.1b).
//
// Onboarding asks for a profile URL and an authorization; this is where that
// answer lives, and where the integrations tab reads it back so a member can
// see and correct what the CRM believes about their own account.
//
// It is always the CALLER's row. There is no path here to read or write
// somebody else's LinkedIn account: a colleague's professional network is
// theirs, and an admin has no more business editing that URL than editing
// their personal address book.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// LinkedInAccount is what one member's LinkedIn connection amounts to.
type LinkedInAccount struct {
	ProfileURL  *string
	ConnectedAt *time.Time
	// Connections is how many ghosts this member's import produced, so the tab
	// can say what the authorization actually yielded rather than only that it
	// happened.
	Connections int
	// Confirmed and Suggested are this member's TOTAL match state, not one
	// import's delta.
	Confirmed int
	Suggested int
}

// MyLinkedInMatchTotals counts where this member's whole network stands.
//
// The import reports what THAT pass decided, which is the honest number for a
// progress line and the wrong one for a status card: the matcher only
// considers ghosts nobody has decided on, so a second import of the same file
// truthfully reports zero new matches while twenty-six matches sit in the
// database. A card labelled "Matched to a contact" showing 0 in that state is
// simply wrong.
func (s *Store) MyLinkedInMatchTotals(ctx context.Context) (confirmed, suggested int, err error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return 0, 0, apperrors.ErrPermissionDenied
	}
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE match_status = 'confirmed'),
			       count(*) FILTER (WHERE match_status = 'suggested')
			  FROM linkedin_connection
			 WHERE owner_user_id = $1 AND tombstoned_at IS NULL`, actor.UserID).
			Scan(&confirmed, &suggested)
	})
	return confirmed, suggested, err
}

// GetMyLinkedInAccount reads the caller's own row. A member who has never been
// through the act has no row, and that is not an error — it is the honest
// "not connected" the tab renders.
func (s *Store) GetMyLinkedInAccount(ctx context.Context) (LinkedInAccount, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return LinkedInAccount{}, apperrors.ErrPermissionDenied
	}
	var out LinkedInAccount
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT a.profile_url, a.connected_at,
			       (SELECT count(*) FROM linkedin_connection c
			         WHERE c.owner_user_id = $1 AND c.tombstoned_at IS NULL)
			  FROM linkedin_account a
			 WHERE a.user_id = $1`, actor.UserID).
			Scan(&out.ProfileURL, &out.ConnectedAt, &out.Connections)
		if errors.Is(err, pgx.ErrNoRows) {
			// No row yet. The connection count is still worth reading: a
			// member may have uploaded an export from the settings tab without
			// ever going through the onboarding act.
			return tx.QueryRow(ctx, `
				SELECT count(*) FROM linkedin_connection
				 WHERE owner_user_id = $1 AND tombstoned_at IS NULL`, actor.UserID).
				Scan(&out.Connections)
		}
		return err
	})
	if err != nil {
		return LinkedInAccount{}, fmt.Errorf("people: reading the caller's LinkedIn account: %w", err)
	}
	return out, nil
}

// SaveMyLinkedInAccountInput is the member's own answer about themselves.
type SaveMyLinkedInAccountInput struct {
	// ProfileURL empty CLEARS the stored URL — a member correcting a typo by
	// emptying the field means "I do not want this recorded", not "leave it".
	ProfileURL string
	// Connected records the authorization. False leaves an existing
	// connected_at alone rather than revoking it: this endpoint edits a
	// profile, and disconnecting is its own deliberate act.
	Connected bool
}

// SaveMyLinkedInAccount upserts the caller's own row.
func (s *Store) SaveMyLinkedInAccount(ctx context.Context, in SaveMyLinkedInAccountInput) (LinkedInAccount, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return LinkedInAccount{}, apperrors.ErrPermissionDenied
	}
	trimmed := strings.TrimSpace(in.ProfileURL)
	if err := validateLinkedInProfileURL(trimmed); err != nil {
		return LinkedInAccount{}, err
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := readLinkedInAccountTx(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO linkedin_account (workspace_id, user_id, profile_url, connected_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, NULLIF($2, ''), CASE WHEN $3 THEN now() END)
			ON CONFLICT (workspace_id, user_id) DO UPDATE SET
			    profile_url = NULLIF($2, ''),
			    connected_at = CASE
			      WHEN $3 THEN coalesce(linkedin_account.connected_at, now())
			      ELSE linkedin_account.connected_at END,
			    updated_at = now()`, actor.UserID, trimmed, in.Connected)
		if err != nil {
			return err
		}
		after, err := readLinkedInAccountTx(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		// The write shape, in one transaction with the row. Consent to read a
		// professional network is exactly the kind of fact that must be
		// auditable — a member has to be able to ask when they agreed and what
		// changed, and an unaudited consent write cannot answer either.
		auditID, err := storekit.Audit(ctx, tx, "update", "user", actor.UserID,
			auditLinkedInAccount(before), auditLinkedInAccount(after))
		if err != nil {
			return err
		}
		// The URL itself stays OUT of the payload: it is the member's own
		// identifier, and a subscriber needs to know the authorization moved,
		// not what their LinkedIn address is.
		return storekit.EmitEvent(ctx, tx, auditID, actor.UserID,
			crmcontracts.PublicEventLinkedinAccountChanged{
				Connected:     after.ConnectedAt != nil,
				HasProfileUrl: after.ProfileURL != nil,
			})
	})
	if err != nil {
		return LinkedInAccount{}, fmt.Errorf("people: saving the caller's LinkedIn account: %w", err)
	}
	return s.GetMyLinkedInAccount(ctx)
}

// validateLinkedInProfileURL accepts an empty value (the clear) and otherwise
// insists on an absolute http(s) URL. A member who pastes their headline
// instead of their address should be told so, not have it stored and rendered
// as a broken link.
func validateLinkedInProfileURL(s string) error {
	if s == "" {
		return nil
	}
	parsed, err := url.Parse(s)
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return &DedupeInputError{
			Field: "profile_url",
			Msg:   "a LinkedIn profile URL looks like https://www.linkedin.com/in/your-name",
		}
	}
	return nil
}

// readLinkedInAccountTx reads one member's row inside an open transaction, for
// the before/after images the audit row carries.
func readLinkedInAccountTx(ctx context.Context, tx pgx.Tx, user ids.UUID) (LinkedInAccount, error) {
	var out LinkedInAccount
	err := tx.QueryRow(ctx,
		`SELECT profile_url, connected_at FROM linkedin_account WHERE user_id = $1`, user).
		Scan(&out.ProfileURL, &out.ConnectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return LinkedInAccount{}, nil
	}
	return out, err
}

// auditLinkedInAccount is the field image the audit row stores. The profile URL
// is recorded here and not in the event: the audit log is the member's own
// history of their own account, while the event fans out to subscribers.
func auditLinkedInAccount(a LinkedInAccount) map[string]any {
	return map[string]any{
		"profile_url":  a.ProfileURL,
		"connected_at": a.ConnectedAt,
	}
}
