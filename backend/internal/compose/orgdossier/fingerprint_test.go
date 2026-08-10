// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// The cache key, held to what it claims to cover. Every entry here is a thing
// that changes the answer without changing the company's rows — the class of
// change a key derived from the rows alone would miss, and therefore serve
// stale forever.

import (
	"testing"
	"time"
)

func TestTheDossierFingerprintMovesWithItsInputsAndItsLane(t *testing.T) {
	in := fourOfSeven()
	base, err := Fingerprint(in, "routing-1")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}

	// The same input hashes the same way twice, or nothing could ever be a
	// cache HIT and every read would rewrite.
	again, err := Fingerprint(in, "routing-1")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if again != base {
		t.Error("the same input hashed differently twice: no assembly would ever be reused")
	}

	// A value moving must move the key, or the dossier keeps describing a
	// company that has since been re-read.
	moved := fourOfSeven()
	moved.ProfileFields[0].Value = "Something else entirely"
	changed, err := Fingerprint(moved, "routing-1")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if changed == base {
		t.Error("a changed profile value left the key unchanged: the cached dossier " +
			"would keep describing the old value indefinitely")
	}

	repointed, err := Fingerprint(in, "routing-2")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if repointed == base {
		t.Error("re-pointing the model lane left the key unchanged, so prose written by a " +
			"model that no longer writes it would keep being served as that model's")
	}
}

// Confirming our own profile changes every company's band cap without touching
// a single company record. A key blind to it would keep serving capped bands to
// a workspace that has since described itself.
func TestTheGrowthFitFingerprintMovesWhenOurOwnOfferingIsConfirmed(t *testing.T) {
	in := fourOfSeven()
	unconfirmed, err := growthFitFingerprint(in, "routing-1", Offering{Fingerprint: "offer-a"})
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	confirmed, err := growthFitFingerprint(in, "routing-1", Offering{Confirmed: true, Fingerprint: "offer-a"})
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}

	if unconfirmed == confirmed {
		t.Error("confirming the workspace's own offering left the key unchanged; every " +
			"reader would keep seeing a band capped for a reason that no longer holds")
	}

	// And EDITING what we sell must move it too. `confirmed` stays true across
	// that edit, so a key carrying only the boolean would keep serving bands
	// measured against an offering this workspace no longer has.
	edited, err := growthFitFingerprint(in, "routing-1",
		Offering{Confirmed: true, Fingerprint: "offer-b"})
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if edited == confirmed {
		t.Error("changing what this workspace sells left the key unchanged")
	}
}

// The two surfaces cache into different tables but hash the same input. If they
// agreed on a key, a change to one assembly's rules would be invisible to the
// other's freshness check.
func TestTheTwoSurfacesDoNotShareAFingerprint(t *testing.T) {
	in := fourOfSeven()
	dossier, err := Fingerprint(in, "routing-1")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	fit, err := growthFitFingerprint(in, "routing-1", Offering{Confirmed: true, Fingerprint: "offer-a"})
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if dossier == fit {
		t.Error("the dossier and the growth fit hashed identically, so a prompt revision " +
			"to one would silently keep serving the other's cached assembly")
	}
}

// A cached entry past its own expiry is a MISS, not a hit. This is the whole
// reason StaleAt is stored: nothing else notices a value ageing out.
func TestACachedAssessmentPastItsExpiryIsNotServed(t *testing.T) {
	stale := storedGrowthFit{
		Version:     growthFitStoredVersion,
		Fingerprint: "abc",
		StaleAt:     assessedAt,
	}

	if !stale.usable("abc", assessedAt.Add(-time.Hour)) {
		t.Error("an entry inside its window was refused")
	}
	if stale.usable("abc", assessedAt.Add(time.Hour)) {
		t.Error("an entry past its window was served: the band rests on evidence that has aged out")
	}
	if stale.usable("different", assessedAt.Add(-time.Hour)) {
		t.Error("an entry whose inputs moved was served")
	}
}

// An assessment resting only on values that never age has no expiry, and must
// not be treated as expiring at the zero time.
func TestAnEntryWithNoExpiryIsServedIndefinitely(t *testing.T) {
	forever := storedGrowthFit{Version: growthFitStoredVersion, Fingerprint: "abc"}

	if !forever.usable("abc", assessedAt.AddDate(10, 0, 0)) {
		t.Error("an entry with nothing that can age out was refused a decade later")
	}
}

// A payload written by an older shape must not be served through a newer
// envelope with its new fields zeroed.
func TestAnEntryFromAnotherPayloadShapeIsNotServed(t *testing.T) {
	old := storedGrowthFit{Version: growthFitStoredVersion - 1, Fingerprint: "abc"}

	if old.usable("abc", assessedAt) {
		t.Error("an entry this build cannot read was served")
	}
}
