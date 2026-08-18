// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

// Reading the filter vocabulary out loud (LVS-EXT-8).
//
// The vocabulary is closed and server-owned, which is what lets a filter mean
// one thing across dynamic lists, saved views and filtered export. A builder
// screen that had to offer a field picker without asking the server would keep
// a second copy of it, and a second copy is the failure this seam exists to
// prevent: it offers a field the engine refuses, and the human learns the
// difference as a 422 nobody could have predicted.
//
// So the field SET here is not assembled; it is the engine's own map, read
// through the same SegmentEngine call that dynamic-list validation, membership
// evaluation and export all resolve through. Parity is structural rather than
// tested-into-place — there is no list to keep in step, because there is no
// second list.
//
// Two things a reader will look for and not find. There is no LABEL: labels are
// admin-facing catalog metadata, which the fieldcatalog seam deliberately keeps
// out of a filtering consumer's reach, and the custom-field catalog already
// serves them keyed by the same column name. And there is no RETIRED flag, for
// the same reason — the seam answers "may a filter name this" and nothing else,
// so telling active from retired here would mean widening it to a lifecycle
// question collections has no other use for. Both are one join away on a
// surface that already has them.

import (
	"context"
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

// FilterVocabulary answers every field a filter may name for this resource,
// each with the operators its type admits.
//
// ok is false for a resource with no engine at all — the same answer
// SegmentEngine gives, and for the same reason: activities and partners are not
// predicate-leaf resources, and the caller decides what that means.
//
// Gated as a read of `list`, the object whose filters this vocabulary describes.
// It returns no record and no record's contents, so there is no row scope to
// apply; what it does reveal is which custom fields a workspace has defined,
// which a caller who may read lists can already infer by building one.
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
	core := segmentEngines[resource].Fields
	fields := make([]VocabularyField, 0, len(engine.Fields))
	for name, field := range engine.Fields {
		// Core membership decides Custom, matching the merge in SegmentEngine
		// exactly: a catalogue row colliding with a core name never reaches the
		// vocabulary, so reporting that name as custom would describe a field
		// the engine does not have.
		_, isCore := core[name]
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

// wireVocabularyField dresses one field for the wire.
//
// The operator and type strings pass through as the contract's enums without
// being re-checked against them, and that is safe for one reason worth stating:
// both sides are the same closed set (LVS-PARAM-1), and the enum gates in
// compose/segmentvocabularyenums_test.go fail if they ever stop being.
// Re-validating here instead would silently drop a value the engine admits,
// turning a contract that has fallen behind into a vocabulary that has quietly
// shrunk.
func wireVocabularyField(f VocabularyField) crmcontracts.SegmentVocabularyField {
	operators := make([]crmcontracts.SegmentVocabularyFieldOperators, 0, len(f.Operators))
	for _, op := range f.Operators {
		operators = append(operators, crmcontracts.SegmentVocabularyFieldOperators(op))
	}
	return crmcontracts.SegmentVocabularyField{
		Name:      f.Name,
		Type:      crmcontracts.SegmentVocabularyFieldType(f.Type),
		Operators: operators,
		Custom:    f.Custom,
	}
}
