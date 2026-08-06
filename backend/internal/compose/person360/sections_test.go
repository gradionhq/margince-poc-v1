// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// The claim path is the join between the page that RENDERS a correction and
// the ledger that STORES it. Spelled differently on either side, the verdict
// is filed against a claim nothing ever consults again — and the failure is
// silent, looking exactly like the correction not sticking.
func TestProfileFieldClaimPathIsStableAndPerField(t *testing.T) {
	title := profileFieldClaimPath("title")
	if title != "profile_field:title" {
		t.Errorf("claim path = %q, want profile_field:title", title)
	}
	if profileFieldClaimPath("phone") == title {
		t.Error("two different fields produced one claim path; correcting one would correct both")
	}
	// The ledger hashes the path, and the hash has to agree across calls or a
	// verdict recorded today is unreadable tomorrow.
	if ai.ClaimKey(title) != ai.ClaimKey(profileFieldClaimPath("title")) {
		t.Error("the same claim path hashed to two different keys")
	}
}

// Case is normalized on the way into the ledger, so a field spelled "Title"
// somewhere cannot lose a correction made against "title".
func TestClaimKeyFoldsCase(t *testing.T) {
	if ai.ClaimKey("profile_field:title") != ai.ClaimKey("Profile_Field:Title") {
		t.Error("claim keys are case-sensitive; a capitalization would lose the human's answer")
	}
}
