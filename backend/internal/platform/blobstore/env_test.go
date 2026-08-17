// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package blobstore_test

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/config"
)

func TestFromEnvUnconfiguredIsNotAnError(t *testing.T) {
	// An empty lookup rather than t.Setenv: the point of taking the
	// environment as a parameter is that a test need not mutate process state
	// to steer a package, and a test that does leaks into every one after it.
	store, configured, err := blobstore.FromEnv(t.Context(), config.Static(nil))
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
