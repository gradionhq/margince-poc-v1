// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What create_record and update_record accept, per record_type: the shapes the
// two tools are gated on, and the refusal a key outside them earns.
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
// reflecting off it means the tool refuses exactly what the decoder refuses.
//
// The shapes themselves are PUBLISHED rather than recited into the two tools'
// input schemas — see recordfieldsdoc.go, which renders them as a document and
// says why the tools name it instead of carrying it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

// recordFieldsDescription is the `fields` description both write tools carry.
//
// It NAMES the vocabulary rather than reciting it. Reciting it cost 18% of the
// whole tool listing — every record type's shape and every advisory, in two
// tools, held by every client on every session and re-sent on every step of
// every Surface-B run — to answer a question one call asks once. The document is
// the same text, published at RecordFieldsURI, and margince://schema/query
// already establishes that a vocabulary belongs there.
//
// What stays is what a caller needs BEFORE it can decide to go and read: that
// the document exists, and what happens to a key this tool does not know. The
// second half is why the first is safe — rejectUnknownFields refuses by name and
// answers with the whole accepted list, so a caller that writes without reading
// is corrected rather than left to guess again.
//
// It NAMES the document rather than ordering a read, and that difference is
// measured rather than stylistic. Phrased as an instruction — "Read <uri>
// first" — a weak binding obeyed it eagerly: ministral spent its one graded turn
// fetching the vocabulary in scenarios with no write coming at all, including
// ones whose right answer was to stop.
//
// It says a wrong guess is REFUSED, and deliberately does not say how many calls
// recovering takes. Unknown-key and shape validation are separate gates and
// neither reports more than one fault, so two distinct mistakes cost two calls —
// a UAT run proved exactly that. Promising one would be a sentence a caller can
// disprove, which costs more than the silence it replaced.
const recordFieldsDescription = "The crm.yaml body for the record_type. The fields each " +
	"record_type takes, which of them are REQUIRED, and their shapes are published at " +
	RecordFieldsURI + " — that document, not this description, is what says what a write may " +
	"name. An extra key must be cf_<slug> for a custom field; any other key is refused BY NAME " +
	"and never dropped in silence, so a wrong guess is answered with the vocabulary rather than lost."

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
const stageIDNote = `,"description":"The target stage, by id — obtain it from list_pipelines, since a deal you have read carries only the stage it is already IN. That stage's semantic decides what happens next: open executes immediately, won or lost is staged for a human's approval."`

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
