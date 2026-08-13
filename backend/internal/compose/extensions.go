// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/ports/jurisdiction"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// RegisterExtensions reconciles the composed extension set (the
// generated composition module's Extensions()) into the core registries.
// Every process role calls it exactly once at boot, before any surface
// serves. Declarations are inert values, so validation and application
// are separate phases: an error anywhere — including a
// duplicate-capability panic from a core registry — aborts the boot, and
// no capability applies unless the whole set validated, so a partially
// registered extension never serves. This is also where the manifest
// emission and the approval filtering slot in: both
// operate on the declared set before anything is applied.
func RegisterExtensions(exts []extension.Extension, verbs []extension.Verb, jobDecls []extension.JobDeclaration) error {
	if err := validateExtensionSet(exts); err != nil {
		return err
	}
	if err := validateVerbSet(verbs); err != nil {
		return err
	}
	// Do every fallible step before applying anything, so the whole
	// reconciliation stays validate-then-apply: adapting the handler-bearing
	// tools to the core seam can (in principle — preflightTools already
	// precludes it, but this stays fail-closed) fail, and it must not fail
	// with a jurisdiction pack already half-applied.
	tools, err := buildExtensionTools(exts, verbs)
	if err != nil {
		return err
	}
	rbacObjects, err := extensionRbacObjects(verbs)
	if err != nil {
		return err
	}
	// Still the validate phase: joining a unit's Go job behavior to its
	// contract-declared kinds can refuse the set (an undeclared job, a
	// confirm-first tier, an outbound scope), and it must do so before a
	// jurisdiction pack is applied.
	composedSet, err := buildExtensionJobs(exts, jobDecls)
	if err != nil {
		return err
	}
	for _, e := range exts {
		for _, p := range e.Jurisdictions {
			jurisdiction.Register(p)
		}
	}
	// After the jurisdiction packs, and still in the apply phase: the RBAC
	// vocabulary is validate-then-apply inside RegisterRbacObjects itself (a set
	// with one bad name registers none), so it cannot half-widen what a role
	// document may grant. It is the one apply step that can still return an
	// error, which is why it is LAST — a failure here leaves the packs applied
	// and the boot aborting, and an aborting boot serves nothing either way.
	if err := RegisterRbacObjects(rbacObjects); err != nil {
		return err
	}
	// The composed job kinds join the declaration table before any runner is
	// built, because everything the runner then asks about them — the wall
	// clock Govern hands River, the queue a fan-out child lands on, the
	// attempt cap, the totality check that refuses an undeclared kind — is
	// answered by jobs.SpecFor. Registering the workers first would mean
	// registering them under the zero Spec, which is River's silent minute.
	if err := jobs.RegisterComposed(composedJobSpecs(composedSet)); err != nil {
		return err
	}
	setComposedJobs(composedSet)
	setComposedSubscriptions(buildExtensionSubscriptions(exts))
	setComposedTools(tools)
	setComposedVerbs(verbs)
	setComposedExtensions(exts)
	return nil
}

// composedExtensions holds this boot's declared unit set, written once by
// RegisterExtensions before any surface serves. Same shape and same reason as
// composedVerbs: the mutex guards the write-then-read ORDERING across the
// boot/serve boundary, not concurrent registrations.
//
// It exists because the operator inventory (/v1/extensions, handlers_extensions.go)
// needs the units THEMSELVES — a name and a version — and every other composed
// accessor holds something derived from them. Recording the set here rather than
// having the handler re-derive it is what keeps the answer equal to what the boot
// reconciliation actually validated: a second source could describe a unit that is
// not serving.
var composedExtensions struct {
	mu   sync.RWMutex
	exts []extension.Extension
}

func setComposedExtensions(exts []extension.Extension) {
	composedExtensions.mu.Lock()
	defer composedExtensions.mu.Unlock()
	composedExtensions.exts = exts
}

// ComposedExtensions returns this boot's declared extension units. Exported for
// the same reason ComposedVerbs is: the surface that reads it is assembled after
// RegisterExtensions has run.
func ComposedExtensions() []extension.Extension {
	composedExtensions.mu.RLock()
	defer composedExtensions.mu.RUnlock()
	return slices.Clone(composedExtensions.exts)
}

// composedVerbs holds the contract-declared operation set of this boot, written
// once by RegisterExtensions before any surface serves. The route mounting reads
// it, and so does the parity sweep that holds declaration and registration
// equal. Same shape and same reason as composedTools: the mutex guards the
// read/write ORDERING, not concurrent registrations.
var composedVerbs struct {
	mu    sync.RWMutex
	verbs []extension.Verb
}

