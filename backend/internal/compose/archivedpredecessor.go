// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// An installation that archived a predecessor organization is carrying that
// organization's rows, and since ADR-0091 §8 phase D nothing can tell them from
// its own.
//
// This is stated at boot because it CANNOT be detected any other way. While the
// record tables carried a tenant column, a gate could count an archived
// workspace's rows and refuse; it ran once, at its ordered position, and the
// operator cleared or accepted what it named. After the column is gone the rows
// are indistinguishable by construction — there is no residue query left to
// write. What survives is the one fact that says a merge was possible at all:
// an archived workspace row.
//
// So this WARNS and does not refuse. Refusing would take down installations
// that archived a predecessor, were told to, accepted the merge and have been
// serving correctly ever since — punishing them for a decision the product
// asked them to make. What an operator gets instead is the sentence nobody said
// at the time: the merge folded credentials and role grants, not just records.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// WarnOnArchivedPredecessor says, once per boot, what an archived organization
// left behind in this installation.
func WarnOnArchivedPredecessor(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	var archived int
	if err := database.WithInfraTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM workspace WHERE archived_at IS NOT NULL`).Scan(&archived)
	}); err != nil {
		return fmt.Errorf("compose: checking for an archived predecessor organization: %w", err)
	}
	if archived == 0 {
		return nil
	}
	log.Warn("this installation carries an archived organization's rows, and they are no longer distinguishable from its own",
		"archived_organizations", archived,
		"what_merged", "login credentials, sessions and RBAC grants — not only records",
		"what_to_check", "the roster and every role assignment: a seat from the archived organization "+
			"authenticates here and holds whatever role it held there",
		"why_now", "ADR-0091 §8 phase D removed the tenant column, so no query can separate the two")
	return nil
}
