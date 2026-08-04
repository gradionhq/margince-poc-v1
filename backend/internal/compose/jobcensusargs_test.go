// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the args census counts as a field, held to what encoding/json actually
// writes into river_job.args.
//
// No args type in this fleet embeds, encodes itself, or is anything but a
// struct today, so these are fixtures for the first one that does. That is the
// whole reason they are here: a walk that stopped at an embedded field would
// report one name — the type's — while the fields underneath it reached the
// column, and every check built on the census (the declared-vs-compiled
// comparison, the content-word suspicion arm) would be looking straight past
// them.

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/riverqueue/river"
)

// censusEnvelope is the embedded half: exactly the kind of struct whose fields
// a declaration must still account for one level down.
type censusEnvelope struct {
	Body    string
	Subject string
}

type censusEmbeddingArgs struct {
	censusEnvelope
	Workspace string
}

func (censusEmbeddingArgs) Kind() string { return "census_embedding_fixture" }

type censusPointerEmbeddingArgs struct {
	*censusEnvelope
	Workspace string
}

func (censusPointerEmbeddingArgs) Kind() string { return "census_pointer_embedding_fixture" }

// censusTaggedEmbeddingArgs is the case that is NOT flattened: a tag names the
// embedding, so JSON writes a nested object and it is one field like any other.
type censusTaggedEmbeddingArgs struct {
	censusEnvelope `json:"envelope"`
	Workspace      string
}

func (censusTaggedEmbeddingArgs) Kind() string { return "census_tagged_embedding_fixture" }

// censusSelfEmbeddingArgs embeds a pointer to itself, which is legal Go and
// would recur forever through a walk that followed embeddings blindly.
type censusSelfEmbeddingArgs struct {
	//nolint:unused // read by reflection, never by name: the embedding IS the fixture.
	*censusSelfEmbeddingArgs
	Workspace string
}

func (censusSelfEmbeddingArgs) Kind() string { return "census_self_embedding_fixture" }

// censusOmittedArgs carries the two shapes that reach river_job.args by no
// path: a field encoding/json cannot write because it is unexported, and one
// whose tag tells it not to. Neither is an undeclared payload — neither is a
// payload — and demanding a declaration for them would put a line in
// api/jobs.yaml saying a job carries something it does not.
type censusOmittedArgs struct {
	Workspace string
	Internal  string `json:"-"`
	secret    string
}

func (censusOmittedArgs) Kind() string { return "census_omitted_fixture" }

// censusShadowedArgs carries a field of its own with the same name as one it
// promotes. The outer field is the one JSON writes; the promoted one is
// shadowed and reaches the column by no name at all.
type censusShadowedArgs struct {
	censusEnvelope
	Body      string
	Workspace string
}

func (censusShadowedArgs) Kind() string { return "census_shadowed_fixture" }

// censusOtherEnvelope collides with censusEnvelope on Body at the same depth,
// which is the case the walk gets wrong by counting rather than by reading:
// encoding/json drops BOTH, so a declaration for Body would describe a field no
// row carries.
type censusOtherEnvelope struct {
	Body string
	Ref  string
}

type censusAmbiguousArgs struct {
	censusEnvelope
	censusOtherEnvelope
	Workspace string
}

func (censusAmbiguousArgs) Kind() string { return "census_ambiguous_fixture" }

// censusTaggedRef and censusPlainRef collide on the JSON key "Ref" one level
// down, and the tag is what settles it: json writes the TAGGED one. The census
// reports Go names, so the two spellings have to be kept apart — the key
// decides the winner and the field name is what comes back.
type censusTaggedRef struct {
	//nolint:tagliatelle // the tag names the key its untagged sibling's Go name
	// takes; a snake_case one would collide with nothing and the fixture would
	// stand for no conflict at all.
	TaggedRef string `json:"Ref"`
}

type censusPlainRef struct {
	Ref string
}

type censusTagBreaksTheTieArgs struct {
	censusTaggedRef
	censusPlainRef
	Workspace string
}

func (censusTagBreaksTheTieArgs) Kind() string { return "census_tag_tie_fixture" }

