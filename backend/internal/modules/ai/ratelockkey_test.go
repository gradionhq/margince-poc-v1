// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import "testing"

// The advisory-lock key must be injective: two distinct (provider, model_id)
// identities that a naive "provider/model_id" join would collide onto one key
// ("a/b","c") vs ("a","b/c") must map to different lock strings, or unrelated
// rows would serialize against each other.
func TestModelRateLockKeyIsInjective(t *testing.T) {
	pairs := [][2]string{{"a/b", "c"}, {"a", "b/c"}, {"a", "bc"}, {"ab", "c"}}
	seen := make(map[string][2]string, len(pairs))
	for _, p := range pairs {
		key := modelRateLockKey(p[0], p[1])
		if prev, ok := seen[key]; ok {
			t.Fatalf("collision: %v and %v both map to %q", prev, p, key)
		}
		seen[key] = p
	}
}
