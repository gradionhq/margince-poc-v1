// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package apptest holds AppEnv, the booted-application fixture: a real compose
// handler stack behind a TLS test server, with a cookie-jar client already
// logged in. Env and SearchEnv next door hand a suite a migrated database; this
// hands it the running application, which is what a suite asserting transport,
// session auth or an RFC 7807 body needs.
//
// It is a package of its own because of an import cycle, not for tidiness.
// Package compose's white-box tests import package integration, so nothing in
// integration may import compose — and this fixture boots a compose handler
// stack. One level down, importing compose is free, and integration's suites
// import this package rather than the other way round.
//
// That direction is load-bearing: nothing here may import
// internal/compose/integration, or the cycle closes from this side instead.
// applyRiverSchema is duplicated from there for exactly that reason, and says so.
//
// The fixture is exported down to its fields because suite packages split out of
// integration ride it from outside. A method declared on it in another package is
// illegal Go, so a helper a suite keeps for itself is a function taking *AppEnv,
// not a method on it.
package apptest
