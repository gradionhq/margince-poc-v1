// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package yogi

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// TestNewDeclaresAServedTool: the unit declares exactly one governed tool,
// it passes the published grammar, and it carries a handler (so boot serves it
// rather than leaving it inert).
//
// It asserts NOTHING about the tier, the scope, the title or the schemas, and
// that absence is the point of this slice: those are declared in api/crm.yaml,
// they are read out of the merged contract, and they are checked where they are
// declared (gen-composition's readOperation and extension.Verb.Validate). A
// second assertion here would be a second statement of the same fact from a
// file that no longer holds it.
func TestNewDeclaresAServedTool(t *testing.T) {
	ext := New()
	if len(ext.Tools) != 1 {
		t.Fatalf("want one tool, got %d", len(ext.Tools))
	}
	tool := ext.Tools[0]
	if err := tool.Validate(); err != nil {
		t.Fatalf("declared tool must validate: %v", err)
	}
	if tool.Name != "yogi_quote" {
		t.Fatalf("Name = %q, want the verb api/crm.yaml declares", tool.Name)
	}
	if tool.Handle == nil {
		t.Fatal("a served tool must carry a handler")
	}
}

// TestTheDeclarationCarriesNoGovernance: the narrowed Tool is {Name, Handle},
// and the unit's own suite is where a reader would look to see that. If a field
// carrying tier, scope or prose ever returned to this file, the contract would
// stop being the single place an operator reads — so the shape is pinned here
// rather than only in the published package's own tests.
func TestTheDeclarationCarriesNoGovernance(t *testing.T) {
	if n := reflect.TypeOf(extension.Tool{}).NumField(); n != 2 {
		t.Fatalf("extension.Tool has %d fields; a unit declaration is {Name, Handle} and everything "+
			"else about a tool is declared in api/crm.yaml", n)
	}
}

// TestQuoteReturnsAKnownQuote: the handler ignores its input and returns
// one of the declared quotes, shaped as the OutputSchema promises. The
// Runtime is nil here because this tool reaches nothing through it — which
// is exactly what a unit needing no capability looks like.
func TestQuoteReturnsAKnownQuote(t *testing.T) {
	out, err := New().Tools[0].Handle(context.Background(), nil, nil)
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
