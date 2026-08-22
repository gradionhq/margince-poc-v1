// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Vocabulary that reads ANOTHER record type than the report's own.
//
// The engine's admission gate covers spec.entity — the projects of a project
// report, the activities of an activity report. A measure that folds the
// deals under a project, or a dimension that names the project an activity
// is filed under, reads a second record type, and the caller owes that
// type's read grant as much as they owe the first. Without this, a seat
// holding project.read alone would total deal money it could not list, and a
// seat holding activity.read alone would learn which projects exist.
//
// A name the caller ASKS for without the grant is refused (403), on the
// report and on the drill-through alike. A name the caller never asked for is
// simply not served: the default plan drops it, and the drill-through omits
// its column — the same answer shape a narrower seat gets everywhere else.

import (
	"context"
	"maps"
	"slices"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// requireVocabularyGrants refuses the first requested name whose grant the
// caller lacks. names may mix dimensions and measures; a name outside
// spec.grants owes nothing beyond the report's own gate.
func requireVocabularyGrants(ctx context.Context, spec reportSpec, names []string) error {
	for _, name := range names {
		object, ok := spec.grants[name]
		if !ok {
			continue
		}
		if err := auth.Require(ctx, object, principal.ActionRead); err != nil {
			return err
		}
	}
	return nil
}

// grantedNames filters a vocabulary down to the names this caller may be
// served, for the plans the caller did not spell out themselves.
func grantedNames(ctx context.Context, spec reportSpec, vocabulary map[string]string) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(vocabulary)) {
		if requireVocabularyGrants(ctx, spec, []string{name}) == nil {
			out = append(out, name)
		}
	}
	return out
}

// grantedDefaultAggregates is the spec's default plan minus the measures the
// caller may not read. A count over the report's own rows survives, so the
// narrower seat still gets an answer rather than a refusal for a plan it never
// wrote.
func grantedDefaultAggregates(ctx context.Context, spec reportSpec) []reportAggregate {
	out := make([]reportAggregate, 0, len(spec.defaultAggs))
	for _, agg := range spec.defaultAggs {
		if agg.Field != "" && requireVocabularyGrants(ctx, spec, []string{agg.Field}) != nil {
			continue
		}
		out = append(out, agg)
	}
	return out
}

// aggregateFields lists the measure names a plan's aggregates read.
func aggregateFields(aggregates []reportAggregate) []string {
	fields := make([]string, 0, len(aggregates))
	for _, agg := range aggregates {
		if agg.Field != "" {
			fields = append(fields, agg.Field)
		}
	}
	return fields
}
