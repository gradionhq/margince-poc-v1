// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Which of a struct's fields encoding/json writes as keys of the object it
// marshals to, answered by Go field name.
//
// It is a PORT of encoding/json's own typeFields rather than a reading of it,
// because the rules are not the ones a reader would guess and each one changes
// the answer: an untagged embedding's fields are promoted into the enclosing
// object, a shallower field shadows a promoted one of the same name, a tagged
// field beats an untagged sibling at the same depth, and two that beat each
// other equally are BOTH dropped. The args census exists to hold api/jobs.yaml
// to what actually lands in river_job.args, so an approximation here fails in
// the direction the whole check exists to catch: a field the walk cannot see is
// a field no declaration is demanded for, sitting in a table with no workspace
// column and no RLS.
//
// The JSON key decides which candidate survives and the GO name is what comes
// back. The two differ on nearly every field in this fleet (`workspace_id` for
// Workspace), and the declaration is written in Go names.

import (
	"cmp"
	"reflect"
	"slices"
	"strings"
	"unicode"
)

// jsonTagPunctuation is the punctuation encoding/json admits in a tag name.
// A tag containing anything else is not a name at all — the encoder ignores it
// and writes the field under its Go name — and a walk that took it for one
// would report a key nothing is written under.
const jsonTagPunctuation = "!#$%&()*+-./:;<=>?@[]^_{|}~ "

// jsonCandidate is one field the walk reached: the key JSON would write it
// under, the Go name the census reports it by, its index path (a longer one is
// a deeper promotion), and whether a tag named it. The last two are what decide
// which of several candidates for one key survives.
type jsonCandidate struct {
	key    string
	goName string
	index  []int
	tagged bool
}

// embeddedLevel is one untagged embedded struct queued for the next round of
// the walk, with the index path that reaches it.
type embeddedLevel struct {
	typ   reflect.Type
	index []int
}

// jsonFieldsWritten is the Go names of the fields encoding/json writes for t,
// in the order it writes them. t must be a struct: argsFieldNames is what
// refuses every type this cannot answer for.
func jsonFieldsWritten(t reflect.Type) []string {
	written := jsonDominantFields(jsonCandidateFields(t))
	slices.SortFunc(written, func(a, b jsonCandidate) int { return slices.Compare(a.index, b.index) })
	names := make([]string, 0, len(written))
	for _, field := range written {
		names = append(names, field.goName)
	}
	return names
}

// jsonCandidateFields is every field the encoder could write, before the
// same-key conflicts are settled. It walks embeddings breadth-first, so a
// candidate's depth is its index path's length and the shallower one is
// reached first.
//
// visited holds a struct type to ONE round: a type embedded on two paths
// contributes its fields once, and a type that embeds a pointer to itself —
// legal Go — terminates rather than recurring forever.
func jsonCandidateFields(t reflect.Type) []jsonCandidate {
	var found []jsonCandidate
	current := []embeddedLevel{}
	next := []embeddedLevel{{typ: t}}
	// count is how many times a type was queued for the level being walked.
	// More than once is what makes two embeddings of one type annihilate each
	// other: a second copy of every field found through it is recorded, so the
	// conflict rule below sees a duplicate and drops both.
	var count, nextCount map[reflect.Type]int
	visited := map[reflect.Type]bool{}

	for len(next) > 0 {
		current, next = next, current[:0]
		count, nextCount = nextCount, map[reflect.Type]int{}
		for _, level := range current {
			if visited[level.typ] {
				continue
			}
			visited[level.typ] = true
			for i := range level.typ.NumField() {
				field := level.typ.Field(i)
				if !reachedByJSON(field) {
					continue
				}
				index := append(slices.Clone(level.index), i)
				key, tagged := jsonKey(field)
				if inner, promotes := promotedStruct(field, tagged); promotes {
					nextCount[inner]++
					if nextCount[inner] == 1 {
						next = append(next, embeddedLevel{typ: inner, index: index})
					}
					continue
				}
				found = append(found, jsonCandidate{key: key, goName: field.Name, index: index, tagged: tagged})
				if count[level.typ] > 1 {
					found = append(found, found[len(found)-1])
				}
			}
		}
	}
	return found
}

