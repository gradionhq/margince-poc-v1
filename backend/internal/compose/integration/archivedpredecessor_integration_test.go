// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The boot warning is the ONLY thing that can still say a merge happened.
//
// While the record tables carried a tenant column, a gate counted an archived
// workspace's rows and refused. After ADR-0091 §8 phase D the rows are
// indistinguishable by construction, so there is no residue query left — the
// surviving signal is the archived workspace row itself, and this suite pins
// that it is read and said out loud.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestBootSaysNothingWhenNoOrganizationWasArchived(t *testing.T) {
	env := Setup(t)
	var out bytes.Buffer

	if err := compose.WarnOnArchivedPredecessor(context.Background(), env.Pool,
		slog.New(slog.NewTextHandler(&out, nil))); err != nil {
		t.Fatalf("WarnOnArchivedPredecessor: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("a clean installation was warned about a merge that never happened:\n%s", out.String())
	}
}

func TestBootNamesWhatAnArchivedOrganizationLeftBehind(t *testing.T) {
	env := Setup(t)
	ctx := context.Background()

	if _, err := OwnerConn(t).Exec(ctx,
		`INSERT INTO workspace (id, slug, archived_at) VALUES ($1, $2, now())`,
		ids.NewV7(), "predecessor"); err != nil {
		t.Fatalf("seeding the archived predecessor: %v", err)
	}

	var out bytes.Buffer
	if err := compose.WarnOnArchivedPredecessor(ctx, env.Pool,
		slog.New(slog.NewTextHandler(&out, nil))); err != nil {
		t.Fatalf("WarnOnArchivedPredecessor: %v", err)
	}
	said := out.String()
	if said == "" {
		t.Fatal("an installation carrying an archived organization's rows was told nothing; " +
			"no other query can find them, so this line is the whole notice")
	}
	// What it says matters more than that it said something: an operator who
	// reads "records merged" will not go and look at the roster.
	for _, want := range []string{"credentials", "RBAC grants"} {
		if !strings.Contains(said, want) {
			t.Errorf("the warning does not mention %q, so it reads as a records-only merge:\n%s", want, said)
		}
	}
	if !strings.Contains(said, "WARN") {
		t.Errorf("the notice is not at WARN, so a log pipeline filters it away:\n%s", said)
	}
}
