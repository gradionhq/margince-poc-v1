// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The authorable retention scopes (GCS-PARAM-8). ONE place decides what an
// admin may author, and it is derived from retentionSelectors rather than
// listed beside it — a policy whose scope the evaluator has no selector for is
// skipped, loudly, every pass forever, so storing one would report governance
// the installation does not have.
//
// The engine keys its selectors by `object_type + "/" + category`, with the
// empty category leaving a bare trailing slash ("activity/"). The wire spells
// the same pair without it ("activity"), because a trailing slash on a wire
// enum reads as a typo. This file owns that translation so neither side has to
// know the other's spelling.

import (
	"fmt"
	"sort"
	"strings"
)

// RetentionScope is a `(object_type, category)` pair in its wire spelling.
type RetentionScope struct {
	ObjectType string
	// Category is empty for a bare object-type policy, which is stored as SQL
	// NULL — DM-SEED-2's ('activity', NULL) row is the one that ships.
	Category string
}

// selectorKey is the scope's spelling in retentionSelectors.
func (s RetentionScope) selectorKey() string { return s.ObjectType + "/" + s.Category }

// String is the scope's wire spelling: the selector key without the trailing
// slash a bare object type would otherwise carry.
func (s RetentionScope) String() string {
	if s.Category == "" {
		return s.ObjectType
	}
	return s.ObjectType + "/" + s.Category
}

// CategoryPtr renders the category for the database: NULL rather than empty
// string, because the UNIQUE constraint and every existing row treat "no finer
// scope" as NULL and an empty string would be a second, silently different way
// to say it.
func (s RetentionScope) CategoryPtr() *string {
	if s.Category == "" {
		return nil
	}
	category := s.Category
	return &category
}

// ScopeOf rebuilds a scope from stored columns — the inverse of CategoryPtr,
// used when a row read from the database has to name itself on the wire.
func ScopeOf(objectType string, category *string) RetentionScope {
	scope := RetentionScope{ObjectType: objectType}
	if category != nil {
		scope.Category = *category
	}
	return scope
}

// UnknownScopeError refuses a policy for a scope the evaluator cannot act on.
// It names the authorable set, because "unknown scope" without the alternatives
// leaves the admin guessing at a closed vocabulary.
type UnknownScopeError struct {
	Scope string
}

func (e UnknownScopeError) Error() string {
	return fmt.Sprintf("retention scope %q has no evaluator selector", e.Scope)
}

// FieldFault classifies the refusal as a 422 against the field the caller sent,
// carrying the whole authorable vocabulary so one round trip is enough to fix
// the request.
func (e UnknownScopeError) FieldFault() (field, code, message string) {
	return "scope", "unknown_retention_scope", fmt.Sprintf(
		"%q is not a retention scope this installation can act on — authorable scopes are: %s",
		e.Scope, strings.Join(AuthorableScopes(), ", "))
}

// ParseRetentionScope resolves a wire scope against the evaluator's selectors.
// A scope with no selector is refused HERE, at the one entry point every write
// passes through, rather than at the nightly pass where nobody is listening.
func ParseRetentionScope(wire string) (RetentionScope, error) {
	objectType, category, _ := strings.Cut(wire, "/")
	scope := RetentionScope{ObjectType: objectType, Category: category}
	if _, known := retentionSelectors[scope.selectorKey()]; !known {
		return RetentionScope{}, UnknownScopeError{Scope: wire}
	}
	return scope, nil
}

// AuthorableScopes is every scope an admin may author, in wire spelling, sorted
// so the refusal message and any test reading it are stable. Derived from the
// selector table, so adding a selector widens the vocabulary and forgetting to
// add one keeps it honest.
func AuthorableScopes() []string {
	out := make([]string, 0, len(retentionSelectors))
	for key := range retentionSelectors {
		objectType, category, _ := strings.Cut(key, "/")
		out = append(out, RetentionScope{ObjectType: objectType, Category: category}.String())
	}
	sort.Strings(out)
	return out
}
