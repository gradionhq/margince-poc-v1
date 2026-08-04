// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The field names create_record and update_record actually accept, per
// record_type, rendered into the tools' input schemas.
//
// Without them the `fields` argument is an opaque object and the only way to
// learn a name is to guess and read the error: a real session spent three
// round-trips discovering name → display_name for an organization and then
// display_name → full_name for a person, and never did find that a person's
// organization is not a field at all. A tool surface that requires trial and
// error to use is a tool surface that will be used wrongly.
//
// The names are REFLECTED off the generated contract structs rather than
// listed here, because a hand-copied list is a second source of truth that
// drifts the moment crm.yaml changes — and it would drift silently, in a
// description no test reads. internal/contracts is generated from crm.yaml, so
// reflecting off it means the tool describes exactly what the decoder accepts.

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// createShapes and updateShapes bind each record_type to the crm.yaml body its
// writes decode into. The keys are the seam's EntityType vocabulary, not
// re-spelled literals: record_type IS that vocabulary, and a tool describing a
// type the seam does not serve would be describing nothing.
var (
	createShapes = map[datasource.EntityType]reflect.Type{
		datasource.EntityPerson:       reflect.TypeFor[crmcontracts.CreatePersonRequest](),
		datasource.EntityOrganization: reflect.TypeFor[crmcontracts.CreateOrganizationRequest](),
		datasource.EntityDeal:         reflect.TypeFor[crmcontracts.CreateDealRequest](),
		datasource.EntityLead:         reflect.TypeFor[crmcontracts.CreateLeadRequest](),
		datasource.EntityActivity:     reflect.TypeFor[crmcontracts.CreateActivityRequest](),
		datasource.EntityProject:      reflect.TypeFor[crmcontracts.CreateProjectRequest](),
		datasource.EntityRelationship: reflect.TypeFor[crmcontracts.CreateRelationshipRequest](),
	}
	// An activity patch cannot reach its LINKS: UpdateActivityRequest declares
	// no link field, so this list has none to describe. A relationship patch
	// cannot reach its ENDPOINTS for the same structural reason and a stronger
	// domain one — an edge's ends are what it IS, so moving one is an archive
	// plus a new edge, never an update.
	updateShapes = map[datasource.EntityType]reflect.Type{
		datasource.EntityPerson:       reflect.TypeFor[crmcontracts.UpdatePersonRequest](),
		datasource.EntityOrganization: reflect.TypeFor[crmcontracts.UpdateOrganizationRequest](),
		datasource.EntityDeal:         reflect.TypeFor[crmcontracts.UpdateDealRequest](),
		datasource.EntityLead:         reflect.TypeFor[crmcontracts.UpdateLeadRequest](),
		datasource.EntityActivity:     reflect.TypeFor[crmcontracts.UpdateActivityRequest](),
		datasource.EntityProject:      reflect.TypeFor[crmcontracts.UpdateProjectRequest](),
		datasource.EntityRelationship: reflect.TypeFor[crmcontracts.UpdateRelationshipRequest](),
	}
)

// contractFieldNames reports the wire field names a contract body accepts, in
// a stable order. It reads json tags, so it sees exactly what the decoder
// binds: the AdditionalProperties catch-all (`json:"-"`) is not a field name
// and is described separately, since its accepted keys are per-workspace.
func contractFieldNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name := tag
		if comma := strings.IndexByte(tag, ','); comma >= 0 {
			name = tag[:comma]
		}
		if name == "" || name == "-" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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

// describeRecordFields renders the per-record_type field SHAPES for a tool's
// `fields` description, in a fixed record_type order so the schema text is
// byte-stable across processes (a description that reshuffles per boot reads
// as a changed tool to a client that caches it).
//
// Shapes, not names. A name list says `domains` and `links` in the same breath
// as `industry` and `subject`, and the first two are arrays of objects — the
// list gives a caller no way to see the difference, and two reported sessions
// guessed wrong and stopped. The shapes come from recordshapes_gen.go, which is
// generated from crm.yaml so the enum VALUES and the required keys come along;
// the Go structs this package reflects cannot yield either.
func describeRecordFields(shapes map[datasource.EntityType]reflect.Type, rendered map[string]string) string {
	// EntityTypes() fixes the order, so the text is byte-stable and a new
	// entity type shows up here instead of being quietly left undescribed.
	order := datasource.EntityTypes()
	var b strings.Builder
	b.WriteString("The crm.yaml body for the record_type. Field shapes below — a key with no `?` is ")
	b.WriteString("REQUIRED, `?` marks an optional one, and a name not listed is NOT stored (see the ")
	b.WriteString("end of this description): ")
	for i, recordType := range order {
		if _, ok := shapes[recordType]; !ok {
			continue
		}
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(string(recordType))
		b.WriteString(": ")
		b.WriteString(rendered[string(recordType)])
	}
	b.WriteString(". ")
	writeFieldAdvisories(&b, shapes)
	return b.String()
}

