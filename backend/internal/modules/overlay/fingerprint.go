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
		// The index is part of the digest because Apply projects fields in
		// declaration order, so a reordering can change which writer lands
		// last on a shared target.
		writeField(h, fmt.Sprintf("field.%d.from", i), fmt.Sprint(f.From))
		writeField(h, fmt.Sprintf("field.%d.to", i), f.To)
		writeField(h, fmt.Sprintf("field.%d.kind", i), f.Kind.String())
		writeField(h, fmt.Sprintf("field.%d.transform", i), f.Transform)
		writeField(h, fmt.Sprintf("field.%d.resolve", i), f.Resolve)
		writeField(h, fmt.Sprintf("field.%d.always_emit", i), fmt.Sprint(f.AlwaysEmit))
		if f.Child != nil {
			writeField(h, fmt.Sprintf("field.%d.child.position", i), fmt.Sprint(f.Child.Position))
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
//craft:ignore naked-any the declaration maps hold decoded JSON values; the any is the declared type, not a missed one
func writeSortedMap(h hash.Hash, name string, values map[string]any) {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeField(h, name+"."+k, fmt.Sprint(values[k]))
	}
}
