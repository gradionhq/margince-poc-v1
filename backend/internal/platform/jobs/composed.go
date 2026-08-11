// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

// The extension tier's half of the declaration table.
//
// specs_gen.go is CLOSED: it is compiled from backend/api/jobs.yaml, it is
// committed, and it is drift-gated, so it must be byte-identical on every
// installation. An extension's kinds cannot live there — a composed
// installation would then have a different generated file from the vanilla one
// and the gate would fail on every build that enabled a unit.
//
// So the composed kinds arrive at RUN time, through this seam, from the
// generated composition module (which re-emits them as literals). Everything
// the running system asks the declaration — SpecFor, Declared, MustBeTotal —
// answers over both tables, so a composed kind is as declared as a core one and
// River's silent one-minute default is no more reachable for one than for the
// other.
//
// The seam is deliberately NARROW in the one direction that matters: it accepts
// nothing outside the ext_ namespace and it refuses to shadow a core kind. A
// registration surface that could redefine `send_email` would be a way to
// change a core job's wall clock from a directory an installation dropped in.

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// composed is this process's extension-kind table. Written once at boot, read
// on every insert and every scrape; the mutex guards that ordering across the
// boot/serve boundary, not concurrent registrations.
var composed struct {
	mu    sync.RWMutex
	specs map[string]Spec
}

// RegisterComposed records the declarations of this installation's composed
// extension job kinds. The composition root calls it once at boot, before any
// runner is built.
//
// Validate-then-apply, like every other reconciliation in this tier: a set with
// one bad entry registers none, so a boot that refuses cannot leave half an
// extension's kinds declared while the other half falls through to River's
// default.
//
// It REPLACES rather than merges. A process has one composed set, settled at
// boot, and a merging seam would let a second call widen the table from
// somewhere that is not the boot.
// The parameter is NOT named `specs`: that is the compiled core table's name in
// this package, and shadowing it here would turn the core-collision check below
// into a check against the argument itself.
func RegisterComposed(declarations []Spec) error {
	table := make(map[string]Spec, len(declarations))
	for _, s := range declarations {
		if !strings.HasPrefix(s.Kind, extension.NamespacePrefix) {
			return fmt.Errorf("jobs: %q is not an extension kind — this seam declares the %s namespace and nothing else", s.Kind, extension.NamespacePrefix)
		}
		if _, core := specs[s.Kind]; core {
			return fmt.Errorf("jobs: %q is already declared by api/jobs.yaml — a composed kind adds a declaration, it never redefines one", s.Kind)
		}
		if _, dup := table[s.Kind]; dup {
			return fmt.Errorf("jobs: %q is declared twice in the composed set", s.Kind)
		}
		// Cloned in, for the reason every hand-out is cloned out: a Spec copies
		// by value but its two slices do not, so storing the caller's Spec
		// leaves the composed table sharing Args and Registration.When with the
		// slice the caller still holds. The table is this process's declaration
		// of what the fleet does, and it must not be editable from the outside
		// after boot settled it.
		table[s.Kind] = s.clone()
	}
	composed.mu.Lock()
	defer composed.mu.Unlock()
	composed.specs = table
	return nil
}

// composedSpecFor answers the composed table alone.
func composedSpecFor(kind string) (Spec, bool) {
	composed.mu.RLock()
	defer composed.mu.RUnlock()
	s, ok := composed.specs[kind]
	return s, ok
}

// composedTable is a snapshot a reader can iterate without holding the lock
// across a yield — Declared hands control back to its caller between kinds, and
// a caller that registered a kind from inside that loop would deadlock.
func composedTable() map[string]Spec {
	composed.mu.RLock()
	defer composed.mu.RUnlock()
	return maps.Clone(composed.specs)
}

// allSpecs is the declaration table every consumer actually reads: the compiled
// core kinds plus this installation's composed extension kinds. Core wins on a
// collision, which cannot happen (RegisterComposed refuses one) and is spelled
// anyway so the merge has a defined answer rather than a map-order one.
func allSpecs() map[string]Spec {
	table := composedTable()
	if table == nil {
		table = make(map[string]Spec, len(specs))
	}
	maps.Copy(table, specs)
	return table
}

// IsExtensionKind reports whether a kind string sits in the extension
// namespace, WITHOUT asking whether this process declares it.
//
// That distinction is the whole point of the function. A vanilla-built process
// scraping a composed database sees ext_ rows it has no declaration for on
// every rolling deploy and every rollback, and a metric that called those
// "a kind nobody declares" would page somebody for a build skew that is
// expected and temporary. See compose/jobdeclared.go.
func IsExtensionKind(kind string) bool {
	return strings.HasPrefix(kind, extension.NamespacePrefix)
}

// ComposedKinds names every extension kind THIS process declares, in kind
// order. The census and the boot report read it to say what an installation
// composed, which is the question a bare kind list cannot answer once the two
// tables are merged behind SpecFor.
func ComposedKinds() []string {
	table := composedTable()
	return slices.Sorted(maps.Keys(table))
}
