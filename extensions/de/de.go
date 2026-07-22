// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package de is the German jurisdiction pack (ADR-0042) as a stable-tier
// extension — the ADR-0069 migration pilot: the first first-party unit
// shipping enabled-by-default in the vanilla tree (its directory under
// extensions/ IS the enablement). V1 ships the GoBD statutory retention
// classes; further obligations (the XRechnung/ZUGFeRD fiscal formats,
// the CRA conformity regime) return to the seam when their work packages
// land. Core code never contains a jurisdiction string — this unit is
// where Germany lives.
package de

import (
	"github.com/gradionhq/margince/backend/pkg/extension"
	"github.com/gradionhq/margince/backend/pkg/extension/jurisdiction"
)

// New returns the unit's declaration (the ADR-0069 §4 constructor
// contract the generated composition calls).
func New() extension.Extension {
	return extension.Extension{
		Name:          "de",
		Version:       "1.0.0",
		Jurisdictions: []jurisdiction.Pack{pack{}},
	}
}

type pack struct{}

func (pack) Code() jurisdiction.Code { return "de" }

// Retention: the GoBD/AO statutory floors for the record classes the
// product can hold (§147 AO as amended 2025: booking records 8 years;
// commercial correspondence 6). The core engine treats these as
// FLOORS — a workspace policy may keep longer, never destroy earlier.
// The spans are calendar years (ISO 8601 periods), never day counts.
// Bücher/Abschlüsse (10 yr) are deliberately absent: a CRM holds no
// books or annual accounts, and a floor no record can carry would be
// documentation posing as enforcement (reconciliation:
// margince-foundation#1160).
func (pack) Retention() jurisdiction.Retention { return retention{} }

type retention struct{}

func (retention) Classes() []jurisdiction.RetentionClass {
	return []jurisdiction.RetentionClass{
		{Name: jurisdiction.CommercialCorrespondence, Keep: jurisdiction.Period{Years: 6}},
		{Name: jurisdiction.AccountingRecords, Keep: jurisdiction.Period{Years: 8}},
	}
}
