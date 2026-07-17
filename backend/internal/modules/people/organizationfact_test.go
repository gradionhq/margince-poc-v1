// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The closed organization-fact vocabulary's own contract: the normalized
// value_key is deterministic, and a staged fact outside the vocabulary
// (or violating the row's CHECKs) fails with a named reason before any
// write.

import "testing"

func TestNormalizeFactValueKeyReducesAValueToItsNameIdentity(t *testing.T) {
	cases := map[string]string{
		"CRM Rollout — end-to-end delivery": "crm rollout",
		"  crm   ROLLOUT  ":                 "crm rollout",
		"Data Migration":                    "data migration",
		" — only a description":             "",
	}
	for value, want := range cases {
		if got := NormalizeFactValueKey(value); got != want {
			t.Errorf("NormalizeFactValueKey(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestValidDeepReadFactRefusesWhatTheRowCheckWould(t *testing.T) {
	grounded := DeepReadFact{
		Category: "offering", Field: "service", Value: "CRM Rollout — delivery",
		ValueKey: "crm rollout", EvidenceSnippet: "CRM Rollout", Confidence: 0.9,
	}
	if err := validDeepReadFact(grounded); err != nil {
		t.Fatalf("a well-formed fact was refused: %v", err)
	}

	bad := map[string]func(DeepReadFact) DeepReadFact{
		"unknown category":                  func(f DeepReadFact) DeepReadFact { f.Category = "team"; return f },
		"field outside its category":        func(f DeepReadFact) DeepReadFact { f.Category = "company"; f.Field = "service"; return f },
		"multi-value without a value_key":   func(f DeepReadFact) DeepReadFact { f.ValueKey = ""; return f },
		"single-value carrying a value_key": func(f DeepReadFact) DeepReadFact { f.Category = "company"; f.Field = "phone"; return f },
		"empty value":                       func(f DeepReadFact) DeepReadFact { f.Value = " "; return f },
		"empty evidence":                    func(f DeepReadFact) DeepReadFact { f.EvidenceSnippet = ""; return f },
		"confidence at zero":                func(f DeepReadFact) DeepReadFact { f.Confidence = 0; return f },
		"confidence above one":              func(f DeepReadFact) DeepReadFact { f.Confidence = 1.5; return f },
	}
	for name, mutate := range bad {
		if err := validDeepReadFact(mutate(grounded)); err == nil {
			t.Errorf("%s passed validation — the row CHECK would have made it a 500", name)
		}
	}
}
