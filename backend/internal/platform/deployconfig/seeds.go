// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

// What an installation is created WITH, as opposed to how it is configured.
//
// Split from deployconfig.go because the two have different lifetimes and that
// is the whole distinction ADR-0061 §2 draws: a seed is consumed exactly once,
// at organization creation, and the database is authoritative afterwards —
// which is why the `bootstrap_admin` and `seeds` sections may be deleted once
// an installation exists, and why nothing else in that file may be.

import (
	"gopkg.in/yaml.v3"
)

// Seeds externalizes the workspace defaults bootstrap previously seeded
// from code. Every key is optional — an omitted key seeds the built-in
// default, so a minimal file behaves exactly like the historical
// bootstrap. Values are consumed once, at organization creation.
type Seeds struct {
	Pipeline           *PipelineSeed    `yaml:"pipeline"`
	ConsentPurposes    []ConsentPurpose `yaml:"consent_purposes"`
	Retention          *RetentionSeed   `yaml:"retention"`
	StarterAutomations *bool            `yaml:"starter_automations"`
	BookingPage        *bool            `yaml:"booking_page"`
	// AIRouting is the tier→model binding a fresh installation is bootstrapped
	// with, so a deployment can declare which vendor it uses in the file it
	// reviews rather than having to call an API after first boot. Consumed
	// exactly once, like every other seed here; the database is authoritative
	// afterwards and an admin re-points a lane through /v1/ai/routing.
	//
	// Carried as a raw node rather than a typed struct because the type it
	// becomes lives in modules/ai, and platform may not import modules. The
	// alternative — mirroring the tier, provider and embeddings shape here —
	// would be a second copy of a structure that already has a validator, free
	// to drift from the one that enforces it. Compose decodes it through the ai
	// module's own parser, so a seed is held to exactly the bar a stored
	// binding is.
	// A VALUE, not a pointer. yaml.v3 special-cases a field only when its type
	// is exactly yaml.Node; a *yaml.Node is dereferenced and then decoded as an
	// ordinary struct, so the document's own keys are matched against
	// yaml.Node's fields and every real binding is refused with "field profile
	// not found in type yaml.Node". Load runs at the top of every boot, so that
	// spelling does not degrade the feature — it stops the process.
	AIRouting yaml.Node `yaml:"ai_routing"`
}
