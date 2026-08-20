// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

// The relationship edge's READ admission, in one spelling.
//
// An edge is governed by two rules that answer different questions, and the
// defect this file closes is taking one of them without the other:
//
//   - the OBJECT gate asks whether this caller may read edges at all.
//     `relationship` is a first-class RBAC object because an edge discloses its
//     endpoints AS A PAIR, which the grants on the two records do not cover:
//     "who works at Acme" is a fact about the pair, not about either record.
//   - the ROW scope asks WHICH edges, and RelationshipEndpointScope already
//     spells that as the conjunction over every endpoint an edge can carry.
//
// The row half has lived here since it acquired a second reader, for the reason
// its own comment gives: scope policy has exactly one spelling (ADR-0054 §8),
// and a second copy of a conjunction is a second place for one of its arms to
// be forgotten. The object half had no home, so every caller assembled it from
// the endpoint grants it could see — and the endpoint grants are exactly what an
// edge grant is not.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// EdgeReadScope admits a caller to read relationship edges and returns the
// clause bounding WHICH edges they may read.
//
// The two halves are returned by one call because taking the scope while
// skipping the gate is the defect: a caller that composes an endpoint's grant
// with an endpoint's scope has written something that looks complete and reads
// an edge the edge grant would have refused.
//
// It does NOT make the wrong thing unreachable. RelationshipEndpointScope stays
// exported, because the module that owns the relationship surface composes it
// under gates taken at its own store entry points. What holds the rule is
// backend/edgereaders_test.go: it requires the object gate at every read of the
// table, and accepts the row half alone only inside those owning packages. A
// new compose read that takes the conjunction and never asks the gate fails
// that census — which is the enforcement this function is the ergonomics of,
// not a property of the function itself.
//
// A caller refused the object gets apperrors.ErrPermissionDenied unwrapped, so
// a section-assembling read can name the omission through the contract's own
// withheld channel (groups_omitted, sections_omitted) rather than failing a
// page whose other sections the caller may legitimately read. A caller that
// serves the edge as its whole answer returns the denial instead. Both are
// existing shapes; this function does not choose between them.
//
// An empty clause means unbounded, exactly as RelationshipEndpointScope's does:
// a caller unbounded over every endpoint table. Callers that interpolate the
// result keep their `if clause == "" { clause = "true" }` shape.
//
// alias names the relationship table in the outer query.
func EdgeReadScope(ctx context.Context, alias string, arg func(any) int) (string, error) {
	// The gate precedes the clause, and the clause is not built when the gate
	// refuses: a caller that received a clause would have been handed something
	// to run.
	if err := EdgeReadAdmitted(ctx); err != nil {
		return "", err
	}
	return RelationshipEndpointScope(ctx, alias, arg)
}

// EdgeReadAdmitted answers the OBJECT half alone: may this caller read edges at
// all. It is for the assembler that has to decide whether a section exists
// before it decides what is in it.
//
// It is deliberately NOT one of the spellings backend/edgereaders_test.go
// accepts as an edge read's gate. A statement admitted by this and bounded by
// nothing would be gated on the object and unbounded on the row — the mirror of
// the defect EdgeReadScope exists to prevent — so a read still has to reach the
// function that returns both halves. This one answers a question ABOUT the
// caller, and issues no statement of its own.
//
// A caller refused gets apperrors.ErrPermissionDenied unwrapped, so the
// assembler can name the omission through its contract's withheld channel.
func EdgeReadAdmitted(ctx context.Context) error {
	return Require(ctx, relationshipObject, principal.ActionRead)
}

// relationshipObject is the RBAC object governing the edge. Spelled once here
// because a misspelled object name would deny silently rather than fail:
// Require asks the permission document for the object it is handed, and a
// document holding no entry for that spelling refuses — which reads as a role
// misconfiguration, not as a typo in this file.
const relationshipObject = "relationship"
