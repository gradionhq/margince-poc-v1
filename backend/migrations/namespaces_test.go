// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migrations

// Loading the embedded migration namespaces, once, for every test in this
// package that reads or replays them.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

func namespaces(t *testing.T) (core, custom dbmigrate.Namespace) {
	t.Helper()
	core, err := Core()
	if err != nil {
		t.Fatalf("loading the core namespace: %v", err)
	}
	custom, err = Custom()
	if err != nil {
		t.Fatalf("loading the custom namespace: %v", err)
	}
	return core, custom
}