func setComposedVerbs(verbs []extension.Verb) {
	composedVerbs.mu.Lock()
	defer composedVerbs.mu.Unlock()
	composedVerbs.verbs = verbs
}

// ComposedVerbs returns this boot's declared extension operations. Exported
// because the composition root mounts their routes from it (routes.go) after the
// Server is assembled, which is later than RegisterExtensions.
func ComposedVerbs() []extension.Verb {
	composedVerbs.mu.RLock()
	defer composedVerbs.mu.RUnlock()
	return slices.Clone(composedVerbs.verbs)
}

// validateExtensionSet preflights every unit and every capability —
// against the declared set AND the live registries — so the apply phase
// cannot fail halfway: a mid-apply abort would leave an earlier unit's
// capabilities registered while the boot reports failure.
func validateExtensionSet(exts []extension.Extension) error {
	seen := make(map[extension.Name]bool, len(exts))
	namespaces := make(map[string]extension.Name, len(exts))
	packCodes := make(map[jurisdiction.Code]extension.Name, len(exts))
	for _, e := range exts {
		if err := e.Name.Validate(); err != nil {
			return fmt.Errorf("compose: %w", err)
		}
		if seen[e.Name] {
			return fmt.Errorf("compose: extension %q composed twice — the enabled set under extensions/ carries one directory per unit", e.Name)
		}
		seen[e.Name] = true
		if err := reserveNamespace(e.Name, namespaces); err != nil {
			return err
		}
		if err := e.Version.Validate(); err != nil {
			return fmt.Errorf("compose: extension %q: %w", e.Name, err)
		}
		if err := preflightJurisdictions(e, packCodes); err != nil {
			return err
		}
		if err := preflightTools(e); err != nil {
			return err
		}
		if err := preflightSecrets(e); err != nil {
			return err
		}
		if err := preflightJobs(e); err != nil {
			return err
		}
		if err := preflightSubscriptions(e); err != nil {
			return err
		}
	}
	return nil
}

// reserveNamespace refuses a composed set in which one unit's SQL namespace is
// a PREFIX of another's.
//
// Unit `foo` owns `ext_foo`, unit `foo-bar` owns `ext_foo_bar`, and every
// identifier derived from a namespace is `<namespace>_<suffix>` — so
// `ext_foo_bar_note` is a legitimate spelling for BOTH: foo's table named
// `bar_note`, and foo-bar's table named `note`. Nothing downstream can tell
// them apart, because nothing downstream holds the split.
//
// The generator already refuses the case where the two units both DECLARE that
// table (checkDerivedIdentifiers, "the collision is at the join"). What it
// cannot see is a unit merely NAMING the ambiguous identifier at run time — a
// ledger row, an event's subject — where the honest answer to "whose table is
// this" does not exist. Refusing the pair at boot is what makes every such
// answer exact, and it costs an installation only a rename.
//
// The names are reported both ways round, because an author reading this has no
// other way to find the other side of the clash.
func reserveNamespace(name extension.Name, taken map[string]extension.Name) error {
	namespace, err := name.Namespace()
	if err != nil {
		return fmt.Errorf("compose: %w", err)
	}
	for other, owner := range taken {
		short, long, shortOwner, longOwner := other, namespace, owner, name
		if len(namespace) < len(other) {
			short, long, shortOwner, longOwner = namespace, other, name, owner
		}
		if strings.HasPrefix(long, short+"_") {
			return fmt.Errorf("compose: extensions %q and %q derive the namespaces %q and %q, and one opens the other — an identifier like %s_… names a table either unit could own, so no ledger row or event about it has one honest owner. Rename one of them",
				shortOwner, longOwner, short, long, long)
		}
	}
	taken[namespace] = name
	return nil
}

