// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"strings"
	"testing"
)

// A data reset wipes an installation's DATA, not the decision about which
// vendor may process it.
//
// This one fails quietly in exactly the environment nobody tests in. A dev
// stack re-seeds the binding from its routing file on the next boot, so a reset
// looks harmless there; a production installation has no file, so the same
// reset leaves it with no binding and its AI lanes simply gone — discovered
// whenever someone next opens a surface that needs a model, not at the reset.
func TestTheRoutingBindingSurvivesADataReset(t *testing.T) {
	if !Routing.SurvivesDataReset() {
		t.Error("a data reset would delete ai.routing; the model binding is what bootstrap took from the " +
			"deployment configuration, like the installation's name and currency, and a reset spares those")
	}
}

// The registered default must be "unconfigured" and never a working binding. A
// default that named vendors would send an installation's text somewhere nobody
// chose — and it would do it on the read of an UNSET setting, which is the
// state every installation is in before anyone binds a model.
func TestTheRegisteredDefaultBindsNothing(t *testing.T) {
	raw, err := Routing.DefaultJSON()
	if err != nil {
		t.Fatalf("the registered default does not encode: %v", err)
	}
	for _, vendor := range []string{"gemini", "anthropic", "openai", "ollama", "vllm"} {
		if strings.Contains(string(raw), vendor) {
			t.Errorf("the registered default names %q; an unset binding must bind nothing: %s", vendor, raw)
		}
	}
}

// The zero config is the one value the validator must accept, because it IS the
// registered default — refusing it would make every unset installation fail its
// own catalog. Everything else is held to the bar the file loader applies.
func TestValidationAcceptsUnconfiguredAndRefusesAHalfBinding(t *testing.T) {
	// The zero document is the registered default, so refusing it would make
	// every unset installation fail its own catalog.
	if err := Routing.ValidateJSON([]byte(`{}`)); err != nil {
		t.Errorf("the unconfigured binding was refused: %v", err)
	}
	// A profile with no tiers is REFUSED, and the exemption above is narrower
	// than it looks for that reason. Both documents serve nothing, but this one
	// somebody wrote — storing it would accept a binding that reads as
	// configured, routes nothing, and reports no fault. The file loader refuses
	// it ("no tiers bound"), and a stored binding is held to the same bar.
	if err := Routing.ValidateJSON([]byte(`{"profile":"eu_hosted","tiers":{}}`)); err == nil {
		t.Error("a profile binding no tiers was stored; it reads as configured and routes nothing")
	}
	if err := Routing.ValidateJSON([]byte(`{"profile":"nowhere","tiers":{"premium":{"provider":"gemini","model":"m"}}}`)); err == nil {
		t.Error("an unknown profile was accepted; a stored binding must meet the bar the file loader applies")
	}
}
