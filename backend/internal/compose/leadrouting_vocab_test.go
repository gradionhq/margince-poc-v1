// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose_test

import (
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// The routable-lead-field vocabulary lives in two modules that cannot
// import each other: the people engine matches routing rules on it, and
// the automation catalog mirrors it as the editor's params-schema enum. If
// they drift, a config the editor accepts silently never matches (the
// engine's field lookup returns "" for an unknown key), or the editor
// 422s a field the engine now supports. Compose is the only place both
// are visible, so the binding lives here — derive the obligation, don't
// maintain two hand-synced lists.
func TestRoutableLeadFieldVocabularyIsSingleSourced(t *testing.T) {
	if !slices.Equal(people.RoutableLeadFields, automation.RoutableLeadFields) {
		t.Fatalf("routable-lead-field vocabularies drifted:\n  people: %v\n  automation: %v",
			people.RoutableLeadFields, automation.RoutableLeadFields)
	}
}
