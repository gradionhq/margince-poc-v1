// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// A logo reference is half-resolved when it names stored bytes with no origin
// URL, or an origin with no bytes: either way the field's provenance would be
// blank, so the write is refused at the door — ahead of the transaction, which
// is what lets this probe run over a nil pool.
//
// The workspace and the actor ARE bound, so the refusal is the only thing that
// can produce the error: a guard that ever slipped behind the query would
// reach the nil pool and panic instead of quietly passing for the wrong reason.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestSetOrganizationLogoRefusesAHalfResolvedWrite(t *testing.T) {
	store := NewStore(nil)
	rep := ids.NewV7()
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + rep.String(), UserID: rep,
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{"organization": {Update: true}},
		},
	})
	orgID := ids.New[ids.OrganizationKind]()

	if _, _, err := store.SetOrganizationLogo(ctx, orgID, "", "https://halbmond.test/f.png"); err == nil {
		t.Fatal("a logo with no storage key must be refused")
	}
	if _, _, err := store.SetOrganizationLogo(ctx, orgID, "k", ""); err == nil {
		t.Fatal("a logo with no source URL must be refused — its provenance would be blank")
	}
}
