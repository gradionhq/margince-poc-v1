// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import "testing"

func TestReadinessChecksAlwaysIncludeTheRuntimeRole(t *testing.T) {
	// Unconditional, unlike the vault/blobstore/schema-pool probes: every
	// role that serves this router holds a runtime pool, so a deployment can
	// never opt out of the probe by omitting an option.
	srv := &Server{}

	checks := srv.readinessChecks(okPing, okPing)

	if !hasCheck(checks, "runtime-role") {
		t.Fatal("readiness checks omit the runtime-role probe")
	}
}
