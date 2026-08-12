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
// The split is by WHAT DRIVES THE WRITE, and the fixtures follow from that:
//
//   - Here: the suites that drive the customfields Service or its HTTP surface.
//     They carry two fixture families — Setup + SchemaPool + integration
//     .CustomFieldAdminPerms for the service suites, and schemaWiredEnv +
//     createCustomField + the RFC 7807 problem shape for the wire ones.
//   - In the parent: the suites that drive a RECORD store — people, deals — whose
//     rows happen to carry cf_ values. They share setupCFV, assertCF and the
//     cfvFixture type, none of which is reachable from here.
//
// So customfields_values_http lives HERE while customfields_values does not: the
// first is a wire suite, the second seeds values through a record store. Reading
// the file names alone, that looks backwards.
//
// privacy_customfields stayed for the same rule rather than as an exception to it:
// it is built on setupCFV, cfvFixture and assertCF, which are the parent's. (It is
// also a GDPR suite — Art. 17 erasure and Art. 15 SAR must reach cf_ columns like
// core columns — and it needs the parent's erasure subject seeder, which lives in
// a _test.go file and so is unreachable from any subpackage.)
package customfields
