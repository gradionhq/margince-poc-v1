// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// The third spelling of a reference: an edge that lives in NEITHER record.
//
// The two derivations in queryfields.go read a scalar `<record type>_id` member
// off a contract type — the referring record's own for a forward hop, another
// record's for the inverse. Both spellings put the reference ON one of the two
// records, which is why an edge carried by a table between them was invisible
// by construction rather than by omission: there was no member to read it off.
//
// What makes one rule enough for both join tables in this schema is that they
// are the same shape physically. `activity_link` carries `person_id`,
// `organization_id`, `deal_id` and `lead_id`; `relationship` carries
// `person_id`, `organization_id`, `deal_id` and `project_id`. The contract's
// `ActivityLink{entity_id, entity_type}` is the WIRE shape of a link and not
// its storage — the columns are typed, and a CHECK constraint says which one
// each row fills. So the rule reads columns, exactly as the field vocabulary
// does, and never has to know what a link means.

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// joinTables declares the tables that carry an edge between two searchable
// records.
//
// The TABLE is what is declared here, and nothing else. Every relation it
// yields — both directions, every pair — is derived from its own columns, so
// widening a join table publishes the new hops without an edit to this list,
// and `TestEveryJoinTableInTheSchemaIsDeclared` fails on a NEW join table that
// this list has not learned about. A hand-maintained list of RELATIONS is what
// this file exists to avoid; a two-entry list of tables that a test holds
// against the schema is not that.
var joinTables = []joinTable{
	// Every legal row pairs a person with one arm: employment is
	// person+organization, deal_stakeholder is person+deal,
	// project_stakeholder is person+project (core 0007, 0131). The partner
	// kinds are the exception and pair organization with counterparty_org_id,
	// which joinTargets does not read — see there.
	{table: "relationship", hub: personRef},
	// activity_id is NOT NULL and exactly one arm is set, which the table's
	// own activity_link_shape CHECK enforces arm by arm (core 0008, 0038).
	{table: "activity_link", hub: "activity_id"},
}

// personRef is the reference column two of the derivations name, spelled once.
// The linter asks for the constant; what makes it worth having is that a
// misspelling in the hub declaration would silently derive no hops at all.
const personRef = entityPerson + relationSuffix

// joinTable is one declared edge table: its name, and the column every legal
// row fills.
//
// The hub is what makes the derivation exact rather than combinatorial. A join
// table's record references are not interchangeable — `relationship` holds
// `organization_id` and `deal_id` and no row ever fills both, because they
// belong to different kinds. Pairing every column with every other would
// publish `organization → deals` through this table, a hop no row can satisfy
// and which a caller would read as "no data" rather than "not a thing".
//
// So one column is named and the rest are arms, and the edges are hub↔arm.
// That is one declaration per table, not one per relation: an arm added to a
// join table becomes traversable with no edit here, which is what happened
// when 0038 gave activity_link its lead arm and 0131 gave relationship its
// project one.
type joinTable struct {
	table string
	hub   string
}

// notAnEdge records the OTHER tables carrying two record references, and why
// each is not a hop.
//
// Carrying two references is what makes a table a candidate, not what makes it
// an edge, and the census in queryjoins_test.go cannot tell the two apart —
// `activity_participant` and `person_consent` look identical to it. So every
// candidate gets a verdict in one of these two lists and a new table gets
// neither, which is what fails the gate. The reason is the point: a bare
// exclusion list would read as "handled" and this reads as "decided".
var notAnEdge = map[string]string{
	"activity_participant": "who capture MATCHED from an address, where activity_link is what it " +
		"ASSERTED about the record — graphactivity.go ranks the assertion above the match for the " +
		"same reason a hop should traverse it and not this",
	"person_consent": "person_id and lead_id are alternative OWNERS of one consent record, never both, " +
		"so the pair is a polymorphic parent rather than an edge between the two",
	"consent_event": "the append-only log behind person_consent, and polymorphic in the same way",
	"activity_retention_evidence": "what a retention decision was taken on; it relates a record to a " +
		"DECISION about it, and nothing traverses from one record to another through it",
	"person_signature_enrich_state": "enrichment bookkeeping keyed by the activity a signature was read " +
		"from — a cursor over work, not a statement about the two records",
	"contract": "a finance record in its own right. Its references are the scalar kind any record " +
		"declares; they are untraversable only because contract is not a searchable record type, " +
		"which is a different question from this one",
}

// JoinEdge is the resolved third spelling: the table the edge lives in and the
// two columns that reach the records at its ends.
//
// Archivable is read off the join table's own columns rather than assumed:
// `relationship` carries `archived_at` and an archived employment must not
// carry a hop, while `activity_link` has no such column and a clause naming
// one would be a database error on every plan that traversed it.
type JoinEdge struct {
	Table      string
	From       string
	To         string
	Archivable bool
}

