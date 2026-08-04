// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the args census counts as a field, held to what encoding/json actually
// writes into river_job.args.
//
// No args type in this fleet embeds today, so these are fixtures for the first
// one that does. That is the whole reason they are here: a walk that stopped
// at an embedded field would report one name — the type's — while the fields
// underneath it reached the column, and every check built on the census (the
// declared-vs-compiled comparison, the content-word suspicion arm) would be
// looking straight past them.

import (
	"encoding/json"
	"maps"
	"slices"
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := argsFieldNames(tc.args)
			if !slices.Equal(got, tc.want) {
				t.Errorf("argsFieldNames = %v, want %v — the census would then hold the declaration to a different set of fields than the ones River persists", got, tc.want)
			}
		})
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
