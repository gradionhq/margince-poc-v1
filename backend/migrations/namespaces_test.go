// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migrations_test

// Loading the embedded migration namespaces, once, for every test in this
// package that reads or replays them.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/migrations"
)

func namespaces(t *testing.T) (core, custom dbmigrate.Namespace) {
	t.Helper()
	core, err := migrations.Core()
	if err != nil {
		t.Fatalf("loading the core namespace: %v", err)
	}
	custom, err = migrations.Custom()
	if err != nil {
		t.Fatalf("loading the custom namespace: %v", err)
	}
	return core, custom
}
