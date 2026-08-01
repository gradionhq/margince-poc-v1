// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Which accounts a member's imported network reaches (ADR-0078 §2.1b).
//
// This is the ONE question the ghosts exist to answer, and until now nothing
// asked it. A member could see that 5,064 connections had been imported and
// that 267 of them resolved to accounts, and could not see WHICH accounts —
// so the import produced a number rather than an answer.
//
// It is also the half of the feature that works on a one-person workspace. The
// company page's connections card asks which COLLEAGUE knows an account, which
// has no content when there is one member; this asks whether the member's own
// network reaches it, which has content immediately.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// LinkedInReachAccount is one account this member's network reaches.
type LinkedInReachAccount struct {
	OrganizationID ids.UUID
	DisplayName    string
	Connections    int
	// ContactsOnFile counts the CONFIRMED matches only. The gap between it and
	// Connections is the answer the import was for: people you know at this
	// account who are not in the CRM.
	ContactsOnFile int
}

// LinkedInReach is the whole answer, including what it cannot show.
type LinkedInReach struct {
	Accounts []LinkedInReachAccount
	// AccountsTotal counts every account reached, not just the page returned.
	// A truncated list read as the whole network would understate reach, which
	// is the one thing this view exists to state.
	AccountsTotal int
	// UnresolvedConnections is how many connections matched no account at all.
	// Reported because it is the honest size of what this view cannot show,
	// and because it is the number that shrinks as accounts are created.
	UnresolvedConnections int
}

// MyLinkedInReach reads the caller's own network, grouped by account.
//
// Organization row scope applies: an account the caller may not read does not
// appear, and its connections fall into neither the list nor the unresolved
// count — they are simply not this caller's to be told about. The alternative,
// counting them as unresolved, would leak the existence of accounts through a
// number.
func (s *Store) MyLinkedInReach(ctx context.Context, limit *int) (LinkedInReach, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return LinkedInReach{}, apperrors.ErrPermissionDenied
	}
	// The payload names organizations, so it takes the organization read grant
	// — a member who may not read accounts must not learn which ones exist by
	// asking about their own address book.
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return LinkedInReach{}, err
	}
	capped := storekit.ClampLimit(limit)

	var out LinkedInReach
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.readReachAccounts(ctx, tx, actor.UserID, capped, &out); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM linkedin_connection
			 WHERE owner_user_id = $1 AND tombstoned_at IS NULL AND matched_org_id IS NULL`,
			actor.UserID).Scan(&out.UnresolvedConnections)
	})
	if err != nil {
		return LinkedInReach{}, err
	}
	return out, nil
}

func (s *Store) readReachAccounts(ctx context.Context, tx pgx.Tx, owner ids.UUID, limit int, out *LinkedInReach) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	ownerPos := arg(owner)
	scope, err := auth.ScopeClauseFor(ctx, "organization", "o", arg)
	if err != nil {
		return err
	}
	visible := sqlAlwaysVisible
	if scope != "" {
		visible = scope
	}
	// The total rides the SAME statement as the rows. Two statements would each
	// take their own snapshot, so a concurrent import between them could report
	// a page longer than the total it is a page of.
	rows, err := tx.Query(ctx, storekit.SQLf(`
		WITH reached AS (
		    SELECT o.id, o.display_name,
		           count(*) AS connections,
		           count(*) FILTER (WHERE c.match_status = 'confirmed') AS on_file
		      FROM linkedin_connection c
		      JOIN organization o ON o.id = c.matched_org_id AND o.archived_at IS NULL
		     WHERE c.owner_user_id = $%d AND c.tombstoned_at IS NULL AND (%s)
		     GROUP BY o.id, o.display_name
		)
		SELECT id, display_name, connections, on_file, (SELECT count(*) FROM reached)
		  FROM reached
		 ORDER BY connections DESC, display_name, id
		 LIMIT $%d`, ownerPos, visible, arg(limit)), args...)
	if err != nil {
		return fmt.Errorf("people: reading which accounts a network reaches: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a LinkedInReachAccount
		// Every row carries the same total; they agree because they come from
		// one statement.
		if err := rows.Scan(&a.OrganizationID, &a.DisplayName, &a.Connections,
			&a.ContactsOnFile, &out.AccountsTotal); err != nil {
			return err
		}
		out.Accounts = append(out.Accounts, a)
	}
	return rows.Err()
}
