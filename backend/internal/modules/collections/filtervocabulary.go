// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

// Reading the filter vocabulary out loud (LVS-EXT-8).
//
// The vocabulary is closed and server-owned, which is what lets a filter mean
// one thing across dynamic lists, saved views and filtered export. A builder
// screen that had to offer a field picker without asking the server would keep a
// second copy of it, and a second copy is the failure this seam exists to
// prevent: it offers a field the engine refuses, and the human learns the
// difference as a 422 nobody could have predicted.
//
// So the field set is not assembled from scratch; it starts as the engine's own
// map, read through the same SegmentEngine call that dynamic-list validation,
// membership evaluation and export all resolve through.
//
// It is not the whole of that map, and the exception is the point. What a filter
// may SAY includes a retired field, because retirement never drops the column and
// a saved segment built on one has to keep evaluating. What a builder may OFFER
// for a NEW clause excludes it — CUSTOM-FIELDS-AC-13 makes retire "hidden from
// API + filtering", and AC-14 names the admin catalogue read as the one surface
// that still shows retired fields. This is not that surface.
//
// So what this operation guarantees is a subset relation, not an equality, and it
// is worth stating precisely because the loose version is wrong: everything
// advertised here is accepted by the engine, and the gap between advertised and
// accepted is exactly the retired set. A picker built from this cannot compose a
// clause the engine refuses; a saved segment naming a retired field keeps working;
// and nothing offers a human a field an admin retired to get it out of their way.
//
// One thing a reader will look for and not find: a LABEL. Labels are
// admin-facing catalog metadata that the fieldcatalog seam keeps out of a
// filtering consumer's reach, and the custom-field catalog already serves them
// keyed by the same column name — one join, on a surface a builder screen already
// calls.

import (
	"context"
	"fmt"
	"sort"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// VocabularyField is one field a filter clause may name on some resource.
//
// Custom answers a question the engine's Field cannot: the engine knows how to
// compile a column, not whether a workspace defined it.
type VocabularyField struct {
	Name      string
	Type      string
	Operators []string
	Custom    bool
}

// FilterVocabulary answers every field a NEW filter clause may name for this
// resource, each with the operators its type admits.
//
// ok is false for a resource with no engine at all — the same answer
// SegmentEngine gives, and for the same reason: activities and partners are not
// predicate-leaf resources, and the caller decides what that means.
//
// Gated as a read of `list`, the object whose filters this vocabulary describes,
// with no row-scope clause because it returns no record and no record's contents.
// The workspace-private part of the answer is which cf_* columns exist, and that
// is already ambient to every principal reachable here: every seeded role holding
// `list:read` also holds `custom_field:read` (core migrations 0064, 0183, 0268),
// and the catalogue read that grant governs returns strictly more — labels,
// slugs, status, and the retired rows this operation omits.
func (s *Store) FilterVocabulary(ctx context.Context, resource string) ([]VocabularyField, bool, error) {
	if err := auth.Require(ctx, "list", principal.ActionRead); err != nil {
		return nil, false, err
	}
	engine, ok, err := s.SegmentEngine(ctx, resource)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	offerable, err := s.offerableCustomColumns(ctx, resource)
	if err != nil {
		return nil, false, err
	}
	core := segmentEngines[resource].Fields
	retiredCore := retiredCoreFields[resource]
	fields := make([]VocabularyField, 0, len(engine.Fields))
	for name, field := range engine.Fields {
		// Core membership decides Custom, matching the merge in SegmentEngine
		// exactly: a catalogue row colliding with a core name never reaches the
		// vocabulary, so reporting that name as custom would describe a field
		// the engine does not have.
		_, isCore := core[name]
		if isCore && retiredCore[name] {
			continue
		}
		if !isCore && !offerable[name] {
			continue
		}
		fields = append(fields, VocabularyField{
			Name:      name,
			Type:      string(field.Type),
			Operators: storekit.OperatorsFor(field.Type),
			Custom:    !isCore,
		})
	}
	// By name, because a map answers a different order every call and a picker
	// whose fields reshuffle between two identical requests reads as broken.
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	return fields, true, nil
}

// offerableCustomColumns answers which cf_* columns a new clause may name — the
// active ones, which is the catalogue's own answer to "may a write set this".
//
// Advertising exactly what a write may set is the rule, and it falls out of
// CUSTOM-FIELDS-AC-13 rather than being chosen here: retire hides a field from
// the API and from filtering while preserving its column, so the set a builder
// offers and the set a record may carry are the same set.
//
// An unwired catalogue has no custom columns at all, so nothing is offerable and
// the vocabulary is the core one — which is exactly what SegmentEngine compiles
// against in that case, so the subset relation still holds.
func (s *Store) offerableCustomColumns(ctx context.Context, resource string) (map[string]bool, error) {
	if s.catalog == nil {
		return map[string]bool{}, nil
	}
	active, err := s.catalog.ActiveColumns(ctx, resource)
	if err != nil {
		return nil, fmt.Errorf("read the active custom-field columns for %s: %w", resource, err)
	}
	offerable := make(map[string]bool, len(active))
	for _, column := range active {
		offerable[column.Name] = true
	}
	return offerable, nil
}

// wireVocabularyField dresses one field for the wire.
//
// The operator and type strings pass through as the contract's enums without
// being re-checked against them, and that is safe for one reason worth stating:
// both sides are the same closed set (LVS-PARAM-1), and the enum gates in
// compose/filtervocabularyenums_test.go fail if they ever stop being.
// Re-validating here instead would silently drop a value the engine admits,
// turning a contract that has fallen behind into a vocabulary that has quietly
// shrunk.
func wireVocabularyField(f VocabularyField) crmcontracts.FilterVocabularyField {
	operators := make([]crmcontracts.FilterVocabularyFieldOperators, 0, len(f.Operators))
	for _, op := range f.Operators {
		operators = append(operators, crmcontracts.FilterVocabularyFieldOperators(op))
	}
	return crmcontracts.FilterVocabularyField{
		Name:      f.Name,
		Type:      crmcontracts.FilterVocabularyFieldType(f.Type),
		Operators: operators,
		Custom:    f.Custom,
	}
}
