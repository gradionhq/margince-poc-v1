// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package retrieval is the seam between the AI layers and crm-search
// (architecture/01 §open-items, promoted per B-EP01.2): crm-ai reaches
// search only through this interface, never by importing crm-search
// internals.
package retrieval

import (
	"context"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Retriever serves grounded context to the AI layers. Results carry
// per-item evidence so callers can enforce evidence-or-omit.
type Retriever interface {
	// Search is ranked hybrid retrieval (full-text + vector) scoped to the
	// caller's workspace and row visibility.
	Search(ctx context.Context, q Query) (Result, error)

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

// Result is one ranked page and what kind of ranking produced it.
//
// The second member is why this is a struct rather than a slice. Hybrid
// retrieval has two lanes, and the vector lane can be absent — no embed
// model is bound, or the embedding call failed and the answer fell back to
// the lexical half. Both cases still return hits, and a caller reading them
// as semantically ranked when they were ranked by word overlap is the
// failure this reports: the flagship phrasing of a semantic search shares no
// words with the records it is meant to rank, so a lexical answer to it is
// not a worse answer, it is an answer to a different question.
type Result struct {
	Hits []Hit
	// SemanticRanking is false when the vector lane did not contribute.
	// A caller that publishes ranking as semantic must say so when it is not
	// — it is never a reason to withhold the hits.
	SemanticRanking bool
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
	// ProjectID narrows the picture to ONE body of work, when the caller knows
	// which. Empty is the ordinary case and filters nothing.
	//
	// The rule it applies is exclusion, not selection: material filed under a
	// DIFFERENT project drops out, while material filed under no project at
	// all stays. Attribution is optional in this product, so most
	// correspondence on an account carries no project — a strict "this project
	// only" read would drop the general relationship history and describe an
	// account as though it had no past.
	//
	// An account running two engagements is where this earns its place: a
	// brief that blends them is fluent, confident and about the wrong work.
	ProjectID string
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
	Ref     datasource.EntityRef
	Summary string
	// OccurredAt is when an event item happened, zero when the item is not an
	// event. It rides the assembled picture so a reader states a date from the
	// RECORD rather than from whatever a note's prose recalls.
	OccurredAt time.Time
	Evidence   []Evidence
}
