// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package blobstore_test

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
)

func TestFromEnvUnconfiguredIsNotAnError(t *testing.T) {
	t.Setenv("MARGINCE_BLOBSTORE_ENDPOINT", "")

	store, configured, err := blobstore.FromEnv(t.Context())
	if err != nil {
		t.Fatalf("FromEnv with no endpoint: %v", err)
	}
	if configured {
		t.Error("FromEnv reported configured with no endpoint set")
	}
	if store != nil {
		t.Error("FromEnv returned a store with no endpoint set")
	}
}
