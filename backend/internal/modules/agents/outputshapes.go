// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The declared shape of every tool result, derived from the Go type the handler
// marshals.
//
// WHY DERIVED AND NOT GENERATED INTO A FILE. A generated file needs a drift gate
// to stop a hand edit, and the gate exists because the file CAN disagree with
// the type. Reading the type at registration removes the disagreement instead of
// policing it: there is one statement of the shape, and the schema is a view of
// it. What a generated file would have bought — schemas a reviewer can read in a
// diff — is bought instead by a golden snapshot test, which renders every schema
// to a committed file and fails when one moves without the change being looked
// at.
//
// This also settles where the generator could have lived. `backend/tools` is its
// own module and cannot import `backend/internal/...`, so a reflecting generator
// would have had to parse the AST and re-implement Go's own type resolution —
// for types that are right here.
//
// WHAT A SCHEMA CLAIMS. `additionalProperties` is deliberately NOT set, so every
// schema reads "at least these fields". That is the honest claim for two
// different reasons at once: a result carrying a field this build does not know
// is not a violation a client should reject, and — for the tools whose handler
// passes another module's entity straight through (PassthroughEntityResult,
// AvailabilityResult, RunReportResult) — the declared fields really are a
// subset by construction.

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The JSON Schema type tokens this surface emits. Named once, because the
// deriver writes them and the checker reads them, and a typo in either half
// would make a schema and its own check disagree silently.
const (
	schemaObject  = "object"
	schemaArray   = "array"
	schemaString  = "string"
	schemaBoolean = "boolean"
	schemaInteger = "integer"
	schemaNumber  = "number"
)

// rawMessageType is the one self-marshalling type the walk looks THROUGH rather
// than at: it holds an already-encoded document, so its schema is that
// document's, which this surface cannot know.
var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// schemaFor renders T's JSON Schema, ONCE per type for the life of the process.
//
// The caching is not a micro-optimization: Spec() is called on a REQUEST path —
// Registry.Invoke reads it to admit a call, and the dispatcher reads it again to
// validate the answer — so a schemaFor that walked the type each time would run
// a full reflection descent and a marshal on every tool call, for a value that
// cannot change.
//
// It panics on a type it cannot describe, which is a defect in a result type. A
// tool's Spec is built while the composition wires the registry, so that is
// where it first fires: at boot, at the only moment it can still be fixed.
func schemaFor[T any]() json.RawMessage {
	return derivedSchema[T]()
}

// derivedSchema is the once-per-type derivation schemaFor hands out.
//
// The cache is a map keyed by the reflect.Type rather than a per-instantiation
// sync.OnceValue, because Go does not admit a generic function value to hang one
// off. Registration is single-threaded and the reads that follow are concurrent,
// so the mutex guards the write-then-read ordering rather than contention: after
// boot every lookup is a read.
func derivedSchema[T any]() json.RawMessage {
	var zero T
	t := reflect.TypeOf(&zero).Elem()

	schemaCache.mu.RLock()
	cached, ok := schemaCache.byType[t]
	schemaCache.mu.RUnlock()
	if ok {
		return cached
	}

	schema, err := describeType(t)
	if err != nil {
		//craft:ignore panic-in-domain composition-time schema derivation — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: cannot describe %s as a result schema: %v", t, err))
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		//craft:ignore panic-in-domain composition-time schema derivation — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: cannot encode the result schema for %s: %v", t, err))
	}
	schemaCache.mu.Lock()
	defer schemaCache.mu.Unlock()
	schemaCache.byType[t] = raw
	return raw
}

// schemaCache holds one derivation per result type for the life of the process.
var schemaCache = struct {
	mu     sync.RWMutex
	byType map[reflect.Type]json.RawMessage
}{byType: map[reflect.Type]json.RawMessage{}}

// jsonSchema is the subset of JSON Schema this surface emits. It is a struct
// rather than a map so the member ORDER is fixed: a schema is embedded verbatim
// into tools/list, and a map would reorder its keys per process, which reads to
// a caching client as a tool that changed.
type jsonSchema struct {
	Type string `json:"type"`
	// Format carries the semantic a caller needs and Go's type does not have:
	// a uuid is a string, and a caller told only "string" will invent one.
	Format string `json:"format,omitempty"`
	// Description is carried only where a field's own type cannot say it.
	Description string                 `json:"description,omitempty"`
	Properties  map[string]*jsonSchema `json:"properties,omitempty"`
	Required    []string               `json:"required,omitempty"`
	Items       *jsonSchema            `json:"items,omitempty"`
	// AdditionalProperties describes a map's VALUES. For an object built from a
	// struct it stays nil, which is JSON Schema's "anything else is allowed" —
	// see the file comment for why that is the honest default here.
	//nolint:tagliatelle // additionalProperties is JSON Schema's own member name, camelCase by that spec
	AdditionalProperties *additionalProperties `json:"additionalProperties,omitempty"`
}

// additionalProperties is JSON Schema's own union: a BOOLEAN closing or opening
// the object, or a SCHEMA every extra member must satisfy.
//
// It is a union rather than just the schema because a hand-written schema is
// free to use either — `extensions/yogi` closes its result with `false`, and a
// reader that could not parse that would report a perfectly good schema as
// unreadable and withhold structured content from every call to that tool. The
// deriver only ever emits the schema arm, for a map's values.
type additionalProperties struct {
	// Closed is set when the member was a boolean. false means no member beyond
	// the declared ones is allowed; true is the default and says nothing.
	Closed *bool
	// Schema is set when the member was an object: every extra member has this
	// shape, which is how a Go map is described.
	Schema *jsonSchema
}

