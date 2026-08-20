// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package network

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// A coworker set IS the edge: it answers "who else works where this person
// works", which is a fact about the pairs. The refusal reaches addAccountGroup,
// which names the group in groups_omitted.
//
// It must refuse BEFORE the statement, and the nil transaction proves it did:
// the page's total rides count(*) OVER () in the same statement as its rows, so
// a version that read first and filtered after would report a remainder that
// discloses how many coworkers were withheld.
func TestTheCoworkerReadRefusesBeforeItReachesAStatement(t *testing.T) {
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test", UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects:  map[string]principal.ObjectGrant{"person": {Read: true}},
			RowScope: principal.RowScopeAll,
		},
	})
	_, _, err := readAccountContacts(ctx, nil, ids.From[ids.PersonKind](ids.NewV7()))
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("readAccountContacts(no edge grant) = %v, want ErrPermissionDenied", err)
	}
}
