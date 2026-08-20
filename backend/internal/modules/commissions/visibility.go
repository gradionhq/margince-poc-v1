// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package commissions

// Commission row visibility, which is INHERITED rather than owned.
//
// The shared row-scope helpers interpolate the tables that answer `owner_id`
// and reject a table name they do not know rather than guessing, so
// commission_entry cannot join them: there is no column for their clauses to
// test.
//
// An entry is visible when the deal it was accrued on is visible. Deriving it
// rather than copying an owner at accrual is what makes a deal reassignment
// carry its commission entries in the same query, instead of leaving them
// behind with the representative who no longer works the account.
//
// There is only one anchor. Unlike a contract, an entry cannot exist without a
// deal — deal_id is NOT NULL — so there is no organization fallback arm.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// unboundedScope is the predicate an unbounded caller contributes: they narrow
// nothing, and the arm still has to say something.
const unboundedScope = "TRUE"

// VisibleClause renders the SQL predicate admitting the commission entries this
// caller may see, for a row under the given alias. It returns the empty string
// for an unbounded caller, exactly as the shared row-scope helpers do.
//
// The anchor must be LIVE. An archived deal keeps its foreign key and its
// grants, so without the filter an entry would stay readable — and payable —
// through a deal whose own read already answers 404.
func VisibleClause(ctx context.Context, alias string, arg func(any) int) (string, error) {
	// The ROW scope below narrows which deals admit their entries; it does not
	// ask whether this caller may read a deal at all. Both are needed: an entry
	// names its deal and prices it, so a caller holding commission:read without
	// deal:read would learn what a deal was worth through the ledger — the
	// object grant the deal's own read would have refused.
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return "", err
	}
	dealScope, err := auth.ScopeClauseFor(ctx, "deal", "d", arg)
	if err != nil {
		return "", err
	}

	qualified := alias
	if qualified != "" {
		qualified += "."
	}
	// An unbounded caller still gets the live-deal requirement: the archived
	// anchor rule is about what the row means, not about who is asking.
	scope := dealScope
	if scope == "" {
		scope = unboundedScope
	}
	return storekit.SQLf(`EXISTS (
		SELECT 1 FROM deal d WHERE d.id = %[1]sdeal_id AND d.archived_at IS NULL AND %[2]s)`,
		qualified, scope), nil
}

// WritableEntriesForDeal narrows a CHANGE to the entries of one deal the caller
// may write, not merely see.
//
// Separate from VisibleClause because a manual share widens VISIBILITY at
// either access level: a caller holding only a `read` share of the deal passes
// the read clause, and voiding their partner's money is not something a read
// share should buy. The write path asks EnsureWritable instead, which is the
// probe that distinguishes the two.
func WritableEntriesForDeal(ctx context.Context, tx pgx.Tx, deal ids.DealID) error {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return err
	}
	return auth.EnsureWritable(ctx, tx, "deal", deal.UUID)
}
