// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package crmhello is the walking-skeleton reference extension: the
// smallest unit that exercises the whole stable-tier path (ADR-0069) —
// scanned from extensions/, composed by gen-composition, reconciled into
// the core registries at boot, enumerated in the boot inventory. The CI
// extension lane copies it under extensions/; the vanilla tree never
// compiles it. Its module path is deliberately non-fetchable: an enabled
// extension resolves through the composed workspace, never a proxy.
package crmhello

import (
	"github.com/gradionhq/margince/backend/pkg/extension"
	"github.com/gradionhq/margince/backend/pkg/extension/jurisdiction"
)

// New returns the unit's declaration (the ADR-0069 §4 constructor
// contract the generated composition calls).
func New() extension.Extension {
	return extension.Extension{
		Name:          "crm-hello",
		Version:       "0.1.0",
		Jurisdictions: []jurisdiction.Pack{pack{}},
	}
}

// pack registers under "zz" — an ISO 3166-1 user-assigned code, so the
// fixture can never collide with a real jurisdiction pack.
type pack struct{}

func (pack) Code() string { return "zz" }

func (pack) Retention() jurisdiction.Retention { return nil }