// jsonDominantFields settles the candidates for each key: the shallowest wins,
// a tag breaks a tie at equal depth, and two that tie on both are dropped
// entirely — an ambiguous promoted field is written by neither name.
func jsonDominantFields(found []jsonCandidate) []jsonCandidate {
	slices.SortFunc(found, byKeyThenDepthThenTag)
	written := make([]jsonCandidate, 0, len(found))
	for i := 0; i < len(found); {
		sameKey := 1
		for i+sameKey < len(found) && found[i+sameKey].key == found[i].key {
			sameKey++
		}
		if dominant, ok := dominantCandidate(found[i : i+sameKey]); ok {
			written = append(written, dominant)
		}
		i += sameKey
	}
	return written
}

// dominantCandidate is the one candidate for a key that the encoder writes, and
// whether any of them is. The slice is sorted, so the first is the winner and
// the only question left is whether the second ties with it.
func dominantCandidate(forOneKey []jsonCandidate) (jsonCandidate, bool) {
	tied := len(forOneKey) > 1 &&
		len(forOneKey[0].index) == len(forOneKey[1].index) &&
		forOneKey[0].tagged == forOneKey[1].tagged
	if tied {
		return jsonCandidate{}, false
	}
	return forOneKey[0], true
}

// byKeyThenDepthThenTag groups the candidates by key and puts the dominant one
// of each group first: shallowest, then tagged before untagged, then in
// declaration order so the ordering is total and the walk answers the same on
// every process.
func byKeyThenDepthThenTag(a, b jsonCandidate) int {
	if c := strings.Compare(a.key, b.key); c != 0 {
		return c
	}
	if c := cmp.Compare(len(a.index), len(b.index)); c != 0 {
		return c
	}
	if a.tagged != b.tagged {
		if a.tagged {
			return -1
		}
		return +1
	}
	return slices.Compare(a.index, b.index)
}

// reachedByJSON reports a field the encoder can write at all. A field it cannot
// reaches river_job.args by no path, and demanding a declaration for it would
// put a line in api/jobs.yaml saying a job carries something it does not.
//
// Only the exact `-` tag is the skip directive: `json:"-,"` names a field "-"
// and is written like any other.
func reachedByJSON(field reflect.StructField) bool {
	if field.Tag.Get("json") == "-" {
		return false
	}
	if field.IsExported() {
		return true
	}
	// An unexported EMBEDDED struct is not the encoder's to drop, and this is
	// the exception that makes the ordering here matter: json reaches through it
	// either way — its exported fields are promoted into the enclosing object,
	// or it is written as a nested one when a tag names it. Both reach the
	// column. An embedded unexported NON-struct has nothing to promote and is
	// dropped like any other unexported field.
	return field.Anonymous && embeddedStruct(field.Type) != nil
}

// jsonKey is the key the encoder writes a field under, and whether a tag chose
// it. An absent, empty or malformed tag name leaves the Go name, untagged —
// which matters beyond the spelling, because a tag is also what wins a tie
// against a sibling at the same depth.
func jsonKey(field reflect.StructField) (string, bool) {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	if !validJSONKey(name) {
		return field.Name, false
	}
	return name, true
}

// validJSONKey mirrors the encoder's own reading of a tag name: letters,
// digits, and a fixed set of punctuation.
func validJSONKey(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if unicode.IsLetter(c) || unicode.IsDigit(c) || strings.ContainsRune(jsonTagPunctuation, c) {
			continue
		}
		return false
	}
	return true
}

// promotedStruct is the struct whose own fields an anonymous field contributes
// to the enclosing object, and whether the field is such an embedding at all. A
// tag that names the field is what stops the promotion: JSON then writes a
// nested object under that name, and the embedding is one field like any other.
//
// The pointer is followed only when the field's type is UNNAMED — which is what
// `*T` is, and what the encoder itself tests — so a defined type whose
// underlying type is a pointer stays one field.
func promotedStruct(field reflect.StructField, tagged bool) (reflect.Type, bool) {
	if tagged || !field.Anonymous {
		return nil, false
	}
	t := field.Type
	if t.Name() == "" && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, false
	}
	return t, true
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
