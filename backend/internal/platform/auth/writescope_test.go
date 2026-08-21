// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

// The read/write asymmetry of a manual grant, as the predicates SPELL it.
//
// record_grant.access has carried two levels since 0011 and the schema has
// always said "write satisfies read" — but nothing read the column, so a `read`
// share widened a mutation exactly as far as a `write` one. These tests pin the
// two halves that make the distinction real, and they are deliberately a pair:
// asserting only that the write arm reads `access` would pass just as well if
// the visibility arm had been narrowed too, which would break sharing instead
// of fixing it.

import (
	"context"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// writeArm renders the write-authority predicate for one table, with the arg
// registrar the production callers use.
func writeArm(p principal.Principal, table string) string {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	return writeAuthorityPredicate(p, table, arg)
}

func TestTheWriteArmCountsOnlyAWriteGrant(t *testing.T) {
	for _, table := range []string{"person", "organization", "deal", "lead", "project"} {
		for _, scope := range []principal.RowScope{principal.RowScopeOwn, principal.RowScopeTeam} {
			sql := writeArm(human(scope), table)
			if !strings.Contains(sql, "rg.access = 'write'") {
				t.Errorf("%s write arm at row_scope=%s does not read record_grant.access, so a `read` "+
					"share still confers write: %s", table, scope, sql)
			}
			if !strings.Contains(sql, "rg.expires_at IS NULL OR rg.expires_at > now()") {
				t.Errorf("%s write arm at row_scope=%s counts an EXPIRED grant: %s", table, scope, sql)
			}
			if !strings.Contains(sql, "rg.record_type = '"+table+"'") {
				t.Errorf("the %s write arm at row_scope=%s does not pin the grant's record_type, so a "+
					"grant on another kind of record would answer for this one: %s", table, scope, sql)
			}
		}
	}
}

func TestTheVisibilityArmStillCountsEveryLiveGrant(t *testing.T) {
	// The mirror of the test above, and the reason it exists: write satisfies
	// read, so narrowing the VISIBILITY arm to `write` would stop a read share
	// from opening the record at all — the feature, not the defect. Only the
	// capture-private tables are left to check: every seat reads deal, lead
	// and project whole (tableclass.go), so a grant has nothing to widen there
	// and the arm is not rendered at all.
	for _, table := range []string{"person", "organization"} {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		sql := VisiblePredicate(human(principal.RowScopeTeam), table, arg)("t")
		if !strings.Contains(sql, "FROM record_grant rg") {
			t.Errorf("%s visibility predicate lost its grant arm entirely, so a share no longer "+
				"widens a read: %s", table, sql)
		}
		if strings.Contains(sql, "rg.access") {
			t.Errorf("%s visibility predicate reads record_grant.access, so a `read` share no longer "+
				"lets its holder OPEN the record — write satisfies read, not the other way: %s", table, sql)
		}
	}
}

// The three cases below all answer BEFORE the probe would query, which is what
// makes a nil transaction the right witness: a case that reached the database
// would panic rather than pass, so these cannot be quietly answering from a
// query that never happened.
func TestTheWriteProbeDecidesWhatItCanBeforeItQueries(t *testing.T) {
	as := func(p principal.Principal) context.Context {
		return principal.WithActor(context.Background(), p)
	}
	id := ids.NewV7()

	t.Run("an unbounded actor needs no grant", func(t *testing.T) {
		if err := ensureWriteAuthority(as(human(principal.RowScopeAll)), nil, "person", id); err != nil {
			t.Errorf("row_scope=all refused a write it already holds every row for: %v", err)
		}
	})

	t.Run("a table no grant can name is the owner scope alone", func(t *testing.T) {
		// A list carries an owner and no share, so the visibility probe that
		// ran first applied the whole authority. Answering nil here is what
		// lets every mutation ask the same question whatever its table.
		if err := ensureWriteAuthority(as(human(principal.RowScopeOwn)), nil, "list", id); err != nil {
			t.Errorf("a non-shareable row-scoped table refused a write: %v", err)
		}
	})

	t.Run("a table the row-scope vocabulary does not hold is an error", func(t *testing.T) {
		// Never nil: a name this primitive cannot place is a programming
		// error, and answering "permitted" for one would interpolate an
		// unvalidated string into the SQL below it.
		if err := ensureWriteAuthority(as(human(principal.RowScopeOwn)), nil, "activity", id); err == nil {
			t.Error("an unknown table was accepted, so the SQL guard is the caller's allowlist alone")
		}
	})
}

func TestEveryGrantIsProbedBeforeItIsGranted(t *testing.T) {
	id := ids.NewV7()

	// A record type no grant can name is refused before anything is read,
	// because the record type arrives in a request body. The nil transaction is
	// the assertion: reaching a probe would panic.
	bounded := principal.WithActor(context.Background(), human(principal.RowScopeOwn))
	if err := EnsureCanGrant(bounded, nil, "list", id); err == nil {
		t.Error("a grant on a non-shareable record type was accepted")
	}

	// An unbounded caller may change any row, so there is nothing for the probe
	// to find and it short-circuits before the transaction is touched. This is
	// the one caller that still needs no query — and it is what keeps the
	// legitimate path (an admin extending or re-opening somebody's share) free
	// of a round trip.
	unbounded := principal.WithActor(context.Background(), human(principal.RowScopeAll))
	if err := EnsureCanGrant(unbounded, nil, "person", id); err != nil {
		t.Errorf("an unbounded caller sharing a person → %v, want allowed without a probe", err)
	}

	// A bounded caller gets no such exemption at ANY access level. There is no
	// longer a branch on access: the probe is the only path out of this
	// function for them, which is what the panic recovered here proves. The
	// behaviour it then applies is asserted against a real row in
	// compose/integration's grants suite.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("a bounded caller returned without probing the row — " +
					"the access-level exemption is back")
			}
		}()
		// Not discarded: if this ever RETURNS instead of reaching the probe,
		// that is the exemption coming back by another route, and it should be
		// reported rather than swallowed by the recover below.
		if err := EnsureCanGrant(bounded, nil, "person", id); err != nil {
			t.Errorf("a bounded caller was answered without a query → %v", err)
		}
	}()
}

func TestTheWriteArmKeepsTheOwnerScopeItNarrows(t *testing.T) {
	// The grant arm is added to the owner scope, never substituted for it: a
	// caller who owns the row, or a teammate, needs no grant. An ownerless
	// row is the one exception — it is nobody's to change until claimed, so
	// the write arm has no `owner_id IS NULL` branch while the read arm keeps
	// it (an unowned row is still the workspace's to see).
	sql := writeArm(human(principal.RowScopeTeam), "deal")
	for _, want := range []string{"deal.owner_id = $", "team_membership"} {
		if !strings.Contains(sql, want) {
			t.Errorf("the write arm dropped %q from the owner scope, so it narrows more than the "+
				"grant column: %s", want, sql)
		}
	}
	if strings.Contains(sql, "owner_id IS NULL") {
		t.Errorf("the write arm admits an ownerless row; it must be claimed first: %s", sql)
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	if read := OwnerPredicate(human(principal.RowScopeTeam), arg)("t"); !strings.Contains(read, "t.owner_id IS NULL") {
		t.Errorf("the READ owner predicate no longer shares an ownerless row: %s", read)
	}
}