// writeFieldAdvisories appends the things a caller CANNOT see from a field list:
// which fields lie, which are not fields at all, and which key shape reaches a
// custom field. Each is keyed on a field the shapes actually declare, so an
// advisory appears on the tool it is true of — the create tool and the patch tool
// disagree about several of them, and the wrong advice is worse than none.
func writeFieldAdvisories(b *strings.Builder, shapes map[datasource.EntityType]reflect.Type) {
	b.WriteString("A task is record_type=activity with kind=task. ")
	writeStampedAndRequiredAdvisories(b, shapes)
	writeRelationshipAdvisory(b, shapes)
	writeActivityReachAdvisories(b, shapes)
	writeCustomFieldAdvisories(b, shapes)
}

// writeStampedAndRequiredAdvisories appends the advisories about fields a caller
// can name but cannot rely on: the one this surface overwrites, and the two it
// requires without being able to supply them.
func writeStampedAndRequiredAdvisories(b *strings.Builder, shapes map[datasource.EntityType]reflect.Type) {
	// Only where the shapes actually carry `source`. On create it is accepted
	// and then overwritten, so believing it took effect would be wrong; on
	// update no request type has it at all, so it is REFUSED as an unknown key
	// — opposite advice, and the shared sentence used to give the create one to
	// both. Derived from the shapes so it cannot drift from which tool this is.
	if describesField(shapes, "source") {
		b.WriteString("`source` is accepted but overwritten — this surface stamps its own provenance. ")
	}
	// Where the two ids a deal cannot be born without come from. Naming them as
	// required (which the mapping does) without saying that is what made
	// create_record/deal unusable: a caller was told exactly what it needed and
	// had nowhere to get it. Keyed on the field, so this sentence appears on the
	// tool whose shapes actually declare it and not on the patch tool.
	if describesField(shapes, "pipeline_id") {
		b.WriteString("A deal's `pipeline_id` and `stage_id` come from list_pipelines — nothing else ")
		b.WriteString("on this surface yields them, and neither is defaultable. ")
	}
}

// writeActivityReachAdvisories appends what an activity write does and does not
// let a caller reach afterwards: whether the record is searchable, and whether
// its links can be moved later.
//
// The links advisories are keyed on the PRESENCE or ABSENCE of the links field,
// not on the record type. Both shape maps carry activity, so a record-type test
// put patch-only advice on the create tool too — which DOES accept links. Same
// mistake as the endpoint advisory, in its sibling.
func writeActivityReachAdvisories(b *strings.Builder, shapes map[datasource.EntityType]reflect.Type) {
	// An activity is not searchable, and only the EDGE was ever told
	// this. search_records' record_type enum has no activity, so an activity is
	// retrievable afterwards only through the id its write returned — the same
	// hazard the relationship advisory names, on the record type this surface
	// creates far more often. Keyed on the create shapes (which carry `links`),
	// because it is the write that hands out the only handle.
	if describesField(shapes, "links") {
		b.WriteString("An activity is not searchable — search_records does not serve it — so keep the id ")
		b.WriteString("an activity write returns; read_record answers it by that id. ")
	}
	_, hasActivity := shapes[datasource.EntityActivity]
	if hasActivity && !describesField(shapes, "links") {
		// Says what is true and stops. Naming the relink action would be
		// directing the reader at something this surface does not serve: it is
		// a REST operation with no tool behind it, so an agent told to use it
		// has been given an instruction it cannot follow — worse than the
		// silence, because it reads as a route.
		b.WriteString("An activity's links are NOT patchable, here or by any tool on this surface: ")
		b.WriteString("this tool changes what an activity says, never who it is about. ")
	}
}

