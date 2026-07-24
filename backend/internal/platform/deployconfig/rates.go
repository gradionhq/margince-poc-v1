// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

import (
	"fmt"
	"strings"
)

// RatesConfig is the (worker-role) source config for the admin "Refresh from
// sources" jobs. Fx is the URL of a page the FX refresh fetches and AI-extracts
// rates from (defaults to api.frankfurter.dev when empty — read as page text,
// not parsed JSON); FxCurrencies is the candidate set the FX refresh proposes to
// bootstrap an empty sheet (worker default: USD/GBP/CHF).
// ModelPricing maps a provider to its pricing-page URL; absent ⇒ the model-cost
// refresh no-ops. The FX refresh, by contrast, always has a source and a
// candidate set (both default), so it never no-ops on absence. Neither refresh
// auto-applies — a human approves every staged proposal.
type RatesConfig struct {
	Fx           string            `yaml:"fx_source"`
	FxCurrencies []string          `yaml:"fx_currencies"`
	ModelPricing map[string]string `yaml:"model_pricing"`
}

// validate fails closed on a malformed candidate set: every fx_currencies entry
// must be an ISO 4217 code (the same shape organization.base_currency is held
// to), and no currency may repeat. A typo must surface at boot, never as a
// silently dropped bootstrap symbol the FX source omits without a trace.
func (r RatesConfig) validate() error {
	seen := make(map[string]bool, len(r.FxCurrencies))
	for _, c := range r.FxCurrencies {
		code := strings.ToUpper(strings.TrimSpace(c))
		if !isCurrencyCode(code) {
			return fmt.Errorf("deployconfig: rates.fx_currencies %q is not a 3-letter ISO 4217 code", c)
		}
		if seen[code] {
			return fmt.Errorf("deployconfig: rates.fx_currencies has a duplicate entry %q", code)
		}
		seen[code] = true
	}
	return nil
}
