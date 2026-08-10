// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// `margince://schema/record-fields` — what create_record and update_record
// accept, published as a document rather than carried in their input schemas.
//
// It used to be carried. The two tools rendered every record type's field shape
// plus every advisory into their own `fields` description, and between them that
// was 18% of the whole tool listing — text every client holds on every session,
// and every Surface-B run re-sends on every step, to answer a question one call
// asks once.
//
// Moving it is not the schema deferral the surface rules out. `fields` stays
// {"type":"object"}, byte-identical, and every argument that decides whether a
// call is WELL-FORMED stays in the schema; what moves is the per-type field
// vocabulary, which is what margince://schema/query already publishes for the
// same reason. The move is safe because the refusal path is loud:
// rejectUnknownFields names the offending key AND returns the whole accepted
// list, so a caller that writes without reading this document is told what to
// read, once, rather than guessing twice.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// RecordFieldsURI is the document's stable identity.
const RecordFieldsURI = "margince://schema/record-fields"

// recordFieldsNotation states the one convention the shapes are written in.
// Without it a reader has to infer what `?` means from examples, and inferring
// which keys are REQUIRED from punctuation is exactly the guess this document
// exists to remove.
const recordFieldsNotation = "A key with no `?` is REQUIRED by the contract; `?` marks one the body may " +
	"omit. A name not listed is not a field: it is refused by name, never stored silently."

// RecordFieldsResource publishes the write vocabulary.
//
// It holds nothing. The document is composed from the contract — the generated
// shapes and the advisories derived from them — so it is the same for every
// caller in every workspace, unlike the query vocabulary next to it, which names
// a workspace's own columns and therefore has to be resolved per principal.
type RecordFieldsResource struct{}

// Resources advertises the one document this module publishes.
func (RecordFieldsResource) Resources(context.Context) []mcp.Resource {
	return []mcp.Resource{{
		URI:   RecordFieldsURI,
		Name:  "record_fields",
		Title: "Record write vocabulary",
		// ScopeWrite, not ScopeRead: this document is about WRITING, and a
		// passport holding only a read grant learning the write vocabulary is
		// the discovery channel the scope filter on the tool list is careful
		// not to be. A caller admitted to create_record or update_record is
		// admitted to this by the same scope.
		RequiredScope: principal.ScopeWrite,
		MIMEType:      "application/json",
		// Says what it HOLDS, not when to fetch it. This sentence rides the
		// Surface-B prompt in full, and as an instruction ("read it before your
		// first write") it drew reads from a run that had no write to make.
		Description: "The fields create_record and update_record accept for each record_type: " +
			"which are required, what shape each takes, and the values the closed ones admit. " +
			"The two tools name this document instead of carrying it.",
	}}
}

// ReadResource composes the document. An unknown URI answers ErrNotFound,
// matching how every other read on this surface treats something the caller
// cannot see.
func (RecordFieldsResource) ReadResource(_ context.Context, uri string) (mcp.ResourceContents, error) {
	if uri != RecordFieldsURI {
		return mcp.ResourceContents{}, fmt.Errorf("agents: resource %q: %w", uri, apperrors.ErrNotFound)
	}
	body, err := json.Marshal(recordFieldsDocument())
	if err != nil {
		return mcp.ResourceContents{}, fmt.Errorf("agents: rendering the record write vocabulary: %w", err)
	}
	return mcp.ResourceContents{URI: uri, MIMEType: "application/json", Text: string(body)}, nil
}

var _ mcp.ResourceProvider = RecordFieldsResource{}

// recordFieldsDoc is the published shape: one section per WRITE, because the
// two disagree about more than their field lists. An activity's links are
// creatable and not patchable, a deal's pipeline is required on create and
// absent on update — so a merged section would have to hedge every note into
// something true of neither call.
type recordFieldsDoc struct {
	Version  string            `json:"version"`
	Notation string            `json:"notation"`
	Create   recordFieldsWrite `json:"create_record"`
	Update   recordFieldsWrite `json:"update_record"`
}

// recordFieldsWrite is one tool's half: the shape per record_type, and the
// things a caller CANNOT see from a shape.
type recordFieldsWrite struct {
	Fields map[string]string `json:"fields"`
	Notes  []string          `json:"notes"`
}

// recordFieldsVersion identifies the document's shape, not its content. A
// caller caching it needs to know when the SHAPE changed; the field lists move
// with crm.yaml on their own schedule and are re-read either way.
const recordFieldsVersion = "1"