// writeCustomFieldAdvisories appends how an extra key is read, which of the two
// fates it meets, and which record types carry no custom fields at all.
func writeCustomFieldAdvisories(b *strings.Builder, shapes map[datasource.EntityType]reflect.Type) {
	// Two different fates, and the sentence used to describe neither. It said
	// every extra key is "silently discarded", which is wrong in both
	// directions: a key that is not cf_-prefixed is REFUSED by name (this file's
	// rejectUnknownFields), so an agent was told to distrust a success it would
	// never receive — while the one case that IS silently dropped, a cf_ key
	// whose custom field is not active, was the half the sentence glossed over.
	// A UAT run found exactly that: cf_employee_count answered 200 with a full
	// record body and the value nowhere in it.
	b.WriteString("Extra keys must be named cf_<slug> and are read as custom-field values; ")
	b.WriteString("any other key is REFUSED by name, so an unknown field is never a silent ")
	b.WriteString("loss. A cf_ key whose custom field is not ACTIVE in this workspace is the ")
	b.WriteString("one that is: the write reports success and drops the value, so re-read the ")
	b.WriteString("record if you are unsure a cf_ value landed.")
	// …but not for every record type, and the exception is this surface's own
	// decision: a type takes custom fields only if its contract shape carries the
	// additionalProperties bag a cf_ value travels in, and activity and
	// relationship carry none. Promising carriage there would send an agent to
	// write a key the strict decoder refuses.
	//
	// DERIVED from the shapes, not listed: the exclusion is a property of the
	// generated contract, and agents may not import customfields to ask it
	// (a module never imports a sibling). So the same question its FieldObjects
	// gate asks — is there a catch-all — is asked here, of the shapes in hand.
	if without := typesWithoutCustomFieldCarriage(shapes); without != "" {
		b.WriteString(" No custom fields on " + without + ": those types carry no cf_ values at all, ")
		b.WriteString("so a cf_ key there is refused rather than stored.")
	}
}

// typesWithoutCustomFieldCarriage names the record types in shapes whose contract
// body has no additionalProperties catch-all, in EntityTypes() order so the text
// is byte-stable. Empty when every type in shapes carries one.
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

// customFieldPrefix is the only shape an extra key may take: the
// customfields engine derives every column it adds as cf_<slug>, so a key
// without that prefix is not a custom field under any workspace's catalog.
const customFieldPrefix = "cf_"

