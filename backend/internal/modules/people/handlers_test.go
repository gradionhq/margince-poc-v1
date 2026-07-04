// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The dedupe pre-checks leave ExistingID zero when the existing row is
// out of the caller's row scope (or a race hid it); the wire response
// must then omit existing_id entirely — a literal zero UUID is not an
// id, and a client trained to special-case one has been trained wrong.
func TestDuplicateIDOmitsZeroUUID(t *testing.T) {
	if got := duplicateID(ids.Nil); got != "" {
		t.Errorf("duplicateID(zero) = %q, want empty (existing_id omitted on the wire)", got)
	}
	id := ids.NewV7()
	if got := duplicateID(id); got != id.String() {
		t.Errorf("duplicateID(%s) = %q, want the id", id, got)
	}
}
