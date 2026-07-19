// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migrations

import "testing"

func TestEmbeddedMigrationNamespacesLoad(t *testing.T) {
	loaders := map[string]func() error{
		"core": func() error {
			_, err := Core()
			return err
		},
		"custom": func() error {
			_, err := Custom()
			return err
		},
	}
	for name, load := range loaders {
		t.Run(name, func(t *testing.T) {
			if err := load(); err != nil {
				t.Fatalf("embedded %s migrations do not form a loadable sequence: %v", name, err)
			}
		})
	}
}
