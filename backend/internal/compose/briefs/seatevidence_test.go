// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package briefs

import (
	"context"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// edgeReader and edgeBlindReader are the two principals the seat evidence read
// distinguishes: one holding the edge grant and one holding every OTHER grant
// the brief needs. The second is the case worth testing — a caller refused the
// edge alone must not take the deal grant as licence to read who sits on it.
func edgeReader() context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test", UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects:  map[string]principal.ObjectGrant{"relationship": {Read: true}, "deal": {Read: true}},
			RowScope: principal.RowScopeAll,
		},
	})
}

func edgeBlindReader() context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test", UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects:  map[string]principal.ObjectGrant{"deal": {Read: true}, "person": {Read: true}},
			RowScope: principal.RowScopeAll,
		},
	})
}

// A caller refused the edge runs NO stakeholder query at all, which is why this
// needs no database: the admission is resolved before the statement, so the
// refusal path never reaches one. The brief then carries no seat evidence,
// which is the same shape as a deal with no stakeholders on it.
func TestSeatEvidenceIsNotAdmittedWithoutTheEdgeGrant(t *testing.T) {
	args, clause, admitted, err := seatEvidenceBound(edgeBlindReader())
	if err != nil {
		t.Fatalf("seatEvidenceBound(no edge grant) = %v, want the refusal reported as not-admitted", err)
	}
	if admitted {
		t.Error("a caller refused the edge was admitted to the stakeholder read — the deal grant does " +
			"not license learning who sits on it")
	}
	if clause != "" || args != nil {
		t.Errorf("a refused caller was handed a clause to run: clause=%q args=%v", clause, args)
	}
}

// The positive control, and it also pins the argument OFFSET: the statement
// this clause joins spends $1 on the deal id, so the first bind this registrar
// hands out must be $2. An off-by-one here binds the deal id to a scope
// predicate, which no assertion about the clause's text would catch.
func TestSeatEvidenceBoundOffsetsItsArgumentsPastTheDealID(t *testing.T) {
	args, clause, admitted, err := seatEvidenceBound(edgeReader())
	if err != nil {
		t.Fatalf("seatEvidenceBound(edge grant) = %v, want a clause", err)
	}
	if !admitted {
		t.Fatal("a caller holding the edge grant was not admitted")
	}
	if clause == "" {
		t.Fatal("an admitted bounded caller got no clause")
	}
	if len(args) == 0 {
		t.Fatal("a bounded clause registered no arguments, so its placeholders bind nothing")
	}
	// The lowest placeholder the clause names must be $2, never $1.
	if strings.Contains(clause, "$1") {
		t.Errorf("the clause binds $1, which the statement spends on the deal id: %s", clause)
	}
	if !strings.Contains(clause, "$2") {
		t.Errorf("the clause does not start binding at $2: %s", clause)
	}
}
