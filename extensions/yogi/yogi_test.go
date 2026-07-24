// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package yogi

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// TestNewDeclaresAServedTool: the unit declares exactly one governed tool,
// it passes the published grammar, and it carries a handler (so boot
// serves it rather than leaving it inert).
func TestNewDeclaresAServedTool(t *testing.T) {
	ext := New()
	if len(ext.Tools) != 1 {
		t.Fatalf("want one tool, got %d", len(ext.Tools))
	}
	tool := ext.Tools[0]
	if err := tool.Validate(); err != nil {
		t.Fatalf("declared tool must validate: %v", err)
	}
	if tool.Handle == nil {
		t.Fatal("a served tool must carry a handler")
	}
	if tool.Tier != extension.TierAutoExecute || tool.RequestedScope != extension.ScopeRead {
		t.Fatalf("a read-only quote tool should request 🟢/read, got tier=%q scope=%q", tool.Tier, tool.RequestedScope)
	}
	if !json.Valid(tool.OutputSchema) {
		t.Fatal("OutputSchema must be valid JSON")
	}
}

// TestQuoteReturnsAKnownQuote: the handler ignores its input and returns
// one of the declared quotes, shaped as the OutputSchema promises.
func TestQuoteReturnsAKnownQuote(t *testing.T) {
	out, err := New().Tools[0].Handle(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got quoteOut
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("result is not the declared shape: %v", err)
	}
	if !slices.Contains(quotes, got.Quote) {
		t.Fatalf("handler returned an unknown quote: %q", got.Quote)
	}
}
