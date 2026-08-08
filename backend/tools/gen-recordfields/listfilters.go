// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// list_records' filter vocabulary, derived from the list operations.
//
// The other half of this generator renders what a WRITE body takes; this half
// renders what a READ may be narrowed by. They share the contract document and
// the schema walker and nothing else, which is why they are separate files.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---- list_records' filter vocabulary, derived from the list operations ----// listOperations binds each enumerable record_type to the crm.yaml operation
// that lists it. A139 pins the rule this implements: `list_records` accepts
// "exactly those the REST list operation already declares — owner,
// stage/status, updated_since, cursor — read off the contract rather than
// authored". So this file reads the operation's own parameters; it never
// carries a filter name of its own.
var listOperations = []struct{ recordType, operationID string }{
	{"person", "listPeople"},
	{"organization", "listOrganizations"},
	{"deal", "listDeals"},
	{"lead", "listLeads"},
	{"project", "listProjects"},
}

// notAFieldFilter names the inline parameters that are NOT filters on the
// record's own fields, with the reason each is out. They are excluded here
// rather than in the tool so the exclusion is one list with one rationale,
// visible beside the derivation it modifies.
var notAFieldFilter = map[string]string{
	// `q` is a text query, and search_records is the verb that owns it. #576
	// exists because the two were conflated; carrying `q` here would rebuild
	// that conflation inside the tool meant to end it.
	"q": "text search is search_records' verb",
	// `include_anchor` toggles the discovery narrowing every read of an
	// organization carries. It widens what is visible rather than filtering it,
	// which is a governance question and not a caller's filter.
	"include_anchor": "widens discovery rather than filtering",
}

// listFilter is one derived filter: the parameter name, the JSON type its
// operand takes, and the closed vocabulary when the contract declares one.
type listFilter struct {
	Name string
	Type string
	Enum []string
}

// operation is one path item's operation, decoded for its id and parameters.
type operation struct {
	OperationID string       `yaml:"operationId"`
	Parameters  []*parameter `yaml:"parameters"`
}

type parameter struct {
	Ref    string `yaml:"$ref"`
	Name   string `yaml:"name"`
	In     string `yaml:"in"`
	Schema *node  `yaml:"schema"`
}

// operationsByID indexes every declared operation by its operationId. Only the
// HTTP verb keys are decoded: a path item's own `parameters` sequence shares the
// map with them and is not an operation.
func operationsByID(paths map[string]map[string]yaml.Node) (map[string]*operation, error) {
	verbs := map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}
	byID := map[string]*operation{}
	for path, item := range paths {
		for key, raw := range item {
			if !verbs[key] {
				continue
			}
			var op operation
			if err := raw.Decode(&op); err != nil {
				return nil, fmt.Errorf("decoding %s %s: %w", strings.ToUpper(key), path, err)
			}
			if op.OperationID != "" {
				byID[op.OperationID] = &op
			}
		}
	}
	return byID, nil
}

// listFiltersFor reads one operation's INLINE query parameters. The $ref'd ones
// (cursor, limit, sort, include_archived, captured_by_kind, ai_written) are the
// contract's shared REST affordances rather than per-type filters: the tool
// declares cursor and limit itself, and the rest are human list controls whose
// meaning on an agent surface is a separate question from this one.
func listFiltersFor(op *operation) []listFilter {
	var filters []listFilter
	for _, p := range op.Parameters {
		if p.Ref != "" || p.In != "query" || p.Schema == nil {
			continue
		}
		if _, excluded := notAFieldFilter[p.Name]; excluded {
			continue
		}
		filters = append(filters, listFilter{
			Name: p.Name,
			Type: firstTypeName(p.Schema),
			Enum: enumValues(p.Schema),
		})
	}
	sort.Slice(filters, func(i, j int) bool { return filters[i].Name < filters[j].Name })
	return filters
}

// firstTypeName reduces a parameter's declared type to the one JSON type name
// its operand takes, ignoring an OpenAPI 3.1 nullable union's 'null' member.
func firstTypeName(schema *node) string {
	for name := range typeNames(schema) {
		if name != "null" {
			return name
		}
	}
	return "string"
}

// renderListFilters emits the per-record_type filter table list_records builds
// its InputSchema from.
func renderListFilters(b *strings.Builder, paths map[string]map[string]yaml.Node) error {
	byID, err := operationsByID(paths)
	if err != nil {
		return err
	}
	b.WriteString("\n// listRecordFilters is what list_records may be asked to filter by, per\n")
	b.WriteString("// record_type, read off each list operation's OWN declared query parameters\n")
	b.WriteString("// (A139). A filter here that the record's store cannot answer fails\n")
	b.WriteString("// TestEveryAdvertisedListFilterReachesTheStore.\n")
	b.WriteString("var listRecordFilters = map[string][]listFilter{\n")
	for _, entry := range listOperations {
		op, ok := byID[entry.operationID]
		if !ok {
			return fmt.Errorf("crm.yaml declares no operation %s (the list operation for %s)",
				entry.operationID, entry.recordType)
		}
		filters := listFiltersFor(op)
		if len(filters) == 0 {
			return fmt.Errorf("%s declares no inline query parameters, so %s could only be enumerated unfiltered",
				entry.operationID, entry.recordType)
		}
		b.WriteString("\t" + strconv.Quote(entry.recordType) + ": {\n")
		for _, f := range filters {
			b.WriteString("\t\t{Name: " + strconv.Quote(f.Name) + ", Type: " + strconv.Quote(f.Type))
			if len(f.Enum) > 0 {
				b.WriteString(", Enum: []string{")
				for i, v := range f.Enum {
					if i > 0 {
						b.WriteString(", ")
					}
					b.WriteString(v)
				}
				b.WriteString("}")
			}
			b.WriteString("},\n")
		}
		b.WriteString("\t},\n")
	}
	b.WriteString("}\n")
	return nil
}