// preflightSubscriptions validates one unit's listeners through the same
// published Subscription.Validate a unit's own tests run, and then asks the one
// question the published type cannot: is every declared event type ROUTABLE?
//
// The catalog is not reachable from pkg/extension (it is stdlib-only), so this
// is the first and only place the answer exists — and it has to be an answer at
// BOOT rather than at delivery, because an unroutable type has no stream, which
// means its listener would be created, hold a cursor, and never receive
// anything. A subscription that silently never fires is indistinguishable from
// a product where that fact never happens.
//
// WHAT IT CANNOT CATCH, so nobody reads more into a green boot than it says: an
// EXTENSION type is routable by SHAPE. `ext_notse.note_added` — a typo for a
// sibling unit's event — boots clean, builds a group over the extension stream
// and never fires, exactly the silence this check removes for core types. It is
// not checkable here and would not be checkable anywhere: a unit's verbs are
// chosen where they are published, at the Record call, and nothing declares
// them in advance. A misspelt core type is caught; a misspelt extension type is
// the unit author's to notice.
func preflightSubscriptions(e extension.Extension) error {
	seen := make(map[string]bool, len(e.Subscriptions))
	for _, sub := range e.Subscriptions {
		if err := sub.Validate(); err != nil {
			return fmt.Errorf("compose: extension %q: %w", e.Name, err)
		}
		if seen[sub.Name] {
			return fmt.Errorf("compose: extension %q declares subscription %q twice", e.Name, sub.Name)
		}
		seen[sub.Name] = true
		for _, eventType := range sub.Events {
			if _, err := kevents.StreamFor(eventType); err != nil {
				return fmt.Errorf("compose: extension %q, subscription %q: %w", e.Name, sub.Name, err)
			}
		}
	}
	return nil
}

// composedSubscriptions holds this boot's registered listeners, written once by
// RegisterExtensions before any surface serves. Same shape and same reason as
// composedTools: the mutex guards the write-then-read ORDERING across the
// boot/serve boundary, not concurrent registrations.
var composedSubscriptions struct {
	mu   sync.RWMutex
	subs []ComposedSubscription
}

func setComposedSubscriptions(subs []ComposedSubscription) {
	composedSubscriptions.mu.Lock()
	defer composedSubscriptions.mu.Unlock()
	composedSubscriptions.subs = subs
}

// ComposedSubscriptions returns this boot's registered listeners, each already
// carrying the unit identity its deliveries are attributed to. The worker role
// reads it to start one consumer per listener; every other role composes them
// and starts none, because consuming the bus is the worker's job.
func ComposedSubscriptions() []ComposedSubscription {
	composedSubscriptions.mu.RLock()
	defer composedSubscriptions.mu.RUnlock()
	return slices.Clone(composedSubscriptions.subs)
}

// SetComposedSubscriptionsForTest installs a listener set without a boot.
//
// Exported for the WORKER's own tests: the lane starter reads this registry,
// and the thing worth testing there — that one unresolvable listener does not
// cost the others their lane — needs a set containing one. cmd/worker cannot
// reach an unexported setter, and reconstructing the registry there would be a
// second source of the same fact.
func SetComposedSubscriptionsForTest(subs []ComposedSubscription) {
	setComposedSubscriptions(subs)
}

// buildExtensionSubscriptions flattens the validated set into one list of
// listeners, each stamped with the unit that declared it.
func buildExtensionSubscriptions(exts []extension.Extension) []ComposedSubscription {
	var subs []ComposedSubscription
	for _, e := range exts {
		for _, sub := range e.Subscriptions {
			subs = append(subs, ComposedSubscription{Unit: e.Name, Version: e.Version, Sub: sub})
		}
	}
	return subs
}

// validateVerbSet refuses two operations that would mount the same route.
//
// It is in the VALIDATE phase because of what happens if it is not checked at
// all: each verb is individually well-formed, so registration succeeds, the
// jurisdiction packs and the RBAC vocabulary apply — and then route assembly
// hands http.ServeMux the same "METHOD /path" pattern twice, which panics. The
// boot dies either way; the difference is whether it dies having already
// changed the registries, which is the one property validate-then-apply is
// here to hold.
//
// The pattern is Method + ServedPath, which is exactly what the mux is keyed
// on. Two units cannot reach this state through the generator — the contract
// merge refuses a second overlay on one path — so this is the fail-closed
// boundary for a composed set that arrived some other way.
func validateVerbSet(verbs []extension.Verb) error {
	seen := make(map[string]extension.Verb, len(verbs))
	for _, v := range verbs {
		pattern := v.Method + " " + v.ServedPath()
		if prev, dup := seen[pattern]; dup {
			return fmt.Errorf("compose: %s is declared by both %s/%s and %s/%s — one route is served by one operation, and mounting it twice panics the router",
				pattern, prev.Unit, prev.OperationID, v.Unit, v.OperationID)
		}
		seen[pattern] = v
	}
	return nil
}

