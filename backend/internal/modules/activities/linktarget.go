// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// linkTarget is one arm of activity_link's polymorphic reference: the
// record type as activity_link.entity_type spells it, the column that
// holds the id for that arm, and the column that holds the record's
// display name.
//
// The name half is carried HERE rather than beside the one read that
// projects it, so a record type joining the vocabulary cannot be added
// without answering what a human calls it. A struct literal missing a
// field is a compile error; a lookup table somewhere else is a thing to
// forget.
//
// The TABLE is not carried beside it: each of these record types lives in a
// table named for it, so a second spelling would be nothing but a chance to
// disagree with the first.
type linkTarget struct {
	kind   datasource.RecordType
	column string
	// nameColumn is where the display name lives. All five spell it
	// differently, which is exactly why it is written down.
	nameColumn string
}

// linkTargets is the module's whole vocabulary for the timeline link, in
// the column order uq_activity_link's coalesce was created with. The
// entity_type CHECK is pinned to datasource.RecordType by
// TestEveryDomainEnumMatchesItsSchemaCheck, so a record type added to the
// schema and forgotten here fails that gate rather than surfacing as a 422
// on the one code path nobody exercised.
var linkTargets = []linkTarget{
	{datasource.RecordPerson, "person_id", "full_name"},
	{datasource.RecordOrganization, "organization_id", "display_name"},
	{datasource.RecordDeal, "deal_id", "name"},
	{datasource.RecordLead, "lead_id", "full_name"},
	{datasource.RecordProject, "project_id", "name"},
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

// linkNameCoalesce projects the display name of whichever record a link row
// points at, built from the same ordered vocabulary as the id expression
// above so the two cannot come to disagree about which arms exist.
//
// Correlated subqueries rather than five LEFT JOINs: coalesce stops at the
// first non-null, and exactly one arm of a link row is ever set, so this
// reads one row from one table however wide the vocabulary grows. A join per
// arm would read five.
//
// IT PROJECTS A NAME THE CALLER IS ALREADY ENTITLED TO. Every caller pairs
// this with auth.LinkTargetVisibleClause, which drops a link row whose target
// is out of the caller's row scope — so a name reached from a surviving row
// belongs to a record that caller may read. Used without that clause it would
// be a disclosure, which is why no caller here spells one without the other.
//
// alias names the activity_link table in the caller's query.
func linkNameCoalesce(alias string) string {
	arms := make([]string, 0, len(linkTargets))
	for _, t := range linkTargets {
		arms = append(arms, sprintf("(SELECT t.%s FROM %s t WHERE t.id = %s.%s)",
			t.nameColumn, t.kind, alias, t.column))
	}
	return "coalesce(" + strings.Join(arms, ", ") + ")"
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
