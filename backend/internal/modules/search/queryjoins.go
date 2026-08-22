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

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
	// Every kind this hub reaches pairs a person with one arm: employment is
	// person+organization, deal_stakeholder is person+deal,
	// project_stakeholder is person+project (core 0007, 0131).
	//
	// It does NOT reach every legal row. The partner kinds (partner_of,
	// referred_by, co_sell_with) pair organization_id with counterparty_org_id
	// and require person_id IS NULL, so they are an organization↔organization
	// edge with no person in it — a shape one hub cannot express, on a column
	// arms cannot read. Those rows stay untraversable, which is stated here and
	// gated by TestAReferenceNamedForItsRoleYieldsNoHop rather than left to be
	// rediscovered.
	{table: "relationship", hub: personRef, object: objectRelationship},
	// activity_id is NOT NULL and exactly one arm is set, which the table's
	// own activity_link_shape CHECK enforces arm by arm (core 0008, 0038).
	// No object of its own: a link is not a record, and reading one discloses
	// only the two records it names — both of which the hop already gates.
	{table: "activity_link", hub: "activity_id"},
}

// personRef is the reference column two of the derivations name, spelled once.
// The linter asks for the constant; what makes it worth having is that a
// misspelling in the hub declaration would silently derive no hops at all.
const personRef = entityPerson + relationSuffix

// objectRelationship is the RBAC object governing the employment and
// stakeholder edges. Named here rather than imported: a module never imports a
// sibling, and TestEveryJoinTableThatIsAnRBACObjectDeclaresIt holds this
// against identity's own list so the two cannot drift.
const objectRelationship = "relationship"

// The two lifecycle columns a join table may carry, spelled once because the
// derivation reads them and the statement names them.
const (
	columnArchivedAt = "archived_at"
	columnEndedAt    = "ended_at"
)

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
	// object is the RBAC object that governs READING this edge, empty when the
	// table is not an object in its own right.
	//
	// It is not the same question as the two endpoints' own grants, which is
	// why gating those is not enough: `relationship` is a first-class object
	// precisely because an edge discloses its endpoints as a PAIR. The people
	// module says so where it gates the same read — "reading an edge discloses
	// its endpoints, which is what `relationship` read governs" — and a
	// traversal that skipped it would answer "who works at Acme" for a role
	// that is refused the employment list on every other surface.
	object string
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
	"project_link_candidate": "a QUESTION the attribution ladder asked about a message and a project, " +
		"not an answer: until a human confirms it nothing connects the two, and once they do the " +
		"activity_link row is the edge a hop traverses — reading the candidate as one would file " +
		"a message under a project nobody agreed it belongs to",
	"person_signature_enrich_state": "enrichment bookkeeping keyed by the activity a signature was read " +
		"from — a cursor over work, not a statement about the two records",
	"attachment": "a file, which is a record in its own right. Its extra references are the " +
		"ROLL-UP READ PATH core 0195 declares them to be — the primary parent still owns the " +
		"file's visibility — so they denormalize one record's parentage rather than relating two",
	"contract": "a finance record in its own right. Its references are the scalar kind any record " +
		"declares; they are untraversable only because contract is not a searchable record type, " +
		"which is a different question from this one",
}

// JoinEdge is the resolved third spelling: the table the edge lives in and the
// two columns that reach the records at its ends.
//
// Both guards are read off the join table's own columns rather than assumed.
// `relationship` carries them and `activity_link` carries neither, and a clause
// naming a column a table does not have is a database error on every plan that
// traverses it — not a narrower answer.
//
// They are two guards because they answer two different questions.
// Archived is DELETED: the edge was recorded in error, or the row was swept.
// Ended is LEFT: the edge was true and stopped being true, which is the
// ordinary end of a job and leaves the row in place. Filtering only the first
// is what makes a company a person left go on reading as where they work —
// the same conflation #1789 fixed on the write side of this column.
type JoinEdge struct {
	Table      string
	From       string
	To         string
	Archivable bool
	Ends       bool
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
		// Dropped from the VOCABULARY rather than refused at execution, so a
		// caller who may not read this edge sees the same surface as one for
		// whom the hop never existed — the existence-hiding every other refusal
		// on this path keeps.
		if join.object != "" && auth.Require(ctx, join.object, principal.ActionRead) != nil {
			continue
		}
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
					Archivable: stored.holds(columnArchivedAt),
					Ends:       stored.holds(columnEndedAt),
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
func pluralRelationName(entity string) string {
	if plural, irregular := irregularPlurals[entity]; irregular {
		return plural
	}
	return entity + "s"
}

// irregularPlurals holds the record types whose name does not pluralize by
// adding an s. `activitys` is what the naive rule produces, and it reached the
// vocabulary the moment a join edge gave `activity` its first inverse hop —
// before that, no record declared a scalar activity_id, so the rule was never
// asked. A name a caller has to misspell to use is a worse answer than a
// missing hop.
var irregularPlurals = map[string]string{entityActivity: "activities"}

// mergeRelations keeps ONE relation per name, and a direct edge wins.
//
// No pair collides on today's schema, and that is a property of the hub rule
// rather than of the namespace: an arm reaches only the hub, so `relationship`
// never offers `organization → deals` beside the scalar edge of that name. This
// stays because the two derivations share ONE namespace and neither knows what
// the other produced — the day a second hub, a same-type edge or a new join
// table makes a name contested, which of them ran would otherwise depend on the
// order the derivations happened to run in. The direct edge wins because it is
// the record's own declared reference, which is what a caller naming it means.
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
// The tenant is not named, and the reason is not RLS: ADR-0091 retired every
// policy (core 0217) and dropped workspace_id from both join tables (the
// activity-spine and records sweeps). One installation serves one organization,
// so there is no tenant predicate anywhere on this read — the outer statement
// and the lateral hop carry none either. What bounds this subquery is what
// bounds them: the caller's row scope and object RBAC, applied to the records at
// each end. Naming a column these tables no longer have would not be a stronger
// boundary; it would not compile.
func joinEdgeCondition(edge JoinEdge) string {
	where := []string{fmt.Sprintf("j.%s = t.id", sanitize(edge.From))}
	if edge.Archivable {
		where = append(where, "j."+columnArchivedAt+" IS NULL")
	}
	if edge.Ends {
		// A hop is CURRENT membership, which is what every other reader of this
		// column in the tree means by it (compose/introseams.go,
		// compose/network/persongraphaccount.go). "People at this company"
		// answering with the ones who left would be wrong in the direction that
		// costs something: a stale contact is acted on, a missing one is asked
		// about.
		//
		// A FUTURE ended_at is a notice period, and it is still current — the
		// person works there until the date arrives. Compared against the
		// DATABASE's clock, because the row is dated by it and a comparison
		// against any other would disagree with it near midnight.
		where = append(where, fmt.Sprintf("(j.%s IS NULL OR j.%s > current_date)", columnEndedAt, columnEndedAt))
	}
	return fmt.Sprintf("h.id IN (SELECT j.%s FROM %s j WHERE %s)",
		sanitize(edge.To), sanitize(edge.Table), strings.Join(where, " AND "))
}