// censusSelfEncodingArgs writes its own object. Its declared field is not what
// lands in the column, and no walk over its fields could tell.
type censusSelfEncodingArgs struct {
	Workspace string
}

func (censusSelfEncodingArgs) Kind() string { return "census_self_encoding_fixture" }

func (censusSelfEncodingArgs) MarshalJSON() ([]byte, error) {
	return []byte(`{"Body":"the census never saw this"}`), nil
}

// censusInheritedEncodingArgs never mentions an encoder: it embeds a type whose
// MarshalJSON is promoted onto it, which is the same defect one indirection
// further away.
type censusInheritedEncodingArgs struct {
	censusSelfEncodingArgs
	Workspace string
}

func (censusInheritedEncodingArgs) Kind() string { return "census_inherited_encoding_fixture" }

// censusScalarArgs is a JobArgs that is not a struct at all. River marshals it
// to a JSON scalar, so there is no field for a declaration to name and no
// bound on what the value holds.
type censusScalarArgs string

func (censusScalarArgs) Kind() string { return "census_scalar_fixture" }

func TestArgsFieldNamesReadsAnArgsStructAsJSONWritesIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		args river.JobArgs
		want []string
	}{
		{
			name: "an embedded struct contributes its own fields",
			args: censusEmbeddingArgs{},
			want: []string{"Body", "Subject", "Workspace"},
		},
		{
			name: "an embedded pointer does the same",
			args: censusPointerEmbeddingArgs{},
			want: []string{"Body", "Subject", "Workspace"},
		},
		{
			name: "a tagged embedding is one nested field",
			args: censusTaggedEmbeddingArgs{},
			want: []string{"censusEnvelope", "Workspace"},
		},
		{
			name: "a self-embedding is walked once",
			args: censusSelfEmbeddingArgs{},
			want: []string{"Workspace"},
		},
		{
			name: "a field JSON never writes carries nothing",
			args: censusOmittedArgs{},
			want: []string{"Workspace"},
		},
		{
			name: "a field of the struct's own shadows the one it promotes",
			args: censusShadowedArgs{},
			want: []string{"Subject", "Body", "Workspace"},
		},
		{
			name: "two promoted fields of one name are written by neither",
			args: censusAmbiguousArgs{},
			want: []string{"Subject", "Ref", "Workspace"},
		},
		{
			name: "a tag beats an untagged sibling at the same depth",
			args: censusTagBreaksTheTieArgs{},
			want: []string{"TaggedRef", "Workspace"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := argsFieldNames(tc.args)
			if err != nil {
				t.Fatalf("reading %T: %v", tc.args, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("argsFieldNames = %v, want %v — the census would then hold the declaration to a different set of fields than the ones River persists", got, tc.want)
			}
		})
	}
}

// TestAnArgsTypeTheWalkCannotSeeThroughIsRefused is the args gate's own
// falsification. Everything the census claims about what a job carries rests on
// reading DECLARED FIELDS, and the failure that costs nothing to miss is the
// one where the bytes reach river_job.args by a route that has no fields: the
// type then reports as empty, its declaration as complete, and the payload
// nobody declared sits in a table with no workspace column and no RLS.
func TestAnArgsTypeTheWalkCannotSeeThroughIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		args river.JobArgs
		want string
	}{
		{
			name: "a type that writes its own object",
			args: censusSelfEncodingArgs{},
			want: "encodes ITSELF",
		},
		{
			name: "a type that inherited the encoder from an embedding",
			args: censusInheritedEncodingArgs{},
			want: "encodes ITSELF",
		},
		{
			name: "a type that is not a struct",
			args: censusScalarArgs("anything at all"),
			want: "rather than a struct",
		},
		{
			name: "no args value at all",
			args: nil,
			want: "recorded no args value",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := argsFieldNames(tc.args)
			if err == nil {
				t.Fatalf("%T was read as carrying %v; what it puts in river_job.args is decided somewhere this walk cannot see", tc.args, got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal for %T says %q, want it to name %q so the author knows what to write instead", tc.args, err, tc.want)
			}
		})
	}
}

