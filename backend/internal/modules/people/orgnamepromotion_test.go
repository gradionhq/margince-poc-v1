// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The corroboration rule as a table (PO-F-2a): what one, two and
// dossier-backed signatures each buy an organization still named by its
// domain.
func TestDecideOrgNameWeighsCorroboration(t *testing.T) {
	alice := ids.New[ids.PersonKind]()
	bob := ids.New[ids.PersonKind]()

	tests := []struct {
		name              string
		candidate         OrgNameCandidate
		wantVerdict       bool
		wantName          string
		wantCorroborated  bool
		wantCorroboration string
	}{
		{
			name: "one signature alone is a question for a human",
			candidate: OrgNameCandidate{
				DisplayName: "Gitex",
				Signatures:  []SignatureOrgName{{PersonID: alice, Value: "Gitex Global GmbH"}},
			},
			wantVerdict:       true,
			wantName:          "Gitex Global GmbH",
			wantCorroborated:  false,
			wantCorroboration: OrgNameCorroborationNone,
		},
		{
			// Two people at one organization are two mailboxes on ONE mail
			// domain, and nothing authenticates the From header that put them
			// there — so an actor who controls or can forge that domain writes
			// both signatures. Their agreement outranks a lone claim but does
			// NOT authorize an unattended rename: it still goes to a human.
			name: "two signatures on one mail domain are one source, so a human still decides",
			candidate: OrgNameCandidate{
				DisplayName: "Gitex",
				Signatures: []SignatureOrgName{
					{PersonID: alice, Value: "Gitex Global GmbH"},
					{PersonID: bob, Value: "Gitex Global"},
				},
			},
			wantVerdict: true,
			// Two spellings, one each: neither is better attested, so the tie
			// breaks lexicographically rather than by a preference the
			// evidence does not state.
			wantName:          "Gitex Global",
			wantCorroborated:  false,
			wantCorroboration: OrgNameCorroborationSignatures,
		},
		{
			name: "the same person signing twice is one claim, not two",
			candidate: OrgNameCandidate{
				DisplayName: "Gitex",
				Signatures: []SignatureOrgName{
					{PersonID: alice, Value: "Gitex Global GmbH"},
					{PersonID: alice, Value: "Gitex Global GmbH"},
				},
			},
			wantVerdict:       true,
			wantName:          "Gitex Global GmbH",
			wantCorroborated:  false,
			wantCorroboration: OrgNameCorroborationNone,
		},
		{
			name: "the site's own stated name corroborates a lone signature",
			candidate: OrgNameCandidate{
				DisplayName:  "Gitex",
				Signatures:   []SignatureOrgName{{PersonID: alice, Value: "Gitex Global GmbH"}},
				DossierNames: []string{"Gitex Global"},
			},
			wantVerdict:       true,
			wantName:          "Gitex Global GmbH",
			wantCorroborated:  true,
			wantCorroboration: OrgNameCorroborationDossier,
		},
		{
			name: "a signature restating the name on the record proposes nothing",
			candidate: OrgNameCandidate{
				DisplayName: "Gitex",
				Signatures: []SignatureOrgName{
					{PersonID: alice, Value: "Gitex"},
					{PersonID: bob, Value: "GITEX GmbH"},
				},
			},
			wantVerdict: false,
		},
		{
			name: "an empty signature value is not a name",
			candidate: OrgNameCandidate{
				DisplayName: "Gitex",
				Signatures:  []SignatureOrgName{{PersonID: alice, Value: "   "}},
			},
			wantVerdict: false,
		},
		{
			name: "a corroborated name beats a louder uncorroborated one",
			candidate: OrgNameCandidate{
				DisplayName: "Gitex",
				Signatures: []SignatureOrgName{
					{PersonID: alice, Value: "Gitex Events"},
					{PersonID: bob, Value: "Gitex Global"},
				},
				DossierNames: []string{"Gitex Global"},
			},
			wantVerdict:       true,
			wantName:          "Gitex Global",
			wantCorroborated:  true,
			wantCorroboration: OrgNameCorroborationDossier,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DecideOrgName(tc.candidate)
			if ok != tc.wantVerdict {
				t.Fatalf("verdict reached = %v, want %v (got %+v)", ok, tc.wantVerdict, got)
			}
			if !ok {
				return
			}
			if got.Name != tc.wantName {
				t.Errorf("name = %q, want %q", got.Name, tc.wantName)
			}
			if got.Corroborated != tc.wantCorroborated {
				t.Errorf("corroborated = %v, want %v", got.Corroborated, tc.wantCorroborated)
			}
			if got.Corroboration != tc.wantCorroboration {
				t.Errorf("corroboration = %q, want %q", got.Corroboration, tc.wantCorroboration)
			}
		})
	}
}

