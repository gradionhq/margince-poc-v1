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

// The writer refuses a file whose category nobody derived, and names the seam
// that owes it. The column's own CHECK would refuse the row too, but its report
// names a vocabulary rather than the caller that failed to fill it — which sends
// the reader looking at the enum instead of at the seam that failed.
//
// A NIL TRANSACTION IS THE ASSERTION. If the guard ran anywhere but before the
// first query this would panic rather than return, so passing nil proves the
// refusal costs no round trip and cannot be confused with a missing parent row —
// a claim a live database could not make, because there the two are
// indistinguishable from the outside.
func TestTheWriterRefusesACapturedFileWithNoCategory(t *testing.T) {
	staged := []StagedFile{{file: CapturedFile{PartID: "part:1", Filename: "deck.png"}}}
	err := (&Store{}).RecordCapturedFiles(capturePrincipalCtx(), nil,
		ids.From[ids.ActivityKind](ids.NewV7()),
		CapturedFileSource{System: "imap", MessageID: "m-1", CapturedBy: "connector:imap"},
		staged)
	if !errors.Is(err, ErrCapturedFileCategoryMissing) {
		t.Fatalf("refusal = %v, want it to wrap ErrCapturedFileCategoryMissing", err)
	}
}

// And it refuses only THAT. A file whose category was derived must get past the
// guard — otherwise the guard is a wall, not a gate, and every capture fails
// with an error blaming a caller that did its job.
//
// The nil transaction is what pins the boundary: reaching it means the guard let
// this through, so the panic IS the pass condition, and no live fixture can
// distinguish "the guard let it through" from "the query happened to succeed".
func TestTheWriterAdmitsACapturedFileWhoseCategoryWasDerived(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a file with a derived category was refused before any query ran")
		}
	}()
	staged := []StagedFile{{file: CapturedFile{PartID: "part:1", Filename: "deck.png"}}}
	err := (&Store{}).RecordCapturedFiles(capturePrincipalCtx(), nil,
		ids.From[ids.ActivityKind](ids.NewV7()),
		CapturedFileSource{
			System: "imap", MessageID: "m-1", CapturedBy: "connector:imap",
			Category: "message_attachment",
		},
		staged)
	// Only reachable if the call RETURNED, which means a guard refused a file
	// whose category was derived. The panic is the pass; this line is the report.
	t.Fatalf("a file with a derived category was refused before any query ran: %v", err)
}