// rejectUnknownFields refuses a `fields` payload carrying keys the record type
// cannot store, naming them and the ones it accepts.
//
// This lives at the TOOL, not in the store, and that placement is the whole
// point. The store's silence is contract-conformant: the write bodies declare
// additionalProperties: true, and storekit's package doc ratifies
// drop-on-mismatch. So REST may keep accepting-and-discarding, while the tool
// surface — whose caller cannot see a response body it did not think to
// re-read — refuses up front instead of reporting success for a write it did
// not perform. Two sessions lost data to that silence: organization_id on a
// person create, and emails on a person UPDATE, which is a real field on
// create and no field at all on update.
//
// A cf_-prefixed key passes: whether that custom field is active in this
// workspace is the store's ratified question, not a shape this tool can judge.
func rejectUnknownFields(shapes map[datasource.EntityType]reflect.Type, recordType string, fields json.RawMessage) error {
	shape, ok := shapes[datasource.EntityType(recordType)]
	if !ok {
		// An unknown record_type is the provider's refusal to make, and it
		// names the served vocabulary when it does.
		return nil
	}
	var submitted map[string]json.RawMessage
	if err := json.Unmarshal(fields, &submitted); err != nil {
		// `fields` is a JSON object in every record type's contract, so a
		// payload that is not one is the caller's mistake and says so here —
		// carrying the decoder's own words rather than discarding them.
		return &BadArgsError{Cause: fmt.Errorf("fields must be a JSON object: %w", err)}
	}
	// A literal null decodes into a nil map with NO error and therefore no
	// unknown keys, so it would pass this check and reach the provider as a
	// write carrying no fields at all.
	if submitted == nil {
		return &BadArgsError{Cause: errors.New("fields must be a JSON object, not null")}
	}
	accepted := make(map[string]struct{})
	for _, name := range contractFieldNames(shape) {
		accepted[name] = struct{}{}
	}
	var unknown []string
	for key := range submitted {
		// The prefix alone is not a field name: `cf_` names no slug, so it
		// can match no catalog column and would be discarded like any other
		// unknown key.
		if _, ok := accepted[key]; ok || len(key) > len(customFieldPrefix) && strings.HasPrefix(key, customFieldPrefix) {
			continue
		}
		unknown = append(unknown, key)
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	// Split along PROVENANCE, not along sentence structure: the refused keys are
	// the caller's own text and ride in Cause, where the echo bound applies, while
	// the accepted list is reflected off the contract and rides in Guidance, which
	// is not bounded. Bounded together, one long unknown key consumes the whole
	// budget and cuts the accepted list mid-word — deleting the actionable half of
	// a message whose reader has just proved it does not know the vocabulary.
	//
	// The claim is about this tool's VOCABULARY, never about what the record can
	// store: an activity stores links, so "cannot store links" would be false and
	// would send the caller looking for the wrong fix.
	return &BadArgsError{
		Cause:    fmt.Errorf("%s does not accept %s", recordType, strings.Join(unknown, ", ")),
		Guidance: "accepts " + strings.Join(contractFieldNames(shape), ", ") + " (or cf_<slug> for an active custom field)",
	}
}

// timestampNote is appended to every date-time argument the tool surface
// takes. "format": "date-time" is RFC 3339, which REQUIRES a zone offset — but
// a model reading the bare format keyword writes a local wall-clock time, and
// the decoder then refuses a value that looks correct. Two failed calls were
// spent on exactly that before the reason was visible, so the requirement is
// stated where it is read rather than left implied by a keyword.
const timestampNote = `,"description":"RFC 3339 with a zone offset — 2026-07-31T16:35:00+07:00 or 2026-07-31T09:35:00Z. A bare local time without an offset is refused."`

// stageIDNote is appended to every stage-id argument the tool surface takes.
// Two tools declared it as a bare format:uuid, which named the requirement
// without making it obtainable: the id lives in pipeline configuration, and
// until list_pipelines existed nothing on this surface yielded one. Saying where
// it comes from is the difference between a correct refusal and a dead end. The
// semantic half is here because it is what decides the tier — a caller that
// picks a stage without reading it cannot tell an immediate move from one that
// will wait on a human.
const stageIDNote = `,"description":"The target stage. Obtain it from list_pipelines — no other tool yields a stage id. That stage's semantic decides what happens next: open executes immediately, won or lost is staged for a human's approval."`

// jsonString renders s as a JSON string literal so a description built at init
// time can be spliced into a hand-written schema literal safely.
//
// strconv.Quote, not json.Marshal, because it cannot fail: this runs while a
// tool spec is being built, where there is no error to return and nothing
// honest to degrade to. Go and JSON string quoting agree on every character
// these descriptions contain; they diverge only on control characters (Go emits
// \x1b, which JSON rejects), and TestDescriptionsCarryNoControlCharacters
// forbids those rather than leaving the difference to chance.
func jsonString(s string) string {
	return strconv.Quote(s)
}

// writeRelationshipAdvisory says what a relationship needs, because an edge's
// requirements are per-KIND and invisible from a flat field list: `kind`,
// `person_id`, `organization_id`, `deal_id` and `project_id` all read as equal
// optional siblings, and they are not. Which pair is required is decided by the
// kind and enforced by a database CHECK, so a caller working from names alone
// sends a plausible pair and gets a shape refusal it could not have predicted.
//
// Keyed on an ENDPOINT FIELD, not on the record type, because both shape maps
// carry relationship — the patch tool serves it too. Only the create shape
// declares `counterparty_org_id`, so that is the honest test for "can the caller
// name an endpoint at all", which is what the pairing rule is about.
func writeRelationshipAdvisory(b *strings.Builder, shapes map[datasource.EntityType]reflect.Type) {
	if describesField(shapes, "counterparty_org_id") {
		b.WriteString("A person's employer is a relationship, not a field on the person: ")
		b.WriteString("record_type=relationship with kind=employment, person_id and organization_id. ")
		// REQUIRES, not "and rejects any other". The schema's shape CHECKs pin the
		// pair each kind must have and forbid the endpoints that would contradict
		// it, but they do not forbid every irrelevant one — an employment edge will
		// accept a stray counterparty_org_id. Promising more than the constraints
		// deliver would be a description a caller could disprove.
		b.WriteString("Each kind REQUIRES its own endpoint pair, and a wrong pair is refused by name — ")
		b.WriteString("employment: person + organization; deal_stakeholder: deal + person; ")
		b.WriteString("project_stakeholder: project + person; partner_of, referred_by and ")
		b.WriteString("co_sell_with: organization + counterparty_org_id. ")
		// An edge is not searchable: search_records' record_type enum has no
		// relationship, and there is no list-relationships tool. So the id a write
		// returns is the only handle on the edge this surface will give out.
		//
		// It deliberately does NOT say "no read tool serves an edge" — read_record
		// answers a relationship by id even though its enum does not advertise one
		// (the contract has no single-relationship GET), and a description a caller
		// can disprove in one call costs more than the silence it replaced.
		b.WriteString("An edge is not searchable, so keep the id a relationship write returns. ")
	} else {
		// The patch tool still owes the reader the pointer, because a person's
		// employer is the field they will look for first and not find.
		b.WriteString("A person's employer is NOT a field here: employment is a relationship, ")
		b.WriteString("created and archived as record_type=relationship — its endpoints are what it ")
		b.WriteString("IS, so they cannot be patched. ")
	}
}
