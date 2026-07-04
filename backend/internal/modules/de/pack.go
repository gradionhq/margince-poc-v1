// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package de is the German jurisdiction pack (architecture/14,
// ADR-0042): self-contained, registered in init(), composed into an
// edge binary by a blank import — core code never contains a
// jurisdiction string. V1 ships the GoBD statutory retention classes;
// the fiscal (XRechnung/ZUGFeRD) and conformity regimes land with
// their own work packages.
package de

import "github.com/gradionhq/margince/backend/internal/shared/ports/jurisdiction"

func init() {
	jurisdiction.Register(pack{})
}

type pack struct{}

func (pack) Code() string { return "de" }

func (pack) Fiscal() jurisdiction.Fiscal { return nil }

// Retention: the GoBD/AO/HGB statutory classes (§147 AO as amended
// 2025: booking records 8 years; books/annual accounts 10; commercial
// correspondence 6). The core engine treats these as FLOORS — a
// workspace policy may keep longer, never destroy earlier.
func (pack) Retention() jurisdiction.Retention { return retention{} }

func (pack) Conformity() jurisdiction.Conformity { return nil }

type retention struct{}

func (retention) Classes() []jurisdiction.RetentionClass {
	return []jurisdiction.RetentionClass{
		{Name: "commercial_correspondence", Years: 6},
		{Name: "accounting_records", Years: 8},
		{Name: "books_and_annual_accounts", Years: 10},
	}
}
