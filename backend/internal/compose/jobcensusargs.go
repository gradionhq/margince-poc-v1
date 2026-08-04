// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The census's args half: what a registered kind's struct actually puts in
// river_job.args, joined to what api/jobs.yaml says it carries.
//
// It is a file of its own because the question is a different one from the
// rest of the census. Every other arm compares a declared VALUE against a
// wired one; this one has to decide first what the compiled type even
// contributes to the column — jobargsjsonfields.go is that reading, and it is
// the whole substance of the check — and then refuse the types for which the
// reading has no answer.

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"

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
//
// The error is every args type the walk cannot answer for, joined. A caller
// that got a short list instead would be holding a declaration to a population
// quietly missing the one type whose payload nothing can see.
func (c *JobCensus) ArgsFields() ([]JobArgsField, error) {
	var fields []JobArgsField
	var refused []error
	for _, reading := range c.readArgs() {
		if reading.err != nil {
			refused = append(refused, reading.err)
			continue
		}
		spec, declared := jobs.SpecFor(reading.kind)
		for _, name := range reading.fields {
			field := JobArgsField{Kind: reading.kind, GoType: spec.GoType, Name: name}
			if declared {
				if arg, found := declaredArg(spec, name); found {
					field.Declared, field.Scalar, field.Reason = true, arg.Scalar, arg.Reason
				}
			}
			fields = append(fields, field)
		}
	}
	if len(refused) > 0 {
		return nil, errors.Join(refused...)
	}
	return fields, nil
}

// argsReading is one registered kind's args type as the walk reads it: the Go
// names of the fields it puts in river_job.args, or the reason the type cannot
// be read at all. Both arms below need the same two answers, and a second walk
// could only disagree with the first.
type argsReading struct {
	kind   string
	fields []string
	err    error
}

// readArgs reads every registered kind's args type, in kind order.
func (c *JobCensus) readArgs() []argsReading {
	readings := make([]argsReading, 0, len(c.wired))
	for _, kind := range slices.Sorted(maps.Keys(c.wired)) {
		fields, err := argsFieldNames(c.wired[kind].args)
		if err != nil {
			err = fmt.Errorf("%s: %w", kind, err)
		}
		readings = append(readings, argsReading{kind: kind, fields: fields, err: err})
	}
	return readings
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
	for _, reading := range c.readArgs() {
		if reading.err != nil {
			findings = append(findings, reading.err.Error())
			continue
		}
		spec, declared := jobs.SpecFor(reading.kind)
		if !declared {
			continue // already reported by the totality check.
		}
		for _, field := range spec.Args {
			if !slices.Contains(reading.fields, field.Name) {
				findings = append(findings, fmt.Sprintf(
					"%s declares an args field %s that %s does not have — a declaration for a field nobody carries governs nothing", reading.kind, field.Name, spec.GoType))
			}
		}
		for _, name := range reading.fields {
			if _, found := declaredArg(spec, name); !found {
				findings = append(findings, fmt.Sprintf(
					"%s.%s is not declared in api/jobs.yaml — say what it carries: `%s: id`, or a scalar with the reason it is safe in a table Art. 17 erasure never reaches", spec.GoType, name, name))
			}
		}
	}
	return findings
}

// everyFanOutChildCarriesItsUnitKey holds the sweep-unit gauges to the rows
// they read. The pair counts a fan-out pass per DECLARED unit by grouping on
// one args key — jobs.FanOutUnit.ArgsKey — and a child whose struct does not
// write that key contributes nothing to the group: the SQL's own filter drops
// it, and the kind then reads as a pass with no units at all rather than as a
// pass nobody can measure. An absent series and an idle one are the same
// output, which is the failure mode the declared catalogue exists to end.
//
// It is checked against the ENCODER rather than against the field walk. What
// the metric groups on is a key in river_job.args, and the key is what
// encoding/json writes — a Go field named ConnectionID answers the walk while
// the column holds connection_id, so a name-level check would pass on a type
// whose rows the query cannot see.
//
// Every unit is held, workspace included, even though the workspace-unit kinds
// are not published in this pair: their key is what the per-workspace pair and
// the whole scoped read group on, so a child that stopped writing it would be
// invisible to those too.
func (c *JobCensus) everyFanOutChildCarriesItsUnitKey() []string {
	var findings []string
	units := jobs.FanOutUnits()
	for _, kind := range slices.Sorted(maps.Keys(units)) {
		wired, registered := c.wired[kind]
		if !registered {
			continue // already reported by the wired-and-back check.
		}
		key := units[kind].ArgsKey()
		if key == "" {
			findings = append(findings, fmt.Sprintf(
				"%s is fanned out to but its dispatcher declares no fan_out_unit, so nothing says what one of its rows stands for", kind))
			continue
		}
		written, err := argsJSONKeys(wired.args)
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: %v", kind, err))
			continue
		}
		if !slices.Contains(written, key) {
			findings = append(findings, fmt.Sprintf(
				"%s is fanned out per %s but its rows carry no %q key (they carry %v) — the sweep gauges group on that key, so every child of this kind would be dropped from the count rather than reported as a failure",
				kind, fanOutUnitName(units[kind]), key, written))
		}
	}
	return findings
}