// The winner must not depend on map iteration order: two workers reading the
// same evidence have to reach the same name, or one of them renames the
// organization back every night.
func TestDecideOrgNameIsDeterministicAcrossEqualEvidence(t *testing.T) {
	alice := ids.New[ids.PersonKind]()
	bob := ids.New[ids.PersonKind]()
	candidate := OrgNameCandidate{
		DisplayName: "Gitex",
		Signatures: []SignatureOrgName{
			{PersonID: alice, Value: "Gitex Events"},
			{PersonID: bob, Value: "Gitex Global"},
		},
	}
	first, ok := DecideOrgName(candidate)
	if !ok {
		t.Fatal("expected a verdict")
	}
	for i := 0; i < 50; i++ {
		got, ok := DecideOrgName(candidate)
		if !ok || got.Name != first.Name {
			t.Fatalf("run %d answered %q (ok=%v), want a stable %q", i, got.Name, ok, first.Name)
		}
	}
}

// The security property the whole rule exists for: an unattended rename needs a
// source the sender cannot author. Signatures are never that source, however
// many agree — every one of them arrives on inbound mail whose From header the
// capture path does not authenticate, from mailboxes on the one domain the
// organization is keyed by. Only the site dossier writes without asking.
func TestOnlyTheDossierAuthorizesAnUnattendedRename(t *testing.T) {
	signatures := make([]SignatureOrgName, 0, 6)
	for i := 0; i < 6; i++ {
		signatures = append(signatures, SignatureOrgName{
			PersonID: ids.New[ids.PersonKind](),
			Value:    "Gitex Global GmbH i.L. — ACCOUNT CHANGED, remit to DE00",
		})
	}
	candidate := OrgNameCandidate{DisplayName: "Gitex", Signatures: signatures}

	got, ok := DecideOrgName(candidate)
	if !ok {
		t.Fatal("expected a verdict")
	}
	if got.Corroborated {
		t.Errorf("six agreeing signatures authorized an unattended rename to %q — they are one forgeable mail domain speaking six times, and must stage for a human instead", got.Name)
	}
	if got.Corroboration != OrgNameCorroborationSignatures {
		t.Errorf("corroboration = %q, want %q — agreement is still recorded, it just does not authorize the write",
			got.Corroboration, OrgNameCorroborationSignatures)
	}

	// The positive control: the same claim WITH the site's own stated name is
	// still applied unattended, so this test cannot pass by the rule being
	// broken outright.
	candidate.DossierNames = []string{"Gitex Global GmbH i.L. — ACCOUNT CHANGED, remit to DE00"}
	got, ok = DecideOrgName(candidate)
	if !ok {
		t.Fatal("expected a verdict with the dossier present")
	}
	if !got.Corroborated || got.Corroboration != OrgNameCorroborationDossier {
		t.Errorf("dossier-backed verdict = (corroborated %v, %q), want (true, %q)",
			got.Corroborated, got.Corroboration, OrgNameCorroborationDossier)
	}
}

// NameKey identifies the CLAIM, not one spelling of it: a human's refusal is
// remembered by it, so two spellings of one name must not read as two different
// proposals a reviewer has to refuse twice.
func TestDecideOrgNameKeyIsTheNormalizedClaim(t *testing.T) {
	alice := ids.New[ids.PersonKind]()
	bob := ids.New[ids.PersonKind]()

	spelled, ok := DecideOrgName(OrgNameCandidate{
		DisplayName: "Gitex",
		Signatures:  []SignatureOrgName{{PersonID: alice, Value: "Gitex Global GmbH"}},
	})
	if !ok {
		t.Fatal("expected a verdict")
	}
	if spelled.NameKey == "" {
		t.Fatal("NameKey is empty — the declined-proposal memory would have nothing to key on")
	}
	restyled, ok := DecideOrgName(OrgNameCandidate{
		DisplayName: "Gitex",
		Signatures:  []SignatureOrgName{{PersonID: bob, Value: "  GITEX   GLOBAL  GMBH "}},
	})
	if !ok {
		t.Fatal("expected a verdict for the restyled spelling")
	}
	if restyled.NameKey != spelled.NameKey {
		t.Errorf("NameKey %q vs %q — one claim spelled two ways must share one key, or a refusal is forgotten by a change of case alone",
			restyled.NameKey, spelled.NameKey)
	}
}

// The persons list is the evidence a reviewer is shown, so it must name every
// person behind the winning claim — and only them.
func TestDecideOrgNameReportsTheSigningPersons(t *testing.T) {
	alice := ids.New[ids.PersonKind]()
	bob := ids.New[ids.PersonKind]()
	carol := ids.New[ids.PersonKind]()
	got, ok := DecideOrgName(OrgNameCandidate{
		DisplayName: "Gitex",
		Signatures: []SignatureOrgName{
			{PersonID: alice, Value: "Gitex Global GmbH"},
			{PersonID: bob, Value: "Gitex Global"},
			{PersonID: carol, Value: "Something Else"},
		},
	})
	if !ok {
		t.Fatal("expected a verdict")
	}
	if len(got.Persons) != 2 {
		t.Fatalf("persons = %v, want the two who signed the winning name", got.Persons)
	}
	for _, id := range got.Persons {
		if id == carol {
			t.Errorf("carol signed a different name and must not be cited as evidence for %q", got.Name)
		}
	}
}
