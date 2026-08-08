// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Where a bounded read stops, and whether it says so.
//
// The bound is reported to a model as a warning — "a warmer route may exist
// outside this list" — so the boundary case is not a detail. An account holding
// exactly the fetch bound had nothing dropped, and telling a rep that someone
// warmer exists when the list is complete is the same false claim as hiding a
// cap, pointed the other way.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestTheContactFetchReportsACapOnlyWhenARowWasActuallyCut(t *testing.T) {
	for name, tc := range map[string]struct {
		read       int
		wantKept   int
		wantCapped bool
	}{
		"an account well inside the bound":     {read: 3, wantKept: 3, wantCapped: false},
		"an account holding exactly the bound": {read: accountContactFetch, wantKept: accountContactFetch, wantCapped: false},
		"an account with one contact past it":  {read: accountContactFetch + 1, wantKept: accountContactFetch, wantCapped: true},
	} {
		t.Run(name, func(t *testing.T) {
			kept, capped := trimToFetchBound(contactsNumbering(tc.read))

			if len(kept) != tc.wantKept {
				t.Errorf("kept %d contacts, want %d", len(kept), tc.wantKept)
			}
			if capped != tc.wantCapped {
				t.Errorf("reported capped=%v, want %v — the bound itself is not evidence that anything was dropped",
					capped, tc.wantCapped)
			}
		})
	}
}

func contactsNumbering(n int) []accountContact {
	out := make([]accountContact, 0, n)
	for range n {
		out = append(out, accountContact{id: ids.NewV7(), name: "Contact"})
	}
	return out
}
