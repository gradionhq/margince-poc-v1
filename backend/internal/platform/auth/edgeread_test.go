// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The defect EdgeReadScope exists to close: a caller holding both endpoints'
// read grants and NOT the edge's own gets no clause and no admission. Every
// endpoint grant a reader could plausibly have composed is held here, so the
// refusal cannot be mistaken for one of them being absent.
func TestEdgeReadScopeRefusesACallerHoldingOnlyTheEndpointGrants(t *testing.T) {
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test",
		Permissions: principal.Permissions{
			RoleKeys: []string{"fixture"},
			Objects: map[string]principal.ObjectGrant{
				"person": {Read: true}, "organization": {Read: true},
				"deal": {Read: true}, "project": {Read: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})

	clause, err := EdgeReadScope(ctx, "r", func(any) int { return 1 })
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("EdgeReadScope(endpoints but no edge grant) = %v, want ErrPermissionDenied", err)
	}
	// An empty clause matters as much as the error: "" also means UNBOUNDED, so
	// a caller that ignored the error and interpolated the result would read
	// every edge in the workspace rather than none.
	if clause != "" {
		t.Errorf("a refused caller was handed a clause to run: %q", clause)
	}
}

// The positive control on the same shape. Without it the refusal above passes
// for a function that refuses everyone, including one whose gate is wired to
// the wrong object.
func TestEdgeReadScopeAdmitsTheEdgeGrantAndBoundsItByEveryEndpoint(t *testing.T) {
	ctx := principal.WithActor(context.Background(), edgeReader(principal.RowScopeOwn))

	var args []any
	clause, err := EdgeReadScope(ctx, "r", func(v any) int { args = append(args, v); return len(args) })
	if err != nil {
		t.Fatalf("EdgeReadScope(edge grant, bounded) = %v, want the conjunction", err)
	}
	if clause == "" {
		t.Fatal("a row-bounded caller got the unbounded empty clause")
	}
	// Every endpoint column an edge can carry appears: the conjunction is what
	// distinguishes this from the single-endpoint scoping the call sites used to
	// assemble by hand, and a clause missing an arm scopes an edge by its other
	// end alone.
	for _, column := range []string{"person_id", "organization_id", "counterparty_org_id", "deal_id", "project_id"} {
		if !strings.Contains(clause, "r."+column) {
			t.Errorf("the clause does not bound the %s endpoint: %s", column, clause)
		}
	}
}

// Only the SYSTEM principal reads edges unbounded, and that is a property of
// capture privacy rather than of row scope: person and organization carry a
// visibility column, so even a human at row_scope=all keeps an arm on those two
// endpoints while deal and project collapse away. Pinned because "" means
// UNBOUNDED at every call site that interpolates it — a human who slipped into
// this branch would read every edge in the workspace.
func TestOnlyTheSystemPrincipalReadsEdgesUnbounded(t *testing.T) {
	atAll := principal.WithActor(context.Background(), edgeReader(principal.RowScopeAll))
	clause, err := EdgeReadScope(atAll, "r", func(any) int { return 1 })
	if err != nil {
		t.Fatalf("EdgeReadScope(human, row_scope=all) = %v, want admission", err)
	}
	if clause == "" {
		t.Fatal("a human at row_scope=all read edges unbounded: capture privacy on person and " +
			"organization must still bound the edge")
	}
	for _, endpoint := range []string{"person", "organization"} {
		if !strings.Contains(clause, "FROM "+endpoint+" ep") {
			t.Errorf("the row_scope=all clause drops the %s arm, so a capture-private endpoint is "+
				"disclosed through its edge: %s", endpoint, clause)
		}
	}

	asSystem := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:test",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
	systemClause, err := EdgeReadScope(asSystem, "r", func(any) int { return 1 })
	if err != nil {
		t.Fatalf("EdgeReadScope(system) = %v, want admission", err)
	}
	if systemClause != "" {
		t.Errorf("the system principal was bounded: %q", systemClause)
	}
}

// The system principal short-circuits Require, which is what keeps the privacy
// sweeps, the auto-enrich pass and the capture resolvers reading edges after
// this gate is added in front of them. It is pinned HERE, at the edge read,
// because those readers hold no object grants at all: a change to Require's
// short-circuit would make a retention sweep under-delete, and the sweep's own
// tests cannot see why.
func TestEdgeReadScopeAdmitsTheSystemPrincipalWhichHoldsNoGrants(t *testing.T) {
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:test",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})

	if _, err := EdgeReadScope(ctx, "r", func(any) int { return 1 }); err != nil {
		t.Fatalf("EdgeReadScope(system) = %v, want admission — a sweep that cannot read edges under-deletes", err)
	}
}

// A context with no actor is a programming error (middleware always binds one)
// and must not read as an unbounded admission.
func TestEdgeReadScopeNeedsAnActor(t *testing.T) {
	clause, err := EdgeReadScope(context.Background(), "r", func(any) int { return 1 })
	if err == nil {
		t.Fatal("EdgeReadScope(no actor) = nil, want an error")
	}
	if clause != "" {
		t.Errorf("EdgeReadScope(no actor) handed back a clause: %q", clause)
	}
}

// edgeReader holds the edge grant and nothing else, at the given row scope —
// building on the package's own `human` so a change to what a bounded principal
// carries reaches this file too.
func edgeReader(scope principal.RowScope) principal.Principal {
	p := human(scope)
	p.Permissions.RoleKeys = []string{"fixture"}
	p.Permissions.Objects = map[string]principal.ObjectGrant{"relationship": {Read: true}}
	return p
}
