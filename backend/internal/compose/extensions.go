// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"fmt"
	"slices"
	"sync"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
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
	setComposedTools(tools)
	setComposedVerbs(verbs)
	return nil
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
	packCodes := make(map[jurisdiction.Code]extension.Name, len(exts))
	for _, e := range exts {
		if err := e.Name.Validate(); err != nil {
			return fmt.Errorf("compose: %w", err)
		}
		if seen[e.Name] {
			return fmt.Errorf("compose: extension %q composed twice — the enabled set under extensions/ carries one directory per unit", e.Name)
		}
		seen[e.Name] = true
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
