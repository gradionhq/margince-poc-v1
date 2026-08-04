// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The census's args half: what a registered kind's struct actually puts in
// river_job.args, joined to what api/jobs.yaml says it carries.
//
// It is a file of its own because the question is a different one from the
// rest of the census. Every other arm compares a declared VALUE against a
// wired one; this one has to decide first what the compiled type even
// contributes to the column — JSON inlines an embedding, drops an unexported
// field, and honours a `-` tag — and that reading is the whole substance of
// the check.

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// JobArgsField is one field of one registered kind's COMPILED args struct,
// joined to what api/jobs.yaml says about it. The compiled side leads: the
// question a caller asks of this is what a job actually carries, and the
// declaration is the answer being checked, not the source of the question.
type JobArgsField struct {
	Kind   string
	GoType string
	Name   string
	// Declared is false for a field the contract says nothing about, which is
	// the finding rather than a default — a zero ArgField would read as a
	// declared id.
	Declared bool
	Scalar   bool
	Reason   string
}

// ArgsFields walks every registered kind's args struct, in kind order and then
// in field order, and reports each field beside its declaration.
func (c *JobCensus) ArgsFields() []JobArgsField {
	var fields []JobArgsField
	for _, kind := range slices.Sorted(maps.Keys(c.wired)) {
		spec, declared := jobs.SpecFor(kind)
		for _, name := range argsFieldNames(c.wired[kind].args) {
			field := JobArgsField{Kind: kind, GoType: spec.GoType, Name: name}
			if declared {
				if arg, found := declaredArg(spec, name); found {
					field.Declared, field.Scalar, field.Reason = true, arg.Scalar, arg.Reason
				}
			}
			fields = append(fields, field)
		}
	}
	return fields
}

// declaredArg answers the declaration for one args field, and whether there is
// one at all.
func declaredArg(spec jobs.Spec, name string) (jobs.ArgField, bool) {
	for _, field := range spec.Args {
		if field.Name == name {
			return field, true
		}
	}
	return jobs.ArgField{}, false
}

// everyArgsFieldIsDeclaredAndBack is what makes the args declaration a proof
// rather than a description. River persists args verbatim in a table with no
// workspace column and no RLS, so a field carrying a body or an address would
// be a second store of subject data that Art. 17 erasure never reaches. The
// generator already refuses a declared field that is neither an id nor an
// argued-for scalar; holding the declared set EQUAL to the compiled set is
// what closes the gap between "every declared field is safe" and "every field
// is safe".
func (c *JobCensus) everyArgsFieldIsDeclaredAndBack() []string {
	var findings []string
	for kind, spec := range jobs.Declared() {
		entry, wired := c.wired[kind]
		if !wired {
			continue // already reported by the totality check.
		}
		compiled := argsFieldNames(entry.args)
		for _, field := range spec.Args {
			if !slices.Contains(compiled, field.Name) {
				findings = append(findings, fmt.Sprintf(
					"%s declares an args field %s that %s does not have — a declaration for a field nobody carries governs nothing", kind, field.Name, spec.GoType))
			}
		}
	}
	// The other direction reads the compiled fields, which ArgsFields already
	// joins to the declaration for its own callers.
	for _, field := range c.ArgsFields() {
		if !field.Declared {
			findings = append(findings, fmt.Sprintf(
				"%s.%s is not declared in api/jobs.yaml — say what it carries: `%s: id`, or a scalar with the reason it is safe in a table Art. 17 erasure never reaches", field.GoType, field.Name, field.Name))
		}
	}
	return findings
}

// argsFieldNames is the fields an args struct puts in river_job.args, in
// declaration order and under the Go names the declaration spells them with.
// River marshals args to a JSON object, so a non-struct carries no fields at
// all and answers none.
//
// An EMBEDDED struct is walked THROUGH rather than counted as one field,
// because flattening is what actually reaches the column: encoding/json lifts
// an anonymous field's own fields into the enclosing object unless a tag names
// it. Reported as its type name, a Body sitting one level down would satisfy
// every check here while landing in the args verbatim, which is the one thing
// the args declaration exists to prevent.
func argsFieldNames(args river.JobArgs) []string {
	return structFieldNames(reflect.TypeOf(args), nil)
}

// structFieldNames walks one struct type, inlining what JSON would inline and
// dropping what JSON would drop. enclosing carries the types already on the
// path: a struct may embed a pointer to itself, which is legal Go and would
// otherwise recur forever.
func structFieldNames(t reflect.Type, enclosing []reflect.Type) []string {
	if t == nil || t.Kind() != reflect.Struct || slices.Contains(enclosing, t) {
		return nil
	}
	names := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		field := t.Field(i)
		if omittedFromArgs(field) {
			continue
		}
		if inner, inlined := inlinedStruct(field); inlined {
			names = append(names, structFieldNames(inner, append(enclosing, t))...)
			continue
		}
		names = append(names, field.Name)
	}
	return names
}

// omittedFromArgs reports a field encoding/json never writes, and therefore one
// that never lands in river_job.args.
//
// The question this whole file answers is what a job CARRIES, so a field that
// reaches the column by no path is not an undeclared payload — it is not a
// payload at all. Demanding a declaration for it would put a line in
// api/jobs.yaml saying a job carries something it does not, which is the
// declared-versus-actual gap this contract removes, read backwards.
//
// Only the exact `-` tag is the skip directive: `json:"-,"` names a field "-"
// and is written like any other. The Go name is what both sides of the census
// are keyed by, so the distinction matters here and the rename does not.
func omittedFromArgs(field reflect.StructField) bool {
	if tag, tagged := field.Tag.Lookup("json"); tagged && tag == "-" {
		return true
	}
	if field.IsExported() {
		return false
	}
	// An unexported field is the encoder's to drop, with one exception it is
	// not: an EMBEDDED struct, which json reaches through either way — its
	// exported fields are promoted into the enclosing object (inlinedStruct,
	// below), or it is written as a nested one when a tag names it. Both reach
	// the column, so neither may be dropped here.
	return !field.Anonymous || embeddedStruct(field.Type) == nil
}

// embeddedStruct is the struct type an anonymous field stands for, following
// the one pointer an embedding may be, and nil when the field embeds something
// that is not a struct at all.
func embeddedStruct(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	return t
}

// inlinedStruct reports the struct an anonymous field contributes its own
// fields to the enclosing object, and whether the field is such an embedding at
// all. A tag that names the field is what stops the inlining: JSON then writes
// a nested object under that name, and the embedding is one field like any
// other.
func inlinedStruct(field reflect.StructField) (reflect.Type, bool) {
	if !field.Anonymous || jsonName(field) != "" {
		return nil, false
	}
	t := embeddedStruct(field.Type)
	return t, t != nil
}

// jsonName is the name a field's json tag gives it, which is the part before
// the first option and is empty when the tag only carries options.
func jsonName(field reflect.StructField) string {
	tag, tagged := field.Tag.Lookup("json")
	if !tagged {
		return ""
	}
	name, _, _ := strings.Cut(tag, ",")
	return name
}

// goTypeName is the Go type name of a registered value: the worker behind a
// kind, which is what a Work method's receiver is spelled as, or the args value
// its type parameter named, which is what api/jobs.yaml's go_type states.
// Workers are registered as pointers, so the pointer is followed; the name
// alone is enough because every worker and args type in this fleet lives in
// this one package.
func goTypeName(v any) string {
	t := reflect.TypeOf(v)
	if t == nil {
		return ""
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}
