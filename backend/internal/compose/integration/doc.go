// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package integration holds the cross-module integration suites — the
// compose charter exercised end to end over a real migrated Postgres —
// and the shared fixtures they ride. Three of those carry a database or a booted
// app, and every suite starts from one of them — all exported, all in non-test
// files:
//
//   - Env, here — a migrated database plus the core stores.
//   - SearchEnv, in searchenv.go — lighter, and despite the name mostly taken
//     for the database rather than the search store.
//   - apptest.AppEnv, in the apptest subpackage — the booted application behind
//     a TLS test server.
//
// Env and SearchEnv live here because the white-box suites that must stay in
// their own package (compose root, briefs) import them, so neither may import
// compose. AppEnv boots a compose handler stack and therefore CANNOT live here:
// that would close a cycle through those same white-box tests. It sits one level
// down instead, and nothing in apptest may import this package, or the cycle
// closes from the other side.
//
// That rule is what decides where a promoted helper goes, and it is the first
// thing a split runs into. A helper this package can hold becomes exported here
// (SeedExtraWorkspace in seed.go, RetentionPassCtx in scope.go); one that needs
// compose cannot, and joins a fixture subpackage instead — jobtest holds the
// River job-runner ceremony for exactly that reason. The compiler reports the
// violation as "import cycle not allowed in test", which names neither this file
// nor the helper you just moved.
//
// Suites also live in sibling packages that import this one, because a package is
// the unit the lane schedules and this one is big enough to be its long pole.
// How much a split is worth is the runner's question, not this file's — read
// scripts/test-integration-parallel.sh, which says what it does and does not
// parallelize. Which subdirectories those are is likewise answered by reading
// them rather than by a list here that each split would have to remember to
// extend: one declaring Test functions is a suite, one declaring none is a
// fixture package, which exists because it must import compose — the rule above.
//
// Split a group out when it is a closed seam: it neither needs nor owes an
// unexported helper across the boundary. It may ride any of the exported
// fixtures, and more than one — a group is not stuck because it mixes them.
// A helper that two such groups need is promoted here; one that only a group
// needs stays with it.
//
// The unexported helpers are the visible half of that seam. The invisible half is
// process-global state, because one package is one binary: anything an init()
// arms — a jurisdiction pack, a registry — arms it for THIS binary only, and a
// suite that moves out leaves it behind without a compile error. That is worse
// than a missing helper, since the suite still runs; it just proves less. Check
// what the group's package registers before moving it, and take the registration
// along.
//
// Two things a split reliably runs into. A fixture is only importable if its
// METHODS are in a non-test file too, since a method declared in a _test.go file
// is not part of the package a sibling imports. And once the fixture's type is
// foreign, a suite cannot declare methods on it at all — helpers a group keeps
// become plain functions taking the fixture.
package integration
