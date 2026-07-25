// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// linkTarget is one arm of activity_link's polymorphic reference: the
// record type as activity_link.entity_type spells it, and the column that
// holds the id for that arm.
type linkTarget struct {
	kind   datasource.RecordType
	column string
}

// linkTargets is the module's whole vocabulary for the timeline link, in
// the column order uq_activity_link's coalesce was created with. The
// entity_type CHECK is pinned to datasource.RecordType by
// TestEveryDomainEnumMatchesItsSchemaCheck, so a record type added to the
// schema and forgotten here fails that gate rather than surfacing as a 422
// on the one code path nobody exercised.
var linkTargets = []linkTarget{
	{datasource.RecordPerson, "person_id"},
	{datasource.RecordOrganization, "organization_id"},
	{datasource.RecordDeal, "deal_id"},
	{datasource.RecordLead, "lead_id"},
}

// linkColumn resolves a wire entity_type to its id column. The empty
// string means the type is outside the vocabulary — the caller's cue to
// raise InvalidLinkTypeError rather than to build SQL from it.
func linkColumn(entityType string) string {
	for _, t := range linkTargets {
		if string(t.kind) == entityType {
			return t.column
		}
	}
	return ""
}

// linkIDCoalesce is uq_activity_link's id expression, built from the same
// ordered vocabulary the index was created from. Every ON CONFLICT target
// and every "which record is this link about" projection reads it from
// here, so the SQL and the index cannot drift apart as the vocabulary grows.
var linkIDCoalesce = buildLinkIDCoalesce("")

// linkIDCoalesceQualified is linkIDCoalesce with a table alias, for the
// joined reads that need to disambiguate the columns.
func linkIDCoalesceQualified(alias string) string { return buildLinkIDCoalesce(alias) }

func buildLinkIDCoalesce(alias string) string {
	cols := make([]string, 0, len(linkTargets))
	for _, t := range linkTargets {
		if alias != "" {
			cols = append(cols, alias+"."+t.column)
			continue
		}
		cols = append(cols, t.column)
	}
	return "coalesce(" + strings.Join(cols, ", ") + ")"
}

// linkVocabulary renders the accepted types for an error a human has to act
// on, so the message names the current set instead of a stale hand-written one.
func linkVocabulary() string {
	names := make([]string, 0, len(linkTargets))
	for _, t := range linkTargets {
		names = append(names, string(t.kind))
	}
	return strings.Join(names, "|")
}
