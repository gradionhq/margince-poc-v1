// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

// Two orderings the page owes, both above the transaction it opens.
//
// The custom-field catalog is read BEFORE that transaction: the catalog read
// runs a transaction of its own, so doing it from inside this page's would take
// a second connection while the first is held — which commits separately and
// blocks against the lock the page already holds. The pool below is nil
// deliberately: it makes the order provable rather than asserted, because a
// read that reached the transaction would have to resolve it.
//
// And the caller's grant is checked before EITHER: this page has no gate above
// the store, so a seat with no person:read must be refused without the
// catalog — or any other database work — happening on its behalf.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// errCatalog is the failure the seam reports; the assertion is that it arrives
// unchanged, so a wrapped or swallowed catalog error is visible.
var errCatalog = errors.New("the catalog is unreachable")

// refusingCatalog is the fieldcatalog seam — a boundary the store reaches over
// the network — standing in for a catalog that cannot answer. It doubles as
// the marker for the gate-order arm: reaching it at all is the finding there.
type refusingCatalog struct{}

func (refusingCatalog) ActiveColumns(context.Context, string) ([]fieldcatalog.Column, error) {
	return nil, errCatalog
}

func refusingCatalogService() *Service {
	store := people.NewStore(database.BindTo(nil, ids.From[ids.WorkspaceKind](ids.NewV7()))).
		WithFieldCatalog(refusingCatalog{})
	return NewService(nil, store, nil, nil, nil, func() time.Time { return time.Unix(0, 0).UTC() })
}

// as binds a principal holding exactly the grants named.
func as(objects map[string]principal.ObjectGrant) context.Context {
	user := ids.NewV7()
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"}, Objects: objects, RowScope: principal.RowScopeAll,
		},
	})
}

func TestAssembleReadsTheCatalogBeforeItOpensTheTransaction(t *testing.T) {
	ctx := as(map[string]principal.ObjectGrant{"person": {Read: true}})

	_, err := refusingCatalogService().Assemble(ctx, ids.From[ids.PersonKind](ids.NewV7()))
	if !errors.Is(err, errCatalog) {
		t.Fatalf("err = %v, want the catalog's own failure — the page must refuse on it rather than carry on without the custom columns", err)
	}
}

func TestAssembleRefusesAnUngrantedSeatBeforeItReadsAnything(t *testing.T) {
	ctx := as(map[string]principal.ObjectGrant{"person": {Read: false}})

	_, err := refusingCatalogService().Assemble(ctx, ids.From[ids.PersonKind](ids.NewV7()))
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("err = %v, want the permission refusal; the catalog's own failure here means a seat with no person:read still cost a catalog read", err)
	}
}
