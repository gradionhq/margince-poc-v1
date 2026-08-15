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
	e := integration.Setup(t)
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
	//
	// Loaded first, because the snapshot is filled at SERVER ASSEMBLY and this
	// lane constructs no server: an unloaded snapshot would make this test
	// report the very emptiness it exists to refuse.
	if err := LoadChannelProviderDirectory(ctx, e.Pool); err != nil {
		t.Fatalf("loading the directory: %v", err)
	}
	registered, sending := ComposedChannelProviders()
	published := publishedChannelProviders(registered, sending)
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
// spellings of one fact, and the migration that adds the column promises this
// test holds them together. They can only disagree for providers where title-casing the id is
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

	// The loop above passes VACUOUSLY while every registered provider is one
	// word: the two spellings can only differ where the id has a word break, so
	// a registry of `telegram` and `whatsapp` exercises nothing. This asserts
	// the rule itself against the shape that separates them — the SQL seed's
	// initcap(replace(provider,'_',' ')) and the Go providerLabel must agree on
	// an underscore, and bare initcap would render "Deal_Room" here.
	if got := providerLabel("deal_room"); got != "Deal Room" {
		t.Errorf("providerLabel(\"deal_room\") = %q, want \"Deal Room\" — the migration's seed treats `_` as a word break and this must match it", got)
	}
	var seeded string
	if err := owner.QueryRow(ctx, `SELECT initcap(replace($1, '_', ' '))`, "deal_room").Scan(&seeded); err != nil {
		t.Fatalf("asking the database for its own seed rule: %v", err)
	}
	if seeded != providerLabel("deal_room") {
		t.Errorf("the migration would seed %q and boot writes %q for the same provider", seeded, providerLabel("deal_room"))
	}
}

// The directory must answer from the REGISTRY, not from whatever a capture
// registry happened to write at boot.
//
// This is the defect the first version of this slice shipped: the snapshot was
// written only inside NewCaptureRegistry, which the api role builds only when a
// keyvault root key is configured — so a vault-less install answered 200 with an
// empty list, telling every timeline it had no labels and telling an agent the
// provider vocabulary was empty while log_activity still demanded a value from
// it. A silent empty reads as an answer.
//
// It is asserted by clearing the snapshot and loading it again from the table,
// which is exactly what a role that never constructed a registry does.
func TestTheDirectoryLoadsFromTheRegistryAndNotFromTheCaptureBoot(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()

	before, sending := ComposedChannelProviders()
	t.Cleanup(func() { setComposedChannelProviders(before, sending) })

	// The state a role that composed no capture registry starts in.
	setComposedChannelProviders(nil, nil)
	if registered, _ := ComposedChannelProviders(); len(registered) != 0 {
		t.Fatalf("the snapshot did not clear, so this test would pass on stale data")
	}

	if err := LoadChannelProviderDirectory(ctx, e.Pool); err != nil {
		t.Fatalf("loading the directory: %v", err)
	}

	registered, _ := ComposedChannelProviders()
	if len(registered) == 0 {
		t.Fatal("the directory is empty after loading from the registry; a vault-less install would publish no labels at all and report 200 while doing it")
	}
}
