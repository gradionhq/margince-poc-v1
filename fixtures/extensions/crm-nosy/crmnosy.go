// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package crmnosy is the NAMESPACE-WALL fixture: a throwaway second unit that
// declares the SAME workspace-scoped `signing` key crm-demo declares, and must
// not be able to read crm-demo's.
//
// The declaration is the whole fixture, and that is the point rather than a
// shortcut. A unit addresses a secret by its own bare key name; the live port
// is built by the core, closes over the invoking unit, and carries that name
// into every statement. So "read crm-demo's key" is not an operation this
// surface can express — there is no method taking a unit name, and no
// parameter one could be smuggled through. What a second unit CAN do is
// declare the same key name, which is exactly what this file does: two units
// both using "signing", neither able to see the other's.
//
// The wall is therefore demonstrated where the two namespaces actually meet —
// over a real database, with two Runtimes the core built — in
// backend/internal/compose/extruntime_integration_test.go
// (TestCrmDemoSigningKeyIsUnreachableFromASecondUnit). It cannot be
// demonstrated from inside this package: a fixture's own test would have to
// build its own fake Secrets, which would be a test of the fake.
//
// Nothing composes it. The CI extension lane copies crm-hello only, and this
// unit ships no tools, no jobs and no migrations — a name and a secret request
// is all the wall needs on this side.
package crmnosy

import "github.com/gradionhq/margince/backend/pkg/extension"

// New returns the unit's declaration.
func New() extension.Extension {
	return extension.Extension{
		Name:    "crm-nosy",
		Version: "0.1.0",
		Secrets: []extension.SecretsRequest{
			{Key: "signing", Scope: extension.SecretScopeWorkspace},
		},
	}
}