func recordFieldsDocument() recordFieldsDoc {
	return recordFieldsDoc{
		Version:  recordFieldsVersion,
		Notation: recordFieldsNotation,
		Create:   recordFieldsWrite{Fields: createRecordShapes, Notes: fieldNotes(createShapes)},
		Update:   recordFieldsWrite{Fields: updateRecordShapes, Notes: fieldNotes(updateShapes)},
	}
}

// fieldNotes returns what a caller cannot see from a field list: which fields
// lie, which are not fields at all, and which key shape reaches a custom field.
//
// Each note is keyed on a field the shapes actually declare, so it appears in
// the section it is true of — the create half and the patch half disagree about
// several of them, and the wrong advice is worse than none.
func fieldNotes(shapes map[datasource.EntityType]reflect.Type) []string {
	notes := []string{"A task is record_type=activity with kind=task."}
	notes = append(notes, provenanceStampNote(shapes)...)
	notes = append(notes, dealPipelineNote(shapes)...)
	notes = append(notes, relationshipNote(shapes))
	notes = append(notes, activityReachNotes(shapes)...)
	return append(notes, customFieldNotes(shapes)...)
}

// provenanceStampNote says that naming `source` does not set it.
//
// Only where the shapes actually carry the field. On create it is accepted and
// then overwritten, so believing it took effect would be wrong; on update no
// request type has it at all, so it is REFUSED as an unknown key — opposite
// advice, and one shared sentence used to give the create one to both. Derived
// from the shapes so it cannot drift from which write this is.
func provenanceStampNote(shapes map[datasource.EntityType]reflect.Type) []string {
	if !describesField(shapes, "source") {
		return nil
	}
	return []string{"`source` is accepted but overwritten — this surface stamps its own provenance."}
}

// dealPipelineNote says where the two ids a deal cannot be born without come
// from. Naming them as required (which the shape does) without saying that is
// what made create_record/deal unusable: a caller was told exactly what it
// needed and had nowhere to get it.
func dealPipelineNote(shapes map[datasource.EntityType]reflect.Type) []string {
	if !describesField(shapes, "pipeline_id") {
		return nil
	}
	return []string{"A deal's `pipeline_id` and `stage_id` come from list_pipelines — nothing else on " +
		"this surface yields them, and neither is defaultable."}
}

// relationshipNote says what a relationship needs, because an edge's
// requirements are per-KIND and invisible from a flat field list: `kind`,
// `person_id`, `organization_id`, `deal_id` and `project_id` all read as equal
// optional siblings, and they are not. Which pair is required is decided by the
// kind and enforced by a database CHECK, so a caller working from names alone
// sends a plausible pair and gets a shape refusal it could not have predicted.
//
// Keyed on an ENDPOINT FIELD, not on the record type, because both shape maps
// carry relationship — the patch half serves it too. Only the create shape
// declares `counterparty_org_id`, so that is the honest test for "can the caller
// name an endpoint at all", which is what the pairing rule is about.
func relationshipNote(shapes map[datasource.EntityType]reflect.Type) string {
	if !describesField(shapes, "counterparty_org_id") {
		// The patch half still owes the reader the pointer, because a person's
		// employer is the field they will look for first and not find.
		return "A person's employer is NOT a field here: employment is a relationship, created and " +
			"archived as record_type=relationship — its endpoints are what it IS, so they cannot be patched."
	}
	// REQUIRES, not "and rejects any other". The schema's shape CHECKs pin the
	// pair each kind must have and forbid the endpoints that would contradict it,
	// but they do not forbid every irrelevant one — an employment edge will accept
	// a stray counterparty_org_id. Promising more than the constraints deliver
	// would be a document a caller could disprove.
	//
	// The closing sentence deliberately does NOT say "no read tool serves an
	// edge": read_record answers a relationship by id even though its enum does
	// not advertise one (the contract has no single-relationship GET), and a
	// document a caller can disprove in one call costs more than the silence it
	// replaced.
	return "A person's employer is a relationship, not a field on the person: record_type=relationship " +
		"with kind=employment, person_id and organization_id. Each kind REQUIRES its own endpoint pair, " +
		"and a wrong pair is refused by name — employment: person + organization; deal_stakeholder: " +
		"deal + person; project_stakeholder: project + person; partner_of, referred_by and co_sell_with: " +
		"organization + counterparty_org_id. An edge is not searchable, so keep the id a relationship " +
		"write returns."
}

