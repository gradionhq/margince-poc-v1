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
}

// groups is the config-driven list of payload families. Today: the public
// webhook/event payload contract compiled from api/public-events.yaml.
var groups = []group{
	{
		Source:  "api/public-events.yaml",
		Out:     "internal/contracts/publicevents_gen.go",
		Package: "crmcontracts",
	},
}
