// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package customfields holds the custom-field configuration suites: the catalog
// and the ALTER it performs on a record table, the field catalog the resolver
// reads, a field's lifecycle from create through retire, and the HTTP surface over
// all of it including the values and vocabulary endpoints.
//
// It is a suite package split out of internal/compose/integration so the lane has
// another scheduling slot: one package is one slot, and the parent is large enough
// to be the lane's long pole by itself. Its suites ride the parent's exported
// fixtures.
//
// The production module is imported as customfieldsmod, because inside a package
// named customfields a bare customfields.X reads as a self-reference.
//
// # Where the boundary fell, which is not where the names suggest
//
// The custom-field suites divide by FIXTURE, not by topic, and the two halves
// barely touch:
//
//   - Here: the catalog and the HTTP surface, which share createCustomField, the
//     RFC 7807 problem shape, and the schema-wired environment.
//   - In the parent: the value-preservation and vocabulary suites, which share
//     setupCFV, assertCF and the cfvFixture type.
//
// So customfields_values_http lives HERE while customfields_values does not —
// the HTTP one creates fields through the endpoint and the store one seeds them
// through the fixture. Reading the file names alone, that looks backwards.
//
// privacy_customfields also stayed, and deliberately: it is a GDPR suite (Art. 17
// erasure and Art. 15 SAR must reach cf_ columns like core columns), it is built
// on the parent's erasure fixtures, and its subject is privacy rather than field
// configuration. Moving it would have meant dragging the erasure subject seeder
// across the boundary for one caller.
package customfields
