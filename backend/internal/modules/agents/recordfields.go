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
	}
	// An activity is updatable but not archivable or mergeable through these
	// tools, which is why the two maps are not the same set. What an activity
	// patch cannot reach is its LINKS: those move through the relink verb,
	// which is a separate governed action, and UpdateActivityRequest declares
	// no link field for this list to describe.
	updateShapes = map[datasource.EntityType]reflect.Type{
		datasource.EntityPerson:       reflect.TypeFor[crmcontracts.UpdatePersonRequest](),
		datasource.EntityOrganization: reflect.TypeFor[crmcontracts.UpdateOrganizationRequest](),
		datasource.EntityDeal:         reflect.TypeFor[crmcontracts.UpdateDealRequest](),
		datasource.EntityLead:         reflect.TypeFor[crmcontracts.UpdateLeadRequest](),
		datasource.EntityActivity:     reflect.TypeFor[crmcontracts.UpdateActivityRequest](),
		datasource.EntityProject:      reflect.TypeFor[crmcontracts.UpdateProjectRequest](),
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

// describeRecordFields renders the per-record_type field lists for a tool's
// `fields` description, in a fixed record_type order so the schema text is
// byte-stable across processes (a description that reshuffles per boot reads
// as a changed tool to a client that caches it).
func describeRecordFields(shapes map[datasource.EntityType]reflect.Type) string {
	// EntityTypes() fixes the order, so the text is byte-stable and a new
	// entity type shows up here instead of being quietly left undescribed.
	order := datasource.EntityTypes()
	var b strings.Builder
	b.WriteString("The crm.yaml body for the record_type. Accepted field names — ")
	b.WriteString("anything else is NOT stored (see below): ")
	for i, recordType := range order {
		shape, ok := shapes[recordType]
		if !ok {
			continue
		}
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(string(recordType))
		b.WriteString(": ")
		b.WriteString(strings.Join(contractFieldNames(shape), ", "))
	}
	b.WriteString(". A task is record_type=activity with kind=task. ")
	// Only where the shapes actually carry `source`. On create it is accepted
	// and then overwritten, so believing it took effect would be wrong; on
	// update no request type has it at all, so it is REFUSED as an unknown key
	// — opposite advice, and the shared sentence used to give the create one to
	// both. Derived from the shapes so it cannot drift from which tool this is.
	if describesField(shapes, "source") {
		b.WriteString("`source` is accepted but overwritten — this surface stamps its own provenance. ")
	}
	// The two traps a caller cannot see from the field list alone, and the
	// second one is why a write can look like it worked and not have.
	b.WriteString("A person's employer is NOT a field here: employment is a relationship record, ")
	b.WriteString("which this tool cannot create. ")
	b.WriteString("Extra keys are read as custom-field values and must be named cf_<slug> for a ")
	b.WriteString("custom field ACTIVE in this workspace; any other key is silently discarded, ")
	b.WriteString("so re-read the record if you are unsure a value landed.")
	return b.String()
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
	return &BadArgsError{Cause: fmt.Errorf("%s cannot store %s; it accepts %s (or cf_<slug> for an active custom field)",
		recordType, strings.Join(unknown, ", "), strings.Join(contractFieldNames(shape), ", "))}
}

// timestampNote is appended to every date-time argument the tool surface
// takes. "format": "date-time" is RFC 3339, which REQUIRES a zone offset — but
// a model reading the bare format keyword writes a local wall-clock time, and
// the decoder then refuses a value that looks correct. Two failed calls were
// spent on exactly that before the reason was visible, so the requirement is
// stated where it is read rather than left implied by a keyword.
const timestampNote = `,"description":"RFC 3339 with a zone offset — 2026-07-31T16:35:00+07:00 or 2026-07-31T09:35:00Z. A bare local time without an offset is refused."`

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
