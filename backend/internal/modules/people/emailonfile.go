// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// EmailAlreadyOnFile reports whether this workspace already holds a live
// person or lead reachable at email.
//
// It is the "we know them already" probe for the paths that PROPOSE a new
// contact to a human — the site read's published-person lane is the first
// caller. Someone who has been emailing us for months arrives through the
// capture Sink as a person long before a crawler finds their name on the
// company's about page; asking a human to confirm them a second time spends
// the decision queue on work that changes nothing.
//
// Person and lead are one question here, not two. A lead is a person the
// workspace has not promoted yet (ADR-0008), so a name sitting in either
// table is a name already on file, and probing only one of them would
// re-propose everyone in the other.
//
// Both halves are row-scoped, so an out-of-scope match reads as no match and
// the caller proposes the contact anyway. That direction is the safe one: the
// answer decides what a human is shown, so a workspace-wide answer would let
// them infer which addresses exist on records their row scope hides.
//
// Which means the CALLER's principal is load-bearing. Ask this as a system
// principal on a human's behalf and the scope clauses collapse to TRUE, which
// silently restores exactly the disclosure the row scope is here to prevent —
// see the site read's probeCtx, which narrows a system worker to the
// requesting human before asking.
func (s *Store) EmailAlreadyOnFile(ctx context.Context, email string) (bool, error) {
	var found bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		found, err = s.EmailAlreadyOnFileTx(ctx, tx, email)
		return err
	})
	return found, err
}

// EmailAlreadyOnFileTx is EmailAlreadyOnFile on the caller's transaction, for
// a staging path that decides and writes in one.
func (s *Store) EmailAlreadyOnFileTx(ctx context.Context, tx pgx.Tx, email string) (bool, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return false, err
	}
	if err := auth.Require(ctx, "lead", principal.ActionRead); err != nil {
		return false, err
	}
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return false, nil
	}

	args := []any{normalized}
	arg := func(v any) int { args = append(args, v); return len(args) }
	personScope, err := scopeOrAllRows(ctx, "person", "p", arg)
	if err != nil {
		return false, err
	}
	leadScope, err := scopeOrAllRows(ctx, "lead", "l", arg)
	if err != nil {
		return false, err
	}

	// Both columns are stored already-normalized, so the comparison goes
	// against the bare column — wrapping it in lower() would forfeit
	// uq_person_email_dedupe and uq_lead_email_dedupe and seq-scan both tables
	// once per proposed person. Every sibling probe of these columns spells it
	// this way.
	var found bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM person p
		   JOIN person_email pe ON pe.person_id = p.id
		   WHERE pe.email = $1
		     AND p.archived_at IS NULL AND pe.archived_at IS NULL
		     AND `+personScope+`
		) OR EXISTS (
		  SELECT 1 FROM lead l
		   WHERE l.email = $1 AND l.archived_at IS NULL
		     AND `+leadScope+`
		)`, args...).Scan(&found); err != nil {
		return false, fmt.Errorf("probe whether this address is already on file: %w", err)
	}
	return found, nil
}