// joinRelations answers the hops one record type reaches through a join table.
//
// A relation is published only when BOTH columns are really there — the same
// bar storedInverseRelations holds an inverse hop to, and for the same reason:
// a hop the executor could not build is worse than a missing one, because a
// caller can see a missing hop and cannot see a broken one.
//
// With no column reader wired there are no join relations at all. That is
// narrower than the unwired field vocabulary, which falls back to the
// contract's — deliberately, because a join edge has NO contract spelling to
// fall back to. Publishing one from a declaration alone would advertise a hop
// whose columns nothing had confirmed, which is precisely the "published but
// unanswerable" case the schema filter exists to remove.
func joinRelations(ctx context.Context, schema *schemaReads, entity string) ([]Relation, error) {
	if contractRecords[entity] == nil {
		return nil, fmt.Errorf("search: %q is not a searchable record type", entity)
	}
	var relations []Relation
	for _, join := range joinTables {
		stored, err := schema.ofTable(ctx, join.table)
		if err != nil {
			return nil, err
		}
		if !stored.holds(join.hub) {
			continue
		}
		for _, edge := range join.edges(stored, entity) {
			relations = append(relations, Relation{
				Name:   pluralRelationName(edge.target),
				Target: edge.target,
				Via:    joinVia(join.table, edge.from, edge.to),
				Join: &JoinEdge{
					Table:      join.table,
					From:       edge.from,
					To:         edge.to,
					Archivable: stored.holds("archived_at"),
				},
			})
		}
	}
	return relations, nil
}

// joinEdge is one resolved hub↔arm traversal, before it is named.
type joinEdge struct {
	target string
	from   string
	to     string
}

// edges answers the traversals this table offers FROM one record type: the hub
// reaches every arm, and every arm reaches the hub. An arm never reaches
// another arm, because no row fills two of them.
func (j joinTable) edges(stored *storage, entity string) []joinEdge {
	arms := j.arms(stored)
	if hub, isHub := strings.CutSuffix(j.hub, relationSuffix); isHub && hub == entity {
		edges := make([]joinEdge, 0, len(arms))
		for _, arm := range arms {
			edges = append(edges, joinEdge{target: arm, from: j.hub, to: arm + relationSuffix})
		}
		return edges
	}
	if !slices.Contains(arms, entity) {
		return nil
	}
	hub, isHub := strings.CutSuffix(j.hub, relationSuffix)
	if !isHub || contractRecords[hub] == nil {
		return nil
	}
	return []joinEdge{{target: hub, from: entity + relationSuffix, to: j.hub}}
}

// arms answers the searchable record types one join table reaches besides its
// hub, read off its columns.
//
// A column is a reference only when stripping `_id` leaves the name of a record
// type this module searches — the same test contractRelations applies, and it
// is what keeps `relationship.counterparty_org_id` out. That column names its
// ROLE (the partner org on an org↔org edge) rather than its target, so
// org↔org partner edges stay untraversable. Special-casing the name here would
// make this derivation carry one table's naming history; the honest fix is in
// the contract, and until it happens the gap is visible rather than papered
// over.
func (j joinTable) arms(stored *storage) []string {
	var arms []string
	for record := range contractRecords {
		column := record + relationSuffix
		if column != j.hub && stored.holds(column) {
			arms = append(arms, record)
		}
	}
	slices.Sort(arms)
	return arms
}

// joinVia renders the published explanation of a join edge.
//
// It is prose, not a parse target. The two SCALAR spellings of Via are read
// back apart by newHopBinding — a bare `organization_id` against a qualified
// `deal.organization_id` — and a join edge is told apart from both by carrying
// a JoinEdge, never by its text. The arrow is what keeps a reader from trying:
// no scalar Via has ever contained one.
func joinVia(table, from, to string) string {
	return fmt.Sprintf("%s(%s → %s)", table, from, to)
}

// pluralRelationName is the ONE spelling of a hop that may land on many rows.
//
// Both derivations that produce one use it: the inverse of a scalar reference
// (`organization` → `deals`) and either direction of a join edge
// (`person` → `organizations`). A second pluralization rule beside this one is
// how a vocabulary comes to answer to two names for one hop.
func pluralRelationName(entity string) string { return entity + "s" }

// mergeRelations keeps ONE relation per name, and a direct edge wins.
//
// The collision is real and not hypothetical: `organization` reaches `deals`
// both as the inverse of `deal.organization_id` and through `relationship`,
// which carries a column for each. They are not the same question — the scalar
// is the deal's own account, the join is a stakeholder edge — and the scalar is
// the one a caller naming `deals` means. Publishing both under one name would
// make which of them ran depend on the order the derivations happened to run
// in, which is the kind of answer that is right until somebody sorts a slice.
func mergeRelations(direct, joined []Relation) []Relation {
	taken := make(map[string]bool, len(direct))
	for _, relation := range direct {
		taken[relation.Name] = true
	}
	merged := direct
	for _, relation := range joined {
		if taken[relation.Name] {
			continue
		}
		taken[relation.Name] = true
		merged = append(merged, relation)
	}
	return merged
}

// joinEdgeCondition renders the traversal through a join table.
//
// It is an IN against the join table rather than a second JOIN because the hop
// is already a LATERAL that returns exactly one row: the join table answers
// membership, and which of several edges connected the two records is not a
// question the hop's evidence claims to answer.
//
// The tenant is not named. Every table this reaches is under FORCE RLS, so the
// subquery is bounded by the same GUC the outer read is — naming workspace_id
// here would be a second, weaker copy of that boundary.
func joinEdgeCondition(edge JoinEdge) string {
	where := []string{fmt.Sprintf("j.%s = t.id", sanitize(edge.From))}
	if edge.Archivable {
		where = append(where, "j.archived_at IS NULL")
	}
	return fmt.Sprintf("h.id IN (SELECT j.%s FROM %s j WHERE %s)",
		sanitize(edge.To), sanitize(edge.Table), strings.Join(where, " AND "))
}
