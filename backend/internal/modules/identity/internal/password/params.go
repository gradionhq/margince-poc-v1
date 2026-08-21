// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build !integration

package password

// The Argon2id cost parameters every build EXCEPT the integration test build
// uses: the OWASP 2024 interactive-login baseline.
//
// These are the values the product ships. `go build ./...` — which is what
// produces api, worker, migrate and the desktop bundle — has no build tags, so
// this is the file that compiles into every binary a user ever runs.
//
// TestProductionParametersHoldTheOWASPBaseline pins them. That gate is not
// ceremony: once the values live behind a build tag, nothing else in an
// ordinary `make check` would notice them being lowered.
const (
	timeCost  = 2
	memoryKiB = 19 * 1024
	threads   = 1
)