// activityReachNotes says what an activity write does and does not let a caller
// reach afterwards: whether the record is searchable, and whether its links can
// be moved later.
//
// The record type gates both notes; the `links` field decides WHICH one. Both
// shape maps carry activity, so a record-type test alone put patch-only advice
// in the create section too — which DOES accept links.
func activityReachNotes(shapes map[datasource.EntityType]reflect.Type) []string {
	// An activity is not searchable, and only the EDGE was ever told this.
	// search_records' record_type enum has no activity, so an activity is
	// retrievable afterwards only through the id its write returned — the same
	// hazard the relationship note names, on the record type this surface creates
	// far more often. Keyed on the create shapes (which carry `links`), because it
	// is the write that hands out the only handle.
	if describesField(shapes, "links") {
		return []string{"An activity is not searchable — search_records does not serve it — so keep the id " +
			"an activity write returns; read_record answers it by that id."}
	}
	if _, hasActivity := shapes[datasource.EntityActivity]; !hasActivity {
		return nil
	}
	// The other field a caller reasonably expects and will not find. An activity
	// DOES carry links — log_activity and create_record both take them — so being
	// told the field is unknown, with no pointer, reads as "this is impossible"
	// rather than "this is a different verb".
	//
	// It says what is true and stops. Naming the relink action would be directing
	// the reader at something this surface does not serve: it is a REST operation
	// with no tool behind it, so an agent told to use it has been given an
	// instruction it cannot follow — worse than the silence, because it reads as
	// a route.
	return []string{"An activity's links are NOT patchable, here or by any tool on this surface: this " +
		"write changes what an activity says, never who it is about."}
}

// customFieldNotes says how an extra key is read, which of the two fates it
// meets, and which record types carry no custom fields at all.
func customFieldNotes(shapes map[datasource.EntityType]reflect.Type) []string {
	// Two different fates, and the sentence used to describe neither. It said
	// every extra key is "silently discarded", which is wrong in both directions:
	// a key that is not cf_-prefixed is REFUSED by name (rejectUnknownFields), so
	// an agent was told to distrust a success it would never receive — while the
	// one case that IS silently dropped, a cf_ key whose custom field is not
	// active, was the half the sentence glossed over. A UAT run found exactly
	// that: cf_employee_count answered 200 with a full record body and the value
	// nowhere in it.
	notes := []string{"Extra keys must be named cf_<slug> and are read as custom-field values; any other " +
		"key is REFUSED by name, so an unknown field is never a silent loss. A cf_ key whose custom " +
		"field is not ACTIVE in this workspace is the one that is: the write reports success and drops " +
		"the value, so re-read the record if you are unsure a cf_ value landed."}
	// …but not for every record type, and the exception is this surface's own
	// decision: a type takes custom fields only if its contract shape carries the
	// additionalProperties bag a cf_ value travels in, and activity and
	// relationship carry none. Promising carriage there would send an agent to
	// write a key the strict decoder refuses.
	//
	// DERIVED from the shapes, not listed: the exclusion is a property of the
	// generated contract, and agents may not import customfields to ask it (a
	// module never imports a sibling). So the same question its FieldObjects gate
	// asks — is there a catch-all — is asked here, of the shapes in hand.
	if without := typesWithoutCustomFieldCarriage(shapes); without != "" {
		notes = append(notes, "No custom fields on "+without+": those types carry no cf_ values at all, "+
			"so a cf_ key there is refused rather than stored.")
	}
	return notes
}

// describesField reports whether any record type in shapes accepts name.
func describesField(shapes map[datasource.EntityType]reflect.Type, name string) bool {
	for _, shape := range shapes {
		if slices.Contains(contractFieldNames(shape), name) {
			return true
		}
	}
	return false
}

// typesWithoutCustomFieldCarriage names the record types in shapes whose
// contract body has no additionalProperties catch-all, in EntityTypes() order so
// the text is byte-stable. Empty when every type in shapes carries one.
//
// The catch-all is a `json:"-"` MAP field — oapi-codegen's rendering of
// additionalProperties — and its presence is exactly "can this shape hold a key
// the schema does not name", which is what a cf_ value needs.
func typesWithoutCustomFieldCarriage(shapes map[datasource.EntityType]reflect.Type) string {
	var without []string
	for _, recordType := range datasource.EntityTypes() {
		shape, ok := shapes[recordType]
		if !ok {
			continue
		}
		if field, found := shape.FieldByName("AdditionalProperties"); found && field.Type.Kind() == reflect.Map {
			continue
		}
		without = append(without, string(recordType))
	}
	return strings.Join(without, " or ")
}