// argsJSONKeys is the top-level keys an args value actually lands in
// river_job.args under, taken from encoding/json itself: River marshals the
// value verbatim, so the encoder's answer is the column's content by
// definition, and no re-derivation of it can be wrong in a different way.
//
// A zero value is enough because the question is about KEYS. Only a field
// declared `omitempty` could vary with content, and one on a fan-out child's
// unit id would be a defect of its own — an unset id that silently drops the
// key rather than writing an empty one.
func argsJSONKeys(args river.JobArgs) ([]string, error) {
	if args == nil {
		return nil, errors.New("the registration recorded no args value at all, so there is nothing to encode")
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("its args do not encode at all, so River could never enqueue one: %w", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, fmt.Errorf("its args encode to %s rather than to a JSON object, so river_job.args has no keys to group on: %w", encoded, err)
	}
	return slices.Sorted(maps.Keys(object)), nil
}

// argsFieldNames is the fields an args type puts in river_job.args, under the
// Go names the declaration spells them with and in the order the encoder writes
// them — or the reason the type carries a payload this walk cannot see.
//
// The error is this gate's own limit, stated rather than left as a silent
// empty answer. Everything below reads DECLARED FIELDS, so a type that decides
// its own encoding, or one that is not an object at all, puts bytes in a column
// with no workspace column and no RLS that no line in api/jobs.yaml can govern.
// Nothing in the fleet is written either way today, and the refusal is what
// keeps it that way: a gate that cannot see a whole encoding path is
// indistinguishable from one that found nothing wrong.
func argsFieldNames(args river.JobArgs) ([]string, error) {
	t := reflect.TypeOf(args)
	switch {
	case t == nil:
		return nil, errors.New("the registration recorded no args value at all, so there is nothing to read the carried fields off — register the kind with its args type")
	case marshalsItself(t):
		return nil, fmt.Errorf(
			"%s encodes ITSELF: what a row of this kind carries is decided by its own MarshalJSON/MarshalText rather than by its fields, so the declaration in api/jobs.yaml governs nothing and this walk sees nothing. Let the encoder write the struct — and if the method arrived by embedding, make the embedding a named field so what it writes sits under one declared key", t)
	case t.Kind() != reflect.Struct:
		return nil, fmt.Errorf(
			"%s is a %s rather than a struct, so what it carries has no field names to hold a declaration to. River persists args as one JSON object per row and api/jobs.yaml governs that object field by field: give the kind a struct whose fields can be named", t, t.Kind())
	}
	return jsonFieldsWritten(t), nil
}

// marshalsItself reports a type that hands the encoder finished bytes instead
// of its fields — including one that inherited the method from an embedding,
// which is the shape a reviewer is least likely to notice.
//
// Both receiver forms are refused. Whether a pointer method is actually REACHED
// depends on the value being addressable where the encoder meets it, which is a
// property of River's call site rather than of the args type; a gate resting on
// that would move when a dependency's internals do.
func marshalsItself(t reflect.Type) bool {
	pointer := reflect.PointerTo(t)
	for _, encoder := range []reflect.Type{
		reflect.TypeFor[json.Marshaler](),
		reflect.TypeFor[encoding.TextMarshaler](),
	} {
		if t.Implements(encoder) || pointer.Implements(encoder) {
			return true
		}
	}
	return false
}

// goTypeName is the Go type name a registration is read back under: the worker
// behind a kind, which is what a Work method's receiver is spelled as, or the
// args type api/jobs.yaml's go_type states. Workers are registered as pointers,
// so the pointer is followed; the name alone is enough because every worker and
// args type in this fleet lives in this one package.
//
// It takes the reflected type rather than the value because the two callers
// hold different things — an args interface and the worker the registry keeps —
// and the only thing this answers about either is its type. A nil type is the
// nil interface a caller reflected, and answers the empty name.
func goTypeName(t reflect.Type) string {
	if t == nil {
		return ""
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}
