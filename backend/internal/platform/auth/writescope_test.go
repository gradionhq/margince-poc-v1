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
	// from opening the record at all — the feature, not the defect.
	for _, table := range []string{"person", "organization", "deal", "lead", "project"} {
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

func TestOnlyAWriteGrantIsProbedBeforeItIsGranted(t *testing.T) {
	ctx := principal.WithActor(context.Background(), human(principal.RowScopeOwn))
	id := ids.NewV7()

	// Passing on `read` needs no probe: EnsureLinkTarget has already proven the
	// caller can see the record, and sight they hold is theirs to pass on
	// (UC-E11-08 F1). Nil transaction again, so a probe would panic.
	if err := EnsureCanGrant(ctx, nil, "person", id, "read"); err != nil {
		t.Errorf("sharing read from a visible record → %v, want allowed without a probe", err)
	}
	// A record type no grant can name is refused before anything is read,
	// because the record type arrives in a request body.
	if err := EnsureCanGrant(ctx, nil, "list", id, "write"); err == nil {
		t.Error("a grant on a non-shareable record type was accepted")
	}
}

func TestTheWriteArmKeepsTheOwnerScopeItNarrows(t *testing.T) {
	// The grant arm is added to the owner scope, never substituted for it: a
	// caller who owns the row needs no grant, and an ownerless (workspace-wide)
	// row stays writable at every tier.
	sql := writeArm(human(principal.RowScopeTeam), "deal")
	for _, want := range []string{"owner_id IS NULL", "team_membership"} {
		if !strings.Contains(sql, want) {
			t.Errorf("the write arm dropped %q from the owner scope, so it narrows more than the "+
				"grant column: %s", want, sql)
		}
	}
}
