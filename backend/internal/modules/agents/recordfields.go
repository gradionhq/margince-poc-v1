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
	}
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
	// The two traps a caller cannot see from the field list alone, and the
	// second one is why a write can look like it worked and not have.
	b.WriteString("A person's employer is NOT a field here: employment is a relationship record, ")
	b.WriteString("which this tool cannot create. ")
	b.WriteString("Extra keys are read as custom-field values and must be named cf_<slug> for a ")
	b.WriteString("custom field ACTIVE in this workspace; any other key is silently discarded, ")
	b.WriteString("so re-read the record if you are unsure a value landed.")
	return b.String()
}

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
