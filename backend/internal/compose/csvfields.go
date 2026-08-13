// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

const (
	// leadStatusNew is where an imported prospect starts: the unworked state a
	// human moves it out of, which is the whole point of landing
	// machine-sourced rows as leads rather than as people.
	leadStatusNew = "new"
	// customFieldColumnPrefix marks the mapping targets that are custom-field
	// columns — real columns on the same table, which is why they are mappable
	// at all (customfields/engine.go mints them).
	customFieldColumnPrefix = "cf_"
)

// The fields a CSV import may target, per object. A closed set, because a
// mapping names a destination and every destination here has to be one this
// writer knows how to both create and update — a target it can only create
// would silently stop honouring the file on the second upload.
//
// `lead` and not `person` is ADR-0008: machine-sourced rows land as leads and
// a human promotes them.
var csvTargets = map[string][]string{
	migration.ObjectLead:         {"full_name", "email", "title", "company_name", "linkedin_url"},
	migration.ObjectOrganization: {"display_name", "legal_name", "industry", "size_band", "description", "linkedin_url"},
}

// csvSourceKeyDefault names the column a run identifies rows by when the
// request supplies none: the field that is the object's own natural identity.
// Stated per object rather than guessed, and the report says which was used.
var csvSourceKeyDefault = map[string]string{
	migration.ObjectLead:         "email",
	migration.ObjectOrganization: "display_name",
}

// importTargets is the closed set a mapping may name for one object: the core
// fields above plus the installation's active custom-field columns, which are
// real columns on the same table and are mappable for exactly that reason.
func importTargets(ctx context.Context, catalog fieldcatalog.Reader, object string) ([]string, error) {
	core, ok := csvTargets[object]
	if !ok {
		return nil, fmt.Errorf("import: %q has no mappable fields", object)
	}
	targets := append([]string(nil), core...)
	if catalog == nil {
		return targets, nil
	}
	columns, err := catalog.ActiveColumns(ctx, object)
	if err != nil {
		return nil, fmt.Errorf("import: reading the %s field catalog: %w", object, err)
	}
	for _, c := range columns {
		targets = append(targets, c.Name)
	}
	return targets, nil
}

// changedFields reports which mapped values differ from what the record
// already holds.
//
// The comparison goes through the record's own JSON rather than a hand-written
// per-field comparator: the wire shape and the mapping targets are the same
// vocabulary, so a field added to the contract is compared automatically, while
// a comparator would keep compiling and quietly stop noticing it.
func changedFields(record any, mapped map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("import: reading the stored record: %w", err)
	}
	var current map[string]any
	if err := json.Unmarshal(encoded, &current); err != nil {
		return nil, fmt.Errorf("import: reading the stored record: %w", err)
	}

	changed := make(map[string]any, len(mapped))
	for field, incoming := range mapped {
		if !sameValue(current[field], incoming) {
			changed[field] = incoming
		}
	}
	return changed, nil
}

// sameValue compares a stored value with an imported one as text. Every value
// a delimited file can carry arrives as text, so text is the only comparison
// that can be made without inventing a type the file never declared.
func sameValue(stored, incoming any) bool {
	if stored == nil {
		return false
	}
	return strings.TrimSpace(fmt.Sprint(stored)) == strings.TrimSpace(fmt.Sprint(incoming))
}

// leadCreateFrom builds the create input for one mapped row. Only mapped
// fields are set: an absent column leaves the field absent rather than
// clearing it, because the file said nothing about it.
func leadCreateFrom(fields map[string]any, sourceSystem, externalID, source string) people.CreateLeadInput {
	in := people.CreateLeadInput{
		Status:       leadStatusNew,
		SourceSystem: &sourceSystem,
		SourceID:     &externalID,
		Source:       source,
		CustomFields: customFieldsFrom(fields),
	}
	in.FullName = importString(fields, "full_name")
	in.Email = importString(fields, "email")
	in.Title = importString(fields, "title")
	in.CompanyName = importString(fields, "company_name")
	in.LinkedInURL = importString(fields, "linkedin_url")
	return in
}

// leadUpdateFrom builds the patch for the fields that actually differ.
func leadUpdateFrom(changed map[string]any) people.UpdateLeadInput {
	return people.UpdateLeadInput{
		FullName:     importString(changed, "full_name"),
		Email:        importString(changed, "email"),
		Title:        importString(changed, "title"),
		CompanyName:  importString(changed, "company_name"),
		CustomFields: customFieldsFrom(changed),
	}
}

func organizationCreateFrom(fields map[string]any, source string) people.CreateOrganizationInput {
	in := people.CreateOrganizationInput{
		DisplayName:  strings.TrimSpace(fmt.Sprint(valueOr(fields, "display_name", ""))),
		Source:       source,
		CustomFields: customFieldsFrom(fields),
	}
	in.Industry = importString(fields, "industry")
	if band := importString(fields, "size_band"); band != nil {
		in.SizeBand = band
	}
	return in
}

func organizationUpdateFrom(changed map[string]any) people.UpdateOrganizationInput {
	return people.UpdateOrganizationInput{
		DisplayName:  importString(changed, "display_name"),
		LegalName:    importString(changed, "legal_name"),
		Description:  importString(changed, "description"),
		Industry:     importString(changed, "industry"),
		SizeBand:     importString(changed, "size_band"),
		LinkedInURL:  importString(changed, "linkedin_url"),
		CustomFields: customFieldsFrom(changed),
	}
}

// customFieldsFrom carries the cf_* targets through to the stores' own
// custom-field handling, which drops anything the active catalog does not
// admit. Naming them here rather than filtering would duplicate that catalog.
func customFieldsFrom(fields map[string]any) map[string]any {
	var out map[string]any
	for name, value := range fields {
		if !strings.HasPrefix(name, customFieldColumnPrefix) {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(fields))
		}
		out[name] = value
	}
	return out
}

// importString reads one mapped field as a pointer, absent when the file did
// not carry it. A nil is "the file said nothing", never "the file said empty":
// the source drops blank values before they reach here, so an empty column
// cannot silently erase a value somebody entered by hand.
func importString(fields map[string]any, name string) *string {
	value, ok := fields[name]
	if !ok {
		return nil
	}
	s := strings.TrimSpace(fmt.Sprint(value))
	if s == "" {
		return nil
	}
	return &s
}

func valueOr(fields map[string]any, name string, fallback any) any {
	if v, ok := fields[name]; ok {
		return v
	}
	return fallback
}
