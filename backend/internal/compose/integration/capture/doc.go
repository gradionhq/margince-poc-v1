// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package capture holds the inbound-capture integration suites: the normalized
// sink and its idempotency on the source natural key, the Gmail and Calendar
// connectors that feed it, the keyvault the connectors read credentials from,
// backfill, and the tier and scope gates around all of it.
//
// It is a suite package split out of internal/compose/integration so the lane
// has another scheduling slot: one package is one slot, and the parent is large
// enough to be the lane's long pole by itself. It rides the parent's exported
// fixtures (SearchEnv mostly, Env in one suite) and its shared seed, job-wait and
// HTTP-stub helpers; it owns its own connector stubs, seeding and assertions.
//
// One capture suite is not here: capturedbykind_http_integration_test.go rides
// the booted-app fixture and lives in the parent package.
//
// The production capture module is imported as capturemod, because inside a
// package named capture a bare capture.X reads as a self-reference.
package capture
