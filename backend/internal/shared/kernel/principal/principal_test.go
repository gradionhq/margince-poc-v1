// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package principal

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestAgentRunIDRoundTrips(t *testing.T) {
	id := ids.NewV7()
	ctx := WithAgentRunID(context.Background(), id)
	got, ok := AgentRunID(ctx)
	if !ok || got != id {
		t.Fatalf("AgentRunID = %v, %v; want %v, true", got, ok, id)
	}
	if _, ok := AgentRunID(context.Background()); ok {
		t.Fatal("AgentRunID on a bare context should report ok=false")
	}
}
