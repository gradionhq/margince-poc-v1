// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package signals

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// RouteInEdges answers "who that we know works there" — the edge itself, with
// its kind and role. A caller refused the edge is REFUSED, not handed an empty
// route list: on the warm room an empty list would read as a COLD verdict, and
// a cold verdict reached by not being allowed to look for warmth is false.
//
// The nil transaction is the assertion: the gate resolves before the statement,
// so the refusal never reaches a database.
func TestRouteInEdgesRefusesBeforeItReachesAStatement(t *testing.T) {
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test", UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects: map[string]principal.ObjectGrant{
				"person": {Read: true}, "organization": {Read: true}, "signal": {Read: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	if _, err := RouteInEdges(ctx, nil, ids.From[ids.OrganizationKind](ids.NewV7())); !errors.Is(
		err, apperrors.ErrPermissionDenied,
	) {
		t.Errorf("RouteInEdges(no edge grant) = %v, want ErrPermissionDenied — reporting cold instead "+
			"would be a verdict this caller was refused the means to reach", err)
	}
}
