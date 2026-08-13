// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The mirror stores a PROJECTION, not the incumbent's raw record, and a row is
// re-projected only when the incumbent's own baseline advances. So a mapping
// change leaves every already-mirrored row holding a payload this code would
// never produce again, indefinitely. The fingerprint is how a row says which
// declaration produced it, so that condition is detectable rather than silent.
//
// It hashes the declaration's DATA, never its source text: a comment or
// formatting edit must not invalidate an estate, and a semantic change must
// not fail to.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"sort"
	"strconv"
)

// Fingerprint digests one object mapping's declaration. Two mappings that
// would project the same raw record identically share a fingerprint; any
// difference that could change a projected payload changes it.
func Fingerprint(m ObjectMapping) string {
	h := sha256.New()
	writeField(h, "source", m.Source)
	writeField(h, "target", m.Target)
	writeField(h, "external_key", m.ExternalKey)
	writeField(h, "baseline", m.Baseline)
	writeField(h, "unmapped_policy", m.UnmappedPolicy)
	writeSortedMap(h, "const", m.Const)
	for i, f := range m.Fields {
		// The index is hashed conservatively, not because a reorder is known to
		// matter: order-independence rests on guards declared elsewhere —
		// checkTargetCollisions rejects a shared target outright, and
		// sortChildRowsByPosition re-sorts one parent's child rows by positions
		// checkChildRowDeclarations keeps unique. Pinning the index costs a
		// re-projection whenever Fields is reordered; leaving it out would bet
		// the estate on those guards never being relaxed, and a missed
		// re-projection is the failure this digest exists to prevent.
		//
		// From is written element by element, prefixed with its count, because a
		// flattened rendering cannot separate ["a","b"] from ["a b"] — one
		// TargetAssembler gathering two raw properties from one gathering a
		// single property whose name contains a space. The count also keeps a nil
		// From and an empty one equal, which they are: neither gathers anything.
		writeField(h, fmt.Sprintf("field.%d.from.count", i), strconv.Itoa(len(f.From)))
		for j, from := range f.From {
			writeField(h, fmt.Sprintf("field.%d.from.%d", i, j), from)
		}
		writeField(h, fmt.Sprintf("field.%d.to", i), f.To)
		writeField(h, fmt.Sprintf("field.%d.kind", i), f.Kind.String())
		writeField(h, fmt.Sprintf("field.%d.transform", i), f.Transform)
		writeField(h, fmt.Sprintf("field.%d.resolve", i), f.Resolve)
		writeField(h, fmt.Sprintf("field.%d.always_emit", i), strconv.FormatBool(f.AlwaysEmit))
		if f.Child != nil {
			writeField(h, fmt.Sprintf("field.%d.child.position", i), strconv.Itoa(f.Child.Position))
			writeSortedMap(h, fmt.Sprintf("field.%d.child.attrs", i), f.Child.Attrs)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeField feeds one named value into the digest length-prefixed, so no
// concatenation of two values can collide with a different pair. The parameter
// is a hash.Hash rather than an io.Writer because that interface documents a
// Write which never returns an error — there is no failure here to report.
func writeField(h hash.Hash, name, value string) {
	field := fmt.Sprintf("%d:%s=%d:%s;", len(name), name, len(value), value)
	h.Write([]byte(field))
}

// writeSortedMap feeds a declaration map in key order, because Go's map
// iteration order varies per run and a fingerprint that varied would mark
// every mirrored row stale forever.
//
// Values carry their dynamic type and their Go-syntax form, because Const and
// Attrs are copied verbatim into the projected payload and a plain rendering is
// type-blind: the bool true and the string "true" both render as "true", and
// so do 1, 1.0 and "1" — a declaration edit that flips one for the other
// changes the mirrored JSON. The Go-syntax form quotes strings and delimits
// container members, so the same separation holds a level down into the nested
// values a decoded-JSON declaration can carry. fmt sorts map keys, so a nested
// map is as stable across runs as the ordering above.
//
//craft:ignore naked-any the declaration maps hold decoded JSON values; the any is the declared type, not a missed one
func writeSortedMap(h hash.Hash, name string, values map[string]any) {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeField(h, name+"."+k, fmt.Sprintf("%T=%#v", values[k], values[k]))
	}
}
