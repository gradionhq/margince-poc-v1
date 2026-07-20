// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A nil pool turns any premature storage access into a panic, proving
// both methods refuse an actor-less request at admission first.
func TestCallReadRefusesBeforeStorage(t *testing.T) {
	store := NewCallReadStore(nil)
	if _, err := store.ListCalls(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("ListCalls: want refusal on an actor-less context, got nil")
	}
	if _, err := store.GetCall(context.Background(), ids.NewV7()); err == nil {
		t.Fatal("GetCall: want refusal on an actor-less context, got nil")
	}
}