func (a *additionalProperties) UnmarshalJSON(raw []byte) error {
	var closed bool
	if err := json.Unmarshal(raw, &closed); err == nil {
		a.Closed = &closed
		return nil
	}
	var schema jsonSchema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("additionalProperties is neither a boolean nor a schema: %w", err)
	}
	a.Schema = &schema
	return nil
}

func (a additionalProperties) MarshalJSON() ([]byte, error) {
	if a.Closed != nil {
		return json.Marshal(*a.Closed)
	}
	return json.Marshal(a.Schema)
}

// textual reports whether a type marshals itself as a JSON string rather than as
// its Go representation — every id on this surface does, and a time does too.
//
// It is DERIVED from the type's own marshalling rather than matched against a
// list of names, because a list is a thing to keep current and the question has
// a real answer: a type that renders itself is not described by walking its
// fields. Without this an id would be advertised as an array of 16 integers,
// which is its memory and not its wire.
func textual(t reflect.Type) bool {
	for _, iface := range []reflect.Type{jsonMarshaler, textMarshaler} {
		if t.Implements(iface) || reflect.PointerTo(t).Implements(iface) {
			return true
		}
	}
	return false
}

var (
	jsonMarshaler = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshaler = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

// idFormat is the `format` an id-shaped string carries. Derived the same way:
// a type whose zero value renders as a canonical uuid IS one, and saying so is
// what lets a caller send the value back rather than invent one.
func idFormat(t reflect.Type) string {
	raw, err := json.Marshal(reflect.New(t).Elem().Interface())
	if err != nil {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return ""
	}
	if uuidShape.MatchString(text) {
		return "uuid"
	}
	return ""
}

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// describeType walks one Go type into its schema. Every branch is a wire fact,
// and an unrecognised kind is an error rather than a permissive default: a
// result type this cannot describe would otherwise be advertised as something
// looser than it is, which is the failure the exact schemas exist to end.
func describeType(t reflect.Type) (*jsonSchema, error) {
	// A POINTER is absence, which `required` already expresses — and it is
	// unwrapped FIRST, before anything asks what the type renders as. A pointer
	// to an id satisfies json.Marshaler through the value's method set, so a
	// walk that asked first would describe *ids.UUID as a string of no format:
	// idFormat marshals the type's zero value, and a nil pointer renders as
	// null rather than as an id. The caller would then be told the member is a
	// string and left to invent its shape.
	if t.Kind() == reflect.Pointer {
		return describeType(t.Elem())
	}
	// A type that renders itself is a string on the wire whatever its Go kind
	// is, and the walk must not look inside it. json.RawMessage is the one
	// exception and is handled next: it renders as whatever it holds.
	if t != rawMessageType && textual(t) {
		return &jsonSchema{Type: schemaString, Format: idFormat(t)}, nil
	}
	// json.RawMessage is a []byte that marshals as whatever it holds. It appears
	// where a handler carries another module's already-encoded document, and the
	// honest schema for that is an object with nothing said about its members.
	if t == rawMessageType {
		return &jsonSchema{Type: schemaObject}, nil
	}
	switch t.Kind() {
	case reflect.String:
		return &jsonSchema{Type: schemaString}, nil
	case reflect.Bool:
		return &jsonSchema{Type: schemaBoolean}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &jsonSchema{Type: schemaInteger}, nil
	case reflect.Float32, reflect.Float64:
		return &jsonSchema{Type: schemaNumber}, nil
	case reflect.Slice, reflect.Array:
		items, err := describeType(t.Elem())
		if err != nil {
			return nil, err
		}
		return &jsonSchema{Type: schemaArray, Items: items}, nil
	case reflect.Map:
		// A map's keys are the caller's, so only the value shape can be stated.
		values, err := describeType(t.Elem())
		if err != nil {
			return nil, err
		}
		return &jsonSchema{Type: schemaObject, AdditionalProperties: &additionalProperties{Schema: values}}, nil
	case reflect.Struct:
		return describeStruct(t)
	}
	return nil, fmt.Errorf("no JSON Schema for Go kind %s", t.Kind())
}

// describeStruct reads a struct's json tags — the wire names, which fields are
// optional, and which are flattened by an embedded type.
func describeStruct(t reflect.Type) (*jsonSchema, error) {
	out := &jsonSchema{Type: schemaObject, Properties: map[string]*jsonSchema{}}
	var required []string
	if err := eachWireField(t, func(name string, optional bool, field reflect.StructField) error {
		schema, err := describeType(field.Type)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", t.Name(), field.Name, err)
		}
		out.Properties[name] = schema
		if !optional {
			required = append(required, name)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// Sorted, because reflect reports fields in declaration order but an
	// embedded type's fields arrive out of line with them — and the list is
	// served verbatim, so it has to be the same on every process.
	sort.Strings(required)
	out.Required = required
	return out, nil
}

// eachWireField visits the fields a struct puts on the wire, flattening
// embedded ones exactly as encoding/json does: an anonymous struct field with no
// json tag contributes its OWN fields at this level, which is what
// UpdateWithStagedApprovalResult relies on to answer as a record plus a note.
func eachWireField(t reflect.Type, visit func(name string, optional bool, field reflect.StructField) error) error {
	for i := range t.NumField() {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if field.Anonymous && name == "" {
			if err := eachWireField(field.Type, visit); err != nil {
				return err
			}
			continue
		}
		if name == "" {
			// An unexported field is not on the wire at all; an exported one
			// with no tag would be, under a name nothing else in this surface
			// uses. Both are defects rather than shapes to describe.
			if !field.IsExported() {
				continue
			}
			return fmt.Errorf("%s.%s has no json tag, so its wire name is its Go name", t.Name(), field.Name)
		}
		if err := visit(name, strings.Contains(opts, "omitempty") || field.Type.Kind() == reflect.Pointer, field); err != nil {
			return err
		}
	}
	return nil
}
