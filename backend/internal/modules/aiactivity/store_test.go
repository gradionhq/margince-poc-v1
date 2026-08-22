// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aiactivity

// The two refusals the store makes BEFORE any SQL runs.
//
// Both shapes would be caught by the table's own CHECKs a moment later, and
// that is exactly why they are worth refusing here: a constraint violation
// arrives at the consumer as an opaque error it can only retry into forever,
// while a refusal names the field and the value. The projection is fed by an
// at-least-once bus, so "retry forever" is a wedged lane rather than one bad
// row.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestAChangeIsRefusedBeforeAnySQLWhenItsShapeCannotBeStored(t *testing.T) {
	valid := Change{
		Source: "attachment_extraction", OccurrenceKey: "k", Kind: "document_extract",
		Attempt: 1, ActorScope: ScopePersonal, State: "queued", EventID: ids.NewV7(),
	}
	cases := []struct {
		name   string
		mutate func(Change) Change
		wants  string
	}{{
		name:   "an attempt below one",
		mutate: func(c Change) Change { c.Attempt = 0; return c },
		wants:  "attempt",
	}, {
		name:   "a scope that is neither personal nor workspace",
		mutate: func(c Change) Change { c.ActorScope = "team"; return c },
		wants:  "actor scope",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A nil handle: reaching SQL at all would fail with the no-workspace
			// sentinel instead, so this also proves the refusal comes FIRST.
			applied, err := NewStore(nil).ApplyStateChange(context.Background(), tc.mutate(valid))
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Fatalf("the refusal must name %q, got %v", tc.wants, err)
			}
			if applied {
				t.Fatal("a refused change must not report itself applied")
			}
		})
	}
}

// A personal read with no bound person is refused rather than answered.
// Answered, it would be a query with no predicate on the one column that makes
// the feed personal — everybody's work, handed to whoever asked.
func TestAPersonalReadWithNoPersonIsRefused(t *testing.T) {
	_, _, err := NewStore(nil).Mine(context.Background(), time.Time{}, nil)
	if err == nil {
		t.Fatal("expected a refusal for a read with no person")
	}
	if !strings.Contains(err.Error(), "needs an authenticated person") {
		t.Fatalf("the refusal must say what is missing, got %v", err)
	}
}
