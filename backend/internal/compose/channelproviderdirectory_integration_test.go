// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The transport directory holds against the registry it describes, in BOTH
// directions: every registered transport is published, and every published
// entry names a transport that is actually registered. One direction alone is
// half a guard — publishing a superset invents transports a client may then ask
// for, and publishing a subset hides one whose messages are already on the
// timeline with no label to render.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
)

func TestTheDirectoryAndTheRegistryDescribeTheSameTransports(t *testing.T) {
	integration.Setup(t)
	owner := integration.OwnerConn(t)
	ctx := context.Background()

	rows, err := owner.Query(ctx, `SELECT provider FROM channel_provider ORDER BY provider`)
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	inRegistry := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scanning a provider: %v", err)
		}
		inRegistry[p] = true
	}
	rows.Close()
	if len(inRegistry) == 0 {
		t.Fatal("the registry is empty, so this test would pass by having nothing to compare")
	}

	// Served from the boot snapshot, which is what the handler reads — asserting
	// against a fresh query instead would prove the query, not the endpoint.
	published := publishedChannelProviders(ComposedChannelProviders())
	if len(published) == 0 {
		t.Fatal("the directory published nothing; every member's timeline would render raw provider ids")
	}

	inDirectory := map[string]bool{}
	for _, entry := range published {
		inDirectory[entry.Provider] = true
		if !inRegistry[entry.Provider] {
			t.Errorf("the directory publishes %q, which is not a registered transport — a client could ask for a provider nothing can carry", entry.Provider)
		}
		if entry.Label == "" {
			t.Errorf("%q is published with no label; the raw id would reach a human", entry.Provider)
		}
		if !entry.CredentialModel.Valid() {
			t.Errorf("%q publishes credential_model %q, which is outside the contract's enum", entry.Provider, entry.CredentialModel)
		}
	}
	for p := range inRegistry {
		if !inDirectory[p] {
			t.Errorf("%q is a registered transport the directory does not publish — its messages are already on timelines with no label to render", p)
		}
	}
}

// The label the MIGRATION seeds and the label the boot reconcile writes are two
// spellings of one fact, and 0252's own comment promises this test holds them
// together. They can only disagree for providers where title-casing the id is
// wrong — which is exactly why the exception exists, and exactly why it is the
// pair most likely to drift.
func TestTheSeededLabelMatchesTheOneBootWrites(t *testing.T) {
	integration.Setup(t)
	owner := integration.OwnerConn(t)
	ctx := context.Background()

	rows, err := owner.Query(ctx, `SELECT provider, label FROM channel_provider ORDER BY provider`)
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var provider, seeded string
		if err := rows.Scan(&provider, &seeded); err != nil {
			t.Fatalf("scanning a row: %v", err)
		}
		if want := providerLabel(provider); seeded != want {
			t.Errorf("migration seeded %q as %q, boot writes %q — a fresh install and a booted one would show different names for one transport",
				provider, seeded, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating the registry: %v", err)
	}
}