// TestTheWalkCountsWhatTheEncoderWritesForEveryRegisteredKind holds the model to
// the population that matters. The fixtures above are the shapes somebody
// thought of; this is every args type the fleet actually registers, put through
// the real encoder and compared against what the census claims it carries. A
// rule jobargsjsonfields.go gets wrong on a real kind is a field the contract
// either invents or never demands, and either way nothing else would notice.
//
// The value put through the encoder is FILLED first, because a field that asks
// to be skipped when empty — site_deep_read's page ceiling does — would
// otherwise be absent from a zero value and the kind would read as one key
// short of what the census counts.
func TestTheWalkCountsWhatTheEncoderWritesForEveryRegisteredKind(t *testing.T) {
	census, err := NewJobCensus()
	if err != nil {
		t.Fatalf("building the job census: %v", err)
	}
	counted := 0
	for _, reading := range census.readArgs() {
		if reading.err != nil {
			t.Errorf("%v", reading.err)
			continue
		}
		raw, err := filledArgsJSON(t, census.wired[reading.kind].args)
		if err != nil {
			t.Errorf("%s: River could not marshal its args at all: %v", reading.kind, err)
			continue
		}
		var written map[string]json.RawMessage
		if err := json.Unmarshal(raw, &written); err != nil {
			t.Errorf("%s: River persists %s, which is not the JSON object api/jobs.yaml governs field by field: %v", reading.kind, raw, err)
			continue
		}
		if len(written) != len(reading.fields) {
			t.Errorf("%s: the encoder writes %d keys (%s) and the census counts %d fields (%v) — the walk follows a rule encoding/json does not, and the difference is a field the contract either invents or never demands a declaration for.",
				reading.kind, len(written), raw, len(reading.fields), reading.fields)
		}
		counted++
	}
	if counted < declaredJobKindFloor {
		t.Fatalf("compared the args of only %d registered kinds, expected at least %d — the census matched almost nothing and this gate would pass vacuously", counted, declaredJobKindFloor)
	}
}

// filledArgsJSON is what River would persist for an args value with every field
// the encoder could skip as empty set to something, so the keys counted above
// are the SET of fields rather than the sample that happened to be non-zero.
//
// Only what becomes a TOP-LEVEL key is filled: whatever sits inside a field's
// own object is that field's content, and the count is of keys.
func filledArgsJSON(t *testing.T, args river.JobArgs) ([]byte, error) {
	t.Helper()
	filled := reflect.New(reflect.TypeOf(args)).Elem()
	filled.Set(reflect.ValueOf(args))
	fillTopLevelFields(t, filled)
	return json.Marshal(filled.Interface())
}

func fillTopLevelFields(t *testing.T, v reflect.Value) {
	t.Helper()
	for i := range v.NumField() {
		field, declared := v.Field(i), v.Type().Field(i)
		if !field.CanSet() {
			continue
		}
		if declared.Anonymous && field.Kind() == reflect.Struct {
			fillTopLevelFields(t, field) // a promoted field is a top-level key like any other.
			continue
		}
		if !skippedWhenEmpty(declared) {
			continue
		}
		switch field.Kind() {
		case reflect.String:
			field.SetString("filled")
		case reflect.Bool:
			field.SetBool(true)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			field.SetInt(1)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			field.SetUint(1)
		case reflect.Float32, reflect.Float64:
			field.SetFloat(1)
		case reflect.Slice:
			field.Set(reflect.MakeSlice(field.Type(), 1, 1))
		case reflect.Pointer:
			field.Set(reflect.New(field.Type().Elem()))
		default:
			t.Errorf("%s.%s asks the encoder to skip it when empty, and this gate cannot make a %s non-empty — the kind would read as one key short and the count above would blame the walk for it; fill this kind here",
				v.Type(), declared.Name, field.Kind())
		}
	}
}

// skippedWhenEmpty reports a field whose tag lets the encoder leave it out.
func skippedWhenEmpty(field reflect.StructField) bool {
	_, options, _ := strings.Cut(field.Tag.Get("json"), ",")
	return slices.Contains(strings.Split(options, ","), "omitempty") ||
		slices.Contains(strings.Split(options, ","), "omitzero")
}

