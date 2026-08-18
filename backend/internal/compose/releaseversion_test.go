// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"
)

// TestRefuseMixedReleaseOnlyRefusesAKnownDifference pins the whole decision the
// guard makes. Two things have to be true at once and they pull in opposite
// directions: a real mixed set must never start, and an installation whose
// release simply is not known must never be taken down by a guard that mistook
// absence for disagreement.
func TestRefuseMixedReleaseOnlyRefusesAKnownDifference(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mine         string
		installation string
		refuse       bool
	}{
		{"a matched set starts", "1970.42", "1970.42", false},
		{"a torn set does not", "1970.41", "1970.42", true},
		{"a torn set does not, whichever side is newer", "1970.43", "1970.42", true},
		{"an unstamped role never refuses", "dev", "1970.42", false},
		{"nor does a role built by a bare go build", "", "1970.42", false},
		{"an installation with no recorded release is not a mismatch", "1970.42", "", false},
		{"nor is one recorded by an unstamped api", "1970.42", "dev", false},
		{"two unknowns are not a mismatch either", "dev", "dev", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseMixedRelease(tc.mine, tc.installation)
			if tc.refuse && err == nil {
				t.Fatalf("release %q against installation %q started; it is a mixed set and must refuse", tc.mine, tc.installation)
			}
			if !tc.refuse && err != nil {
				t.Fatalf("release %q against installation %q refused to start: %v", tc.mine, tc.installation, err)
			}
		})
	}
}

// TestMixedReleaseRefusalNamesBothReleasesAndTheFix: the refusal is read off a
// crash-looping container's log by somebody who has to decide what to re-pull,
// so it owes them both versions and the action. A message that only said
// "release mismatch" would leave them reading the source to find out which
// image is wrong.
func TestMixedReleaseRefusalNamesBothReleasesAndTheFix(t *testing.T) {
	err := refuseMixedRelease("1970.41", "1970.42")
	if err == nil {
		t.Fatal("a mixed set started")
	}
	msg := err.Error()
	for _, want := range []string{"1970.41", "1970.42", "re-pull", "api, web, worker"} {
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(want)) {
			t.Errorf("the refusal does not mention %q: %s", want, msg)
		}
	}
}
