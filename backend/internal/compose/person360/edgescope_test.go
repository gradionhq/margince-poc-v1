// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func personReader(objects map[string]principal.ObjectGrant) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test", UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"}, Objects: objects, RowScope: principal.RowScopeAll,
		},
	})
}

// The sentinel must reach the caller UNWRAPPED, because Assemble decides
// between naming a section in sections_omitted and failing the page by asking
// errors.Is on exactly this value. A wrapped denial would fail the whole page.
func TestEdgeScopeReturnsTheSentinelSoASectionCanBeNamed(t *testing.T) {
	ctx := personReader(map[string]principal.ObjectGrant{
		"person": {Read: true}, "deal": {Read: true},
	})
	clause, err := edgeScope(ctx, func(any) int { return 1 })
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("edgeScope(no edge grant) = %v, want ErrPermissionDenied unwrapped", err)
	}
	if clause != "" {
		t.Errorf("a refused caller was handed a clause: %q", clause)
	}
}

// An unbounded caller gets the narrows-nothing predicate rather than the empty
// string: the sections interpolate this straight into SQL, where "" is a syntax
// error and scopeAll is the honest "no bound".
func TestEdgeScopeAnswersScopeAllForAnUnboundedCaller(t *testing.T) {
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:test",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
	clause, err := edgeScope(ctx, func(any) int { return 1 })
	if err != nil {
		t.Fatalf("edgeScope(system) = %v, want admission", err)
	}
	if clause != scopeAll {
		t.Errorf("edgeScope(unbounded) = %q, want %q — an empty string would not compile into the "+
			"statement that interpolates it", clause, scopeAll)
	}
}