// preflightSecrets validates one unit's declared secret keys through the
// same published SecretsRequest.Validate the manifest generator runs, and
// rejects the same (key, scope) declared twice — two entries for one secret
// would show an operator a duplicate to resolve that resolves to one thing.
// The same key in BOTH scopes is legitimate: they are independent namespaces
// (extension.Secrets), so a unit may hold an installation credential and a
// per-member one under one name.
func preflightSecrets(e extension.Extension) error {
	seen := make(map[extension.SecretsRequest]bool, len(e.Secrets))
	for _, req := range e.Secrets {
		if err := req.Validate(); err != nil {
			return fmt.Errorf("compose: extension %q: %w", e.Name, err)
		}
		if seen[req] {
			return fmt.Errorf("compose: extension %q declares secret %q at %s scope twice", e.Name, req.Key, req.Scope)
		}
		seen[req] = true
	}
	return nil
}

// preflightJobs validates one unit's scheduled jobs through the same published
// Job.Validate the manifest generator runs, and rejects a job name declared
// twice within the unit — the same fail-closed boundary preflightTools holds,
// for a declaration that reached the composed set outside the generator path.
func preflightJobs(e extension.Extension) error {
	seen := make(map[string]bool, len(e.Jobs))
	for _, job := range e.Jobs {
		if err := job.Validate(); err != nil {
			return fmt.Errorf("compose: extension %q: %w", e.Name, err)
		}
		if seen[job.Name] {
			return fmt.Errorf("compose: extension %q declares job %q twice", e.Name, job.Name)
		}
		seen[job.Name] = true
	}
	return nil
}

// preflightTools validates one unit's governed tools through the same
// published Tool.Validate the manifest generator runs, and rejects a verb
// declared twice within the unit — so the fail-closed boundary holds at
// boot even for a declaration that reached the composed set outside the
// generator path.
func preflightTools(e extension.Extension) error {
	seen := make(map[string]bool, len(e.Tools))
	for _, tool := range e.Tools {
		if err := tool.Validate(); err != nil {
			return fmt.Errorf("compose: extension %q: %w", e.Name, err)
		}
		if seen[tool.Name] {
			return fmt.Errorf("compose: extension %q declares tool %q twice", e.Name, tool.Name)
		}
		seen[tool.Name] = true
	}
	return nil
}

// preflightJurisdictions checks one unit's declared packs for grammar,
// duplicates within the composed set, collisions with core packs, and
// retention classes outside the closed vocabularies — an unknown class
// (or anchor, or a negative period) would be a statutory floor that
// looks registered while the engine misreads or ignores it.
func preflightJurisdictions(e extension.Extension, packCodes map[jurisdiction.Code]extension.Name) error {
	for _, p := range e.Jurisdictions {
		code := p.Code()
		if err := code.Validate(); err != nil {
			return fmt.Errorf("compose: extension %q: %w", e.Name, err)
		}
		if owner, dup := packCodes[code]; dup {
			return fmt.Errorf("compose: extensions %q and %q both declare jurisdiction %q", owner, e.Name, code)
		}
		if _, taken := jurisdiction.For(code); taken {
			return fmt.Errorf("compose: extension %q declares jurisdiction %q, which a core pack already registers", e.Name, code)
		}
		if err := preflightRetentionClasses(e.Name, code, p.Retention()); err != nil {
			return err
		}
		packCodes[code] = e.Name
	}
	return nil
}

// preflightRetentionClasses validates one pack's declared floors: class
// name, period, and anchor each carry their own published grammar, and a
// class may be declared once — two floors for the same class with
// different Keep/Anchor would leave the engine picking one silently.
func preflightRetentionClasses(unit extension.Name, code jurisdiction.Code, ret jurisdiction.Retention) error {
	if ret == nil {
		return nil
	}
	seen := make(map[jurisdiction.RetentionClassName]bool)
	for _, class := range ret.Classes() {
		if err := class.Name.Validate(); err != nil {
			return fmt.Errorf("compose: extension %q, jurisdiction %q: %w", unit, code, err)
		}
		if seen[class.Name] {
			return fmt.Errorf("compose: extension %q, jurisdiction %q declares retention class %q twice", unit, code, class.Name)
		}
		seen[class.Name] = true
		if err := class.Keep.Validate(); err != nil {
			return fmt.Errorf("compose: extension %q, jurisdiction %q, class %q: %w", unit, code, class.Name, err)
		}
		if err := class.Anchor.Validate(); err != nil {
			return fmt.Errorf("compose: extension %q, jurisdiction %q, class %q: %w", unit, code, class.Name, err)
		}
	}
	return nil
}