// A tie the tag settles is the one conflict whose winner a key set cannot show:
// both fields would be written under "Ref". The VALUE is what says which of
// them the encoder took, and the Go name of that one is what the census has to
// hold the declaration to.
func TestTheTaggedFieldIsTheOneJSONActuallyWrites(t *testing.T) {
	raw, err := json.Marshal(censusTagBreaksTheTieArgs{
		censusTaggedRef: censusTaggedRef{TaggedRef: "from the tagged field"},
		censusPlainRef:  censusPlainRef{Ref: "from the untagged one"},
		Workspace:       "w",
	})
	if err != nil {
		t.Fatalf("marshalling the fixture: %v", err)
	}
	var written struct {
		Ref string
	}
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatalf("reading back %s: %v", raw, err)
	}
	if written.Ref != "from the tagged field" {
		t.Errorf("River would persist Ref as %q, want the tagged field's value — the census names the losing field and the declaration would govern the wrong one", written.Ref)
	}
}

// The self-encoding fixture earns its place only if it really does write
// something else. A MarshalJSON the encoder never called would make the refusal
// above a rule about a shape that does not exist.
func TestTheSelfEncodingFixtureReallyWritesSomethingItsFieldsDoNot(t *testing.T) {
	raw, err := json.Marshal(censusSelfEncodingArgs{Workspace: "w"})
	if err != nil {
		t.Fatalf("marshalling the fixture: %v", err)
	}
	var written map[string]json.RawMessage
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatalf("reading back %s: %v", raw, err)
	}
	if _, carried := written["Body"]; !carried || len(written) != 1 {
		t.Errorf("River would persist %s, want the single Body key the type's own encoder writes — the fixture no longer stands for a type whose fields are not what it carries", raw)
	}
}

// The walk above is a claim about ANOTHER package's behaviour — what
// encoding/json puts in the column — and such a claim is worth exactly what it
// is held to. So the two fixtures whose reading is not obvious are marshalled
// here and the actual object's keys are read back: an unexported field and a
// `-` tag really are absent, and the unexported EMBEDDING really is present,
// which is why the exportedness rule cannot simply be applied first.
//
// Only the top level is compared. What json nests under a name is that field's
// content, and the census's question is which fields a job carries.
func TestTheFieldsTheCensusCountsAreTheOnesJSONWrites(t *testing.T) {
	for _, tc := range []struct {
		name string
		args river.JobArgs
		want []string
	}{
		{
			name: "an unexported field and a dashed one are written by neither",
			args: censusOmittedArgs{Workspace: "w", Internal: "i", secret: "s"},
			want: []string{"Workspace"},
		},
		{
			name: "a tagged embedding is written, unexported type and all",
			args: censusTaggedEmbeddingArgs{
				censusEnvelope: censusEnvelope{Body: "b", Subject: "s"},
				Workspace:      "w",
			},
			want: []string{"Workspace", "envelope"},
		},
		{
			name: "a shadowed promoted field is written once, by the shadowing one",
			args: censusShadowedArgs{
				censusEnvelope: censusEnvelope{Body: "promoted", Subject: "s"},
				Body:           "the struct's own",
				Workspace:      "w",
			},
			want: []string{"Body", "Subject", "Workspace"},
		},
		{
			name: "two promoted fields of one name are written by neither",
			args: censusAmbiguousArgs{
				censusEnvelope:      censusEnvelope{Body: "one", Subject: "s"},
				censusOtherEnvelope: censusOtherEnvelope{Body: "the other", Ref: "r"},
				Workspace:           "w",
			},
			want: []string{"Ref", "Subject", "Workspace"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.args)
			if err != nil {
				t.Fatalf("marshalling the fixture: %v", err)
			}
			var written map[string]json.RawMessage
			if err := json.Unmarshal(raw, &written); err != nil {
				t.Fatalf("reading back %s: %v", raw, err)
			}
			if got := slices.Sorted(maps.Keys(written)); !slices.Equal(got, tc.want) {
				t.Errorf("River would persist %v, want %v — the census is filtering by a rule encoding/json does not follow", got, tc.want)
			}
		})
	}
}
