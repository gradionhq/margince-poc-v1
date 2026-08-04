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
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := argsFieldNames(tc.args)
			if !slices.Equal(got, tc.want) {
				t.Errorf("argsFieldNames = %v, want %v — the census would then hold the declaration to a different set of fields than the ones River persists", got, tc.want)
			}
		})
	}
}
