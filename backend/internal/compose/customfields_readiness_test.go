// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import "testing"

func TestReadinessChecksIncludeSchemaPoolWhenConfigured(t *testing.T) {
	srv := &Server{schemaPoolReady: okPing}

	checks := srv.readinessChecks(okPing)

	if !hasCheck(checks, "customfields-schema-pool") {
		t.Fatal("readiness checks omit the schema pool when one is configured")
	}
}

func TestReadinessChecksOmitSchemaPoolWhenAbsent(t *testing.T) {
	srv := &Server{}

	checks := srv.readinessChecks(okPing)

	if hasCheck(checks, "customfields-schema-pool") {
		t.Fatal("readiness checks include the schema pool when none is configured")
	}
	if !hasCheck(checks, "postgres") {
		t.Fatal("readiness checks must always include postgres")
	}
}
