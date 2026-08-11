// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Binding the correspondence envelope every drafting surface is handed.

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/draftfloor"
)

// draftEnvelope builds the resolver that answers what language a draft is
// written in, what time it is, and who is writing it.
//
// One constructor for every drafting surface, which is the point: the person
// composer, the account composer and the endpoints that run with no model lane
// all resolve the sender the same way, so a draft cannot be written as the
// right person on one screen and as nobody on another.
//
// The sender is identity's, and it is the ONE thing here that reaches the
// database. Everything else the resolver does is pure.
func draftEnvelope(pool *pgxpool.Pool, log *slog.Logger) *draftfloor.Resolver {
	return draftfloor.NewResolver().
		WithSender(identity.NewService(pool)).
		WithLogger(log)
}
