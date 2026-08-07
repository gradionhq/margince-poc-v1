// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package capture holds the inbound-capture integration suites: the normalized
// sink and its idempotency on the source natural key, the Gmail and Calendar
// connectors that feed it, the keyvault the connectors read credentials from,
// backfill, and the tier and scope gates around all of it.
//
// It is a suite package split out of internal/compose/integration to give the
// lane a second scheduling slot — one package is one slot, and the parent was
// large enough to be the lane's long pole by itself. It rides the exported
// SearchEnv fixture from the parent and owns everything else it needs.
//
// One capture suite did NOT come along: capturedbykind_http_integration_test.go
// drives the booted-app fixture in the parent's e2e_integration_test.go, which is
// declared in a test file and so is not importable. It stays there until that
// fixture is promoted the way SearchEnv was.
//
// The production capture module is imported as capturemod, because inside a
// package named capture a bare capture.X reads as a self-reference.
package capture
