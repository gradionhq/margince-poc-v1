// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The composed-extension inventory (GET /v1/extensions): what this binary
// actually composed, per unit.
//
// It exists for the role editor. An extension's objects reach a role document as
// `ext_<unit>_<object>` and nothing else in the grant map says which product
// surface a name belongs to — an operator reading a flat list of thirty core
// objects and two ext_ ones has no way to know that granting one of them is what
// turns on the notes screen. This answers that grouping question, and answers it
// from the same values the boot reconciliation validated.
//
// It lives in compose rather than in a module because the composed set is not a
// module's fact: it is the composition root's, held in this package's own
// accessors (ComposedExtensions, ComposedVerbs, servedExtensionJobs). A module
// asked to serve this would need those handed to it, which is a dependency in
// the wrong direction for a value that is fixed at build time.

import (
	"net/http"
	"slices"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// extensionsHandlers serves the inventory. It holds NO state: every value it
// reports is process-level and already recorded by the boot, so a field here
// would be a second copy that could go stale against the accessors.
type extensionsHandlers struct{}

// ListExtensions (GET /v1/extensions). Admin-only.
//
// The admin check is auth.RequireAdmin, the same gate every other
// installation-wide admin endpoint takes, and it is taken HERE rather than in a
// service because there is no service: nothing is read from the database, so
// there is no store to own the gate. Admin is the right bar even though the
// answer is identical for every caller and changes only on deploy — it enumerates the installation's internal
// surface (routes, jobs, unit versions), which is operator information, and the
// one caller that needs it is the admin role editor.
//
// Agents never reach it at all: the operation is `x-agent-access: human-only`,
// enforced by the generated policy table before this runs.
func (extensionsHandlers) ListExtensions(w http.ResponseWriter, r *http.Request) {
	if err := auth.RequireAdmin(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	units := composedExtensionInventory(ComposedExtensions(), ComposedVerbs(), servedExtensionJobs())
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ExtensionDirectory{Extensions: units})
}

// composedExtensionInventory joins the three composed facts into one entry per
// unit. Split from the handler so the join is testable without a request, and
// so the handler stays the transport decision it should be.
//
// Every list is sorted and non-nil. Sorted because map iteration and slice order
// are not a UI's business and a re-render must not reshuffle; non-nil because
// `[]` on the wire says "this unit contributes none", while `null` says nothing
// at all — and "contributes no RBAC objects" is the common, correct answer for a
// unit that owns no records.
func composedExtensionInventory(exts []extension.Extension, verbs []extension.Verb, jobs []composedJob) []crmcontracts.ComposedExtension {
	out := make([]crmcontracts.ComposedExtension, 0, len(exts))
	for _, e := range exts {
		out = append(out, crmcontracts.ComposedExtension{
			Name:        string(e.Name),
			Version:     string(e.Version),
			RbacObjects: unitRbacObjects(e.Name, verbs),
			Routes:      unitRoutes(e.Name, verbs),
			Jobs:        unitJobs(e.Name, jobs),
		})
	}
	slices.SortFunc(out, func(a, b crmcontracts.ComposedExtension) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// unitRbacObjects collects the distinct objects this unit's operations gate on.
//
// Read off the VERBS rather than off the RBAC registry, deliberately. The
// registry (policy.RegisteredObjects) knows every registered name but not which
// unit declared it — the `ext_<unit>_` prefix looks like it would answer that
// and does not, because the namespace is not injective: a hyphenated unit name
// underscores, so `crm-demo`/`widget` and `crm`/`demo_widget` derive one name
// (see extrbac.go). Splitting the prefix back apart would attribute an object to
// the wrong unit; the verb carries its declaring unit as data and cannot.
func unitRbacObjects(unit extension.Name, verbs []extension.Verb) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range verbs {
		if v.Unit != unit || v.RbacObject == "" || seen[v.RbacObject] {
			continue
		}
		seen[v.RbacObject] = true
		out = append(out, v.RbacObject)
	}
	slices.Sort(out)
	return out
}

// unitRoutes collects the unit's published operations as method+path pairs, one
// per operation rather than one per path. A bare path would under-report the
// surface: a path serving GET and DELETE is two capabilities to an operator
// auditing what a unit can do, and collapsing them hides the destructive one
// behind the harmless one.
//
// The CONTRACT spelling (`/ext/<unit>/…`), not the mounted path, because that is
// the spelling a reader can find in the published document — see Verb.Route.
//
// Sorted by path then method, so the pairs for one path stay adjacent — a list
// sorted by method first would scatter one endpoint's verbs across the whole
// unit.
func unitRoutes(unit extension.Name, verbs []extension.Verb) []crmcontracts.ComposedExtensionRoute {
	out := []crmcontracts.ComposedExtensionRoute{}
	for _, v := range verbs {
		if v.Unit != unit {
			continue
		}
		out = append(out, crmcontracts.ComposedExtensionRoute{Method: v.Method, Path: v.Route})
	}
	slices.SortFunc(out, func(a, b crmcontracts.ComposedExtensionRoute) int {
		if byPath := strings.Compare(a.Path, b.Path); byPath != 0 {
			return byPath
		}
		return strings.Compare(a.Method, b.Method)
	})
	return out
}

// unitJobs collects the jobs this boot RUNS for the unit — the served set, not
// the declared one. A job declared in the unit's jobs.yaml with no Go handler
// ticks nothing, and an operator reading this list is asking what runs; naming
// an inert kind here would promise a cadence nobody works.
func unitJobs(unit extension.Name, jobs []composedJob) []string {
	out := []string{}
	for _, j := range jobs {
		if j.decl.Unit == unit {
			out = append(out, j.decl.Job)
		}
	}
	slices.Sort(out)
	return out
}
