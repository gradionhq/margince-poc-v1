// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The RBAC vocabulary seam. A unit that owns records needs an object name a
// role document can grant and the /me snapshot can report; this is where the
// composed set's object names join the vocabulary the identity module enforces.
//
// Why the seam exists rather than the objects simply being in policy.coreObjects:
// coreObjects is compiled in, mirrored by the contract's RbacObject enum, and
// held equal to it by a merge-blocking parity test. An installation's units are
// none of those things. So the vocabulary gains a registration path, and the
// registration happens at boot alongside every other extension reconciliation —
// before any surface serves, and refusing the whole set on one bad name.

import (
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// Where the object names come from: an extension operation declares one with
// `x-rbac-object` in extensions/<unit>/api/<contract>.yaml. It is read out of
// the MERGED contract and re-emitted into the composition as a literal, like
// every other piece of an extension's governance after this slice — never a Go
// field, because a Go field would be a second place the same fact could be
// stated and the two could disagree.
//
// Note for a reader wondering why the object is not ALSO added to the
// contract's RbacObject enum: it cannot be. $.components.schemas.RbacObject is
// a CORE node, and the fragment composer's ownership rule lets a unit extend
// only a node it created itself, so a fragment adding an enum member is
// refused. The runtime side is unaffected (the /me snapshot's object map is
// keyed by plain string), but the generated TypeScript union will not carry it
// until the composed-types lane emits client types from the merged contract.

// RegisterRbacObjects extends the RBAC object vocabulary with the extension
// objects the composed set declares, so a role document may grant them, an
// authority requirement naming one is satisfiable, and /me reports the holder's
// grant on them.
//
// The plan sketched this as taking []policy.Object. It cannot: policy is
// identity's internal package and unreachable from here. identity.RbacObject is
// an alias for exactly that type, so the grammar a name must clear is still
// policy's own — see identity/rbacobjects.go.
func RegisterRbacObjects(objects []identity.RbacObject) error {
	if len(objects) == 0 {
		return nil
	}
	if err := identity.RegisterRbacObjects(objects...); err != nil {
		return fmt.Errorf("compose: %w", err)
	}
	return nil
}

// THE NAMESPACE IS NOT INJECTIVE, and this is where that is handled.
//
// A unit name may hold hyphens (it is a URL path segment); a SQL identifier may
// not, so the derived namespace underscores them — `ext_` + `crm-demo` → `ext_
// crm_demo_`. That map is not one-to-one: unit `crm` declaring object
// `demo_widget` and unit `crm-demo` declaring object `widget` derive the same
// `ext_crm_demo_widget`, and both clear Verb.Validate, because each name really
// is inside its own declaring unit's namespace.
//
// The vocabulary would then refuse the second registration with "already
// registered" and name neither unit — an operator would read it as one unit
// declaring the same object twice and go looking in the wrong file. So the
// collision is detected HERE, where both unit names are in hand, and the error
// names both. Not reachable with a single unit in the tree; reachable as soon as
// a second one ships.
//
// A stronger fix — a per-unit ownership map in the vocabulary, so two units
// could hold distinct objects that happen to derive one name — is deliberately
// NOT taken: the derived name is what a stored role document and the /me
// snapshot carry, so two objects sharing one derived name would be one grant
// wherever it matters. Refusing the ambiguity is the honest resolution; the
// error tells both units to pick different names.

// extensionRbacObjects collects the distinct RBAC objects the composed verb set
// declares, in the verbs' own (already deterministic) order.
//
// crm-demo, the reference extension, declares ext_crm_demo_note on its three
// record operations; de, yogi and crm-hello own no records and declare none, so
// a composed set without crm-demo returns the empty set and this seam does
// nothing — which is the state every other installation is in.
//
// Registration is only half of what an object is for. It is what lets a role
// document grant the object and /me report the holder's grant; the ENFORCEMENT
// is extensionTool.Handle's, which requires the declared object and action of
// the calling principal before the handler runs. Both halves are needed, and
// registration without enforcement was a live defect: the object reached /me,
// a screen hid its controls accordingly, and the same principal could still
// reach the operation through the agent.
func extensionRbacObjects(verbs []extension.Verb) ([]identity.RbacObject, error) {
	var objects []identity.RbacObject
	owner := map[string]extension.Name{}
	for _, v := range verbs {
		if v.RbacObject == "" {
			continue
		}
		if prior, claimed := owner[v.RbacObject]; claimed {
			// The same unit declaring one object on several operations is the
			// normal case — a unit's screens share it — and de-duplicates.
			if prior == v.Unit {
				continue
			}
			return nil, fmt.Errorf("compose: extensions %q and %q both derive RBAC object %q — "+
				"the unit namespace underscores hyphens, so two unit names can derive one object name; "+
				"rename one of the objects", prior, v.Unit, v.RbacObject)
		}
		owner[v.RbacObject] = v.Unit
		objects = append(objects, identity.RbacObject(v.RbacObject))
	}
	return objects, nil
}
