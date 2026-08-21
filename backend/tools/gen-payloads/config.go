// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// group is one payload family: an isolated, components-only OpenAPI 3.1
// source compiled into a Go models file. The slice below IS the whole
// configuration — adding a family (another projection of domain events, a
// second public-contract surface) costs one entry, not new generator code.
// Nothing here is webhook-specific by design.
type group struct {
	// Source is the OpenAPI 3.1 file (components/schemas only, no paths),
	// relative to the backend module root.
	Source string
	// Out is the generated Go destination, relative to the backend module root.
	Out string
	// Package is the package clause the generated file carries.
	Package string
	// VersionsVar names the generated event-type→version map, or is empty
	// for a family that owes none. Every family compiles into ONE package, so
	// two families naming the same map would not compile — and only the
	// public contract has gates that read one.
	VersionsVar string
}

// groups is the config-driven list of payload families: the public
// webhook/event payload contract, and the internal payloads that ride the same
// bus without being subscribable.
var groups = []group{
	{
		Source:      "api/public-events.yaml",
		Out:         "internal/contracts/publicevents_gen.go",
		Package:     "crmcontracts",
		VersionsVar: "PublicEventVersions",
	},
	{
		// Internal payloads: on the bus, deliberately not in the public
		// webhook contract, so no version map — the coverage and version gates
		// that read one are gates on what a subscription may name.
		Source:  "api/internal-events.yaml",
		Out:     "internal/contracts/internalevents_gen.go",
		Package: "crmcontracts",
	},
}
