// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package policy

// The RBAC object vocabulary, made composable.
//
// coreObjects (policy.go) is CLOSED and stays closed: it is the set the
// contract's RbacObject enum mirrors, the seeded role documents grant, and the
// merge-blocking parity test holds equal. An extension cannot join it —
// coreObjects is compiled in, and an installation's units are not.
//
// What an extension CAN do is register its own object into the same vocabulary
// at boot, before anything serves. That is what makes an extension screen
// possible at all: without it, a role document granting `ext_<unit>_<object>`
// is refused by Parse, so the user's whole identity resolution fails — and with
// Parse refusing, nothing could ever be granted, so the object could never
// reach the /me snapshot the web client gates its affordances on. A screen
// gating on an object the client never learns the user holds renders nothing,
// and looks like a frontend bug rather than a missing vocabulary entry.
//
// Registration is boot-only, by contract with its one caller
// (compose.RegisterRbacObjects, called from RegisterExtensions before any
// surface serves). The lock guards the read/write ORDERING against the request
// path, not concurrent registrations — the same posture compose's composedTools
// takes.

import (
	"fmt"
	"regexp"
	"slices"
	"sync"
)

// Object is an RBAC object name an extension contributes to the vocabulary.
// It is a distinct type from the plain strings coreObjects holds so that a
// caller cannot pass an arbitrary string into the vocabulary without meeting
// Validate first.
type Object string

// extensionObjectGrammar: `ext_<unit>_<object>`, with the unit and the object
// as lower-case [a-z0-9] runs joined by single underscores. It is deliberately
// NOT the same grammar as a core object's (which has no prefix): the whole
// point of the namespace token is that a core object can never be mistaken for
// an extension's, in either direction, at a glance or by a parser.
var extensionObjectGrammar = regexp.MustCompile(`^ext_[a-z0-9]+(_[a-z0-9]+)+$`)

// Validate enforces the namespace and the grammar. An object outside it is
// refused rather than accepted-and-ignored, because the vocabulary is what
// Parse admits a stored document by: an unnamespaced entry would let a unit
// widen what a core role document may grant, from a directory an operator
// dropped in.
func (o Object) Validate() error {
	if !extensionObjectGrammar.MatchString(string(o)) {
		return fmt.Errorf("RBAC object %q is not a valid extension object (ext_<unit>_<object>, lower-case [a-z0-9] joined by single underscores)", string(o))
	}
	// The core set is closed, and this is the one place a name could be added
	// that collides with it. A shadowing entry would not widen anything today
	// (the grammar's ext_ prefix already precludes it), but the check is what
	// keeps that a FACT rather than a consequence of two regexes agreeing.
	if slices.Contains(coreObjects, string(o)) {
		return fmt.Errorf("RBAC object %q is already a core object — an extension may not redefine one", string(o))
	}
	return nil
}

// registered holds the extension objects this process registered.
var registered struct {
	mu      sync.RWMutex
	objects map[Object]bool
}

// Register adds objects to the vocabulary. Validate-then-apply, in one pass
// each: a set with one bad name registers NONE of them, so a boot that reports
// failure has not half-widened what a role document may grant.
//
// Re-registering the same object is an error, not a no-op. Two units both
// claiming one object name would be a wiring defect where each thinks it owns
// the grants — and the ext_<unit>_ prefix means it can only happen if one unit
// declares the same name twice, which is a mistake worth naming.
func Register(objects ...Object) error {
	for _, o := range objects {
		if err := o.Validate(); err != nil {
			return err
		}
	}
	registered.mu.Lock()
	defer registered.mu.Unlock()
	for _, o := range objects {
		if registered.objects[o] {
			return fmt.Errorf("RBAC object %q is already registered", string(o))
		}
	}
	if registered.objects == nil {
		registered.objects = make(map[Object]bool, len(objects))
	}
	for _, o := range objects {
		registered.objects[o] = true
	}
	return nil
}

// ResetRegisteredForTest clears the registered set. Exported for the identity
// package's own tests, which register an object and must not leak it into the
// next test's vocabulary; the boot never calls it.
func ResetRegisteredForTest() {
	registered.mu.Lock()
	defer registered.mu.Unlock()
	registered.objects = nil
}

// IsRegisteredObject reports whether an object was registered by an extension.
func IsRegisteredObject(object string) bool {
	registered.mu.RLock()
	defer registered.mu.RUnlock()
	return registered.objects[Object(object)]
}

// RegisteredObjects returns the registered set, sorted. It is what the merge
// seeds the /me snapshot from, and sorted so a snapshot does not depend on map
// iteration order.
func RegisteredObjects() []string {
	registered.mu.RLock()
	defer registered.mu.RUnlock()
	out := make([]string, 0, len(registered.objects))
	for o := range registered.objects {
		out = append(out, string(o))
	}
	slices.Sort(out)
	return out
}

// IsGrantableObject reports whether an object name is one a role document may
// grant: a core object, or one an extension registered at boot. This is the
// question Parse asks, and the question an authority gate asks before trusting
// a requirement is satisfiable.
//
// IsCoreObject stays what its name says — the closed compiled-in set — because
// the parity test against the contract's enum reads exactly that, and widening
// it would make the gate pass for an object no client can express.
func IsGrantableObject(object string) bool {
	return IsCoreObject(object) || IsRegisteredObject(object)
}
