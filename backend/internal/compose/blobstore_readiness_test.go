// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
)

func okPing(context.Context) error { return nil }

func hasCheck(checks []httpserver.ReadyCheck, name string) bool {
	for _, c := range checks {
		if c.Name == name {
			return true
		}
	}
	return false
}

func TestReadinessChecksIncludeBlobstoreWhenConfigured(t *testing.T) {
	srv := &Server{blob: blobstore.NewMemory()}

	checks := srv.readinessChecks(okPing)

	if !hasCheck(checks, "blobstore") {
		t.Fatal("readiness checks omit blobstore when a store is configured")
	}
}

func TestReadinessChecksOmitBlobstoreWhenAbsent(t *testing.T) {
	srv := &Server{}

	checks := srv.readinessChecks(okPing)

	if hasCheck(checks, "blobstore") {
		t.Fatal("readiness checks include blobstore when no store is configured")
	}
	if !hasCheck(checks, "postgres") {
		t.Fatal("readiness checks must always include postgres")
	}
}
