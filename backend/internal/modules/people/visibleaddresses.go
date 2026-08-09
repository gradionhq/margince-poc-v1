// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Which of these addresses belongs to a contact the caller may read — the
// question an account-started send asks before it will mail a typed address
// (ADR-0087 §2). The send path owns the refusal; this owns the lookup,
// because person_email is this module's table and no sibling reads it.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// VisibleAddresses returns the subset of addresses that belong to a live
// person this caller can read, keyed lowercase.
//
// Row scope is applied to the PERSON, not to the address: an address is
// visible exactly when the contact carrying it is. An out-of-scope person's
// address is therefore absent from the result and indistinguishable from an
// address nobody has — which is the existence-hiding answer the row-scope
// gate gives everywhere else, and the reason the caller learns "not on file"
// rather than "not yours".
//
// Addresses are matched lowercased because person_email stores them
// lowercased (its person_email_norm CHECK), and a caller types whatever
// case they like.
func VisibleAddresses(ctx context.Context, tx pgx.Tx, addresses []string) (map[string]bool, error) {
	// Both halves of the gate, in the usual order. The row scope below answers
	// WHICH contacts this caller may read; the object grant answers whether
	// they may read contacts at all. A seat with no person grant whose row
	// scope happens to match would otherwise confirm that an address is on
	// somebody's record — which is a read of that record.
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return nil, err
	}
	normalized := make([]string, 0, len(addresses))
	for _, addr := range addresses {
		if trimmed := strings.ToLower(strings.TrimSpace(addr)); trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	found := make(map[string]bool, len(normalized))
	if len(normalized) == 0 {
		return found, nil
	}

	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	addressPos := arg(normalized)
	visible, err := auth.ScopeClauseFor(ctx, "person", "p", arg)
	if err != nil {
		return nil, err
	}
	if visible == "" {
		visible = sqlAlwaysVisible
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT DISTINCT pe.email
		  FROM person_email pe
		  JOIN person p ON p.id = pe.person_id AND p.archived_at IS NULL
		 WHERE pe.email = ANY($%d::text[])
		   AND pe.archived_at IS NULL
		   AND (%s)`, addressPos, visible), args...)
	if err != nil {
		return nil, fmt.Errorf("people: resolving recipient addresses: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, fmt.Errorf("people: resolving recipient addresses: %w", err)
		}
		found[email] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("people: resolving recipient addresses: %w", err)
	}
	return found, nil
}
