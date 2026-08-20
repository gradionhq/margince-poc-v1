// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// capturePrincipalCtx binds what a connector's sync loop holds when it lands a
// message: create on activity, and nothing else this writer asks for.
func capturePrincipalCtx() context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalConnector, ID: "connector:imap",
		Permissions: principal.Permissions{
			RoleKeys: []string{"capture"},
			Objects:  map[string]principal.ObjectGrant{"activity": {Create: true}},
			RowScope: principal.RowScopeAll,
		},
	})
}

// The guard refuses a file whose category nobody derived, and admits one whose
// category was. BOTH arms, because a guard that refuses everything passes any
// test that only checks the refusal — and it would fail every capture in
// production with an error blaming a caller that did its job.
func TestOnlyAFileWithNoDerivedCategoryIsRefused(t *testing.T) {
	if err := refuseUnderivedCategory(CapturedFileSource{
		System: "imap", MessageID: "m-1", CapturedBy: "connector:imap",
	}); !errors.Is(err, ErrCapturedFileCategoryMissing) {
		t.Errorf("an unset category: %v, want it to wrap ErrCapturedFileCategoryMissing", err)
	}
	for _, category := range []string{"email_attachment", "message_attachment", "other"} {
		if err := refuseUnderivedCategory(CapturedFileSource{
			System: "imap", MessageID: "m-1", CapturedBy: "connector:imap", Category: category,
		}); err != nil {
			t.Errorf("a derived category %q was refused: %v", category, err)
		}
	}
}

// And the guard is WIRED INTO the entry point, not merely present beside it. The
// writer's own refusal is what a caller meets, and a guard nothing calls is the
// same as no guard at all.
//
// A nil transaction is safe here precisely because the refusal precedes the first
// query — which is the second thing this asserts. If that ever stops being true
// the call panics, and a panic fails the test rather than passing it, which is
// what an earlier version of this test got backwards.
func TestTheWriterItselfRefusesAFileWithNoCategory(t *testing.T) {
	staged := []StagedFile{{file: CapturedFile{PartID: "part:1", Filename: "deck.png"}}}
	err := (&Store{}).RecordCapturedFiles(capturePrincipalCtx(), nil,
		ids.From[ids.ActivityKind](ids.NewV7()),
		CapturedFileSource{System: "imap", MessageID: "m-1", CapturedBy: "connector:imap"},
		staged)
	if !errors.Is(err, ErrCapturedFileCategoryMissing) {
		t.Fatalf("the writer answered %v, want it to wrap ErrCapturedFileCategoryMissing", err)
	}
}
