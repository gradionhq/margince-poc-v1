// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package blobstore_test

import (
	"strings"
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

// A configured blobstore with no region is refused, and refused BEFORE anything
// dials — the refusal is a statement about where a bucket would be created, not
// a connection failure, so it must not depend on a reachable endpoint.
//
// Asserted in both directions on the region alone, with every other field held
// constant. A one-directional test passes just as happily against a New that
// refuses everything.
func TestFromEnvRefusesAConfiguredStoreWithNoRegion(t *testing.T) {
	env := map[string]string{
		// Endpoint is syntactically invalid on purpose, so the second arm below
		// fails while minio.New parses it rather than on a dial. A reachable-looking
		// host would make this test spend half a minute retrying a closed port,
		// and a unit test that waits on the network is a flake with a slow fuse.
		blobstore.EnvEndpoint:  "not a valid host",
		blobstore.EnvAccessKey: "a",
		blobstore.EnvSecretKey: "b",
		blobstore.EnvBucket:    "attachments",
	}

	store, configured, err := blobstore.FromEnv(t.Context(), config.Static(env))
	if err == nil {
		t.Fatal("a configured blobstore with no region was accepted; the bucket holding attachments would be created wherever the client defaulted to")
	}
	if !strings.Contains(err.Error(), blobstore.EnvRegion) {
		t.Errorf("the refusal does not name %s, so an operator cannot tell what to set: %v", blobstore.EnvRegion, err)
	}
	if configured || store != nil {
		t.Errorf("a refused configuration still reported configured=%v store!=nil=%v", configured, store != nil)
	}

	// The same configuration WITH a region gets past this check and fails on the
	// endpoint instead. That is what proves the region was the refusal's subject
	// rather than the configuration being rejected wholesale.
	env[blobstore.EnvRegion] = "eu-central-1"
	_, _, err = blobstore.FromEnv(t.Context(), config.Static(env))
	if err == nil {
		t.Fatal("expected the invalid endpoint to fail once the region was supplied")
	}
	if strings.Contains(err.Error(), blobstore.EnvRegion) {
		t.Errorf("a supplied region is still reported as missing: %v", err)
	}
}
