// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

func TestReadinessChecksIncludeKeyvaultWhenConfigured(t *testing.T) {
	srv := &Server{vault: keyvault.NewMemory()}

	checks := srv.readinessChecks(okPing)

	if !hasCheck(checks, "keyvault") {
		t.Fatal("readiness checks omit keyvault when a vault is configured")
	}
}

func TestReadinessChecksOmitKeyvaultWhenAbsent(t *testing.T) {
	srv := &Server{}

	checks := srv.readinessChecks(okPing)

	if hasCheck(checks, "keyvault") {
		t.Fatal("readiness checks include keyvault when no vault is configured")
	}
	if !hasCheck(checks, "postgres") {
		t.Fatal("readiness checks must always include postgres")
	}
}
