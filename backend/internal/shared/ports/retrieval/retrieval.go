// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package retrieval is the seam between the AI layers and crm-search
// (architecture/01 §open-items, promoted per B-EP01.2): crm-ai reaches
// search only through this interface, never by importing crm-search
// internals.
package retrieval

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Retriever serves grounded context to the AI layers. Results carry
// per-item evidence so callers can enforce evidence-or-omit.
type Retriever interface {
	// Search is ranked hybrid retrieval (full-text + vector) scoped to the
	// caller's workspace and row visibility.
	Search(ctx context.Context, q Query) ([]Hit, error)

	// AssembleContext builds the provenance-stamped context object an
	// intent tool returns for one anchor record — the assembled picture,
	// not raw rows the agent must re-stitch.
	AssembleContext(ctx context.Context, anchor datasource.EntityRef, opts AssembleOptions) (Context, error)
}

type Query struct {
	Text        string
	EntityTypes []datasource.EntityType
	Limit       int
}

// Hit is one ranked result with the evidence that grounds it.
type Hit struct {
	Ref      datasource.EntityRef
	Score    float64
	Evidence []Evidence
}

// Evidence is a source snippet a claim traces to; ungrounded output is
// omitted, never guessed.
type Evidence struct {
	Source  string // provenance ref, e.g. "gmail:msg-18c2…"
	Snippet string
}

type AssembleOptions struct {
	// MaxItems bounds the assembled context per section (recent touches,
	// open questions, related people).
	MaxItems int
}

// Context is the assembled, provenance-stamped picture for one anchor.
type Context struct {
	Anchor   datasource.EntityRef
	Sections []Section
}

type Section struct {
	Name  string
	Items []Item
}

type Item struct {
	Ref      datasource.EntityRef
	Summary  string
	Evidence []Evidence
}
