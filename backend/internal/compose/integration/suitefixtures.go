// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/ports/jurisdiction"
)

// Fixtures shared between this package's suites and the suite packages split out
// of it. They live in a NON-test file on purpose: a subpackage can import
// identifiers from this package's ordinary files, and nothing at all from its
// _test.go files, so a helper two suite packages need has to sit here or be
// copied into each — and a copied seeder drifts from the one it was copied from.
//
// The bar for moving a helper here is a caller on BOTH sides of a package
// boundary. A helper only one suite uses belongs in that suite, next to the test
// that reads it.
//
// This file must not import internal/compose/integration/apptest: apptest
// imports compose, and compose's white-box tests import this package, so an
// ordinary file here that reaches apptest closes an import cycle. A fixture that
// takes an *apptest.AppEnv therefore belongs in apptest, not here.

// GoBDFloorPack is the six-calendar-year correspondence floor the retention
// suites test against — a stand-in jurisdiction under a reserved code, not the
// shipped de pack, so a suite asserts the seam rather than one country's law.
type GoBDFloorPack struct{}

// Code is a reserved two-letter code, so this pack can never collide with a
// shipped jurisdiction or be mistaken for one in a failure message.
func (GoBDFloorPack) Code() jurisdiction.Code { return "zq" }

// Retention declares the one class these suites turn on — the commercial
// correspondence floor.
//
//nolint:ireturn // jurisdiction.Pack declares this method returning the interface; a concrete return type would not satisfy it.
func (GoBDFloorPack) Retention() jurisdiction.Retention { return goBDFloorClasses{} }

type goBDFloorClasses struct{}

func (goBDFloorClasses) Classes() []jurisdiction.RetentionClass {
	return []jurisdiction.RetentionClass{
		{Name: jurisdiction.CommercialCorrespondence, Keep: jurisdiction.Period{Years: 6}, Anchor: jurisdiction.AnchorCalendarYearEnd},
	}
}

// RegisterGoBDFloorPack arms the floor the way the composed boot does. The
// registry is process-global and one package is one binary, so every suite
// package whose tests depend on the floor must call this from its own init —
// leaving it behind does not fail to compile, it makes a destructive pass over
// correspondence go green precisely because the floor that shields it is absent.
func RegisterGoBDFloorPack() {
	jurisdiction.Register(GoBDFloorPack{})
}

// SeedRetentionPolicies installs the policy set the retention engine acts on:
// one row per branch the sweep must take, so a suite asserting an outcome does
// not also have to state the policy that produced it.
func SeedRetentionPolicies(t *testing.T, e *Env) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO retention_policy (workspace_id, object_type, category, retain_days, action)
			SELECT NULLIF(current_setting('app.workspace_id', true), '')::uuid, v.o, v.c, v.d, v.a
			FROM (VALUES
			  ('lead', 'unconverted', 365, 'anonymize'),
			  ('activity', NULL, 1095, 'archive'),
			  ('activity', 'transcript', 365, 'erase'),
			  ('person', 'no_consent_no_deal', 730, 'anonymize'),
			  ('deal', 'lost', 1825, 'archive')
			) AS v(o, c, d, a)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
