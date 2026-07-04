package rubric

import "testing"

func TestLoad_everyRuleCarriesIDCategoryAndBlockFlag(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.Version == "" {
		t.Error("rubric version is empty; the version pins the gate identity tuple")
	}
	if len(r.Rules) == 0 {
		t.Fatal("rubric has no rules")
	}
	seen := map[string]bool{}
	for _, rule := range r.Rules {
		if rule.ID == "" {
			t.Errorf("rule with title %q has no id", rule.Title)
		}
		if rule.Category == "" {
			t.Errorf("rule %s has no category", rule.ID)
		}
		if rule.Kind != KindAntiTell && rule.Kind != KindPositive {
			t.Errorf("rule %s has unknown kind %q", rule.ID, rule.Kind)
		}
		// block_eligible is the field downstream BLOCK-mapping reads; positive
		// guidelines must never be block-eligible.
		if rule.Kind == KindPositive && rule.BlockEligible {
			t.Errorf("positive rule %s is block_eligible; only anti-tells block", rule.ID)
		}
		if seen[rule.ID] {
			t.Errorf("duplicate rule id %s", rule.ID)
		}
		seen[rule.ID] = true
	}
}

func TestBlockEligible(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tests := []struct {
		category string
		want     bool
	}{
		{"over-commenting", true},
		{"type-escape-hatch", true},
		{"idiomatic", false},
		{"restraint", false},
		{"category-that-does-not-exist", false},
	}
	for _, tt := range tests {
		if got := r.BlockEligible(tt.category); got != tt.want {
			t.Errorf("BlockEligible(%q) = %v, want %v", tt.category, got, tt.want)
		}
	}
}
