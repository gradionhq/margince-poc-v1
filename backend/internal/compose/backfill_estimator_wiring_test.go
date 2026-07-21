// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
)

// TestWithBackfillEstimatorWiresThePromotedField locks the composition-root wiring
// of the ADR-0068 cost estimator against a silent-nil regression.
//
// backfillHandlers is EMBEDDED in Server, and PreviewConnectorBackfill has a value
// receiver that reads h.estimator behind an `h.estimator != nil` guard (a nil estimator
// degrades the preview to a message-count-only number — cost is transparency, never a
// gate). That guard means a mis-wired option — assigning to a shadow field on Server
// instead of the promoted backfillHandlers.estimator — would DISABLE cost pricing
// entirely yet pass every existing handler test, because those construct backfillHandlers
// directly with the field set. This test is the only one exercising the option path, so
// it asserts WithBackfillEstimator populates the field the handler actually reads.
func TestWithBackfillEstimatorWiresThePromotedField(t *testing.T) {
	// A local router gives the option a real (provider, model) binding without a
	// network; the estimator is only constructed here, never called, so a nil pool
	// (its stores merely hold it) is fine.
	router, err := ai.NewLocalRouter(ai.RoutingConfig{
		Profile: ai.ProfileEUHosted,
		Tiers: map[ai.Tier]ai.ProviderConfig{
			ai.TierLocalSmall: {Provider: ai.ProviderFake, Model: "local-model"},
			ai.TierCheapCloud: {Provider: ai.ProviderFake, Model: "cloud-model"},
			ai.TierPremium:    {Provider: ai.ProviderFake, Model: "premium-model"},
		},
		Embeddings: ai.ProviderConfig{Provider: ai.ProviderFake, Model: "embed-model"},
	})
	if err != nil {
		t.Fatalf("NewLocalRouter: %v", err)
	}

	s := &Server{}
	// The option self-gates on a non-nil backfill registry; a zero-value Registry is a
	// non-nil sentinel that satisfies the guard (the estimator never calls it here).
	s.backfillHandlers.registry = &capture.Registry{}

	WithBackfillEstimator(router)(s, nil)

	// Assert the EXPLICIT embedded selector on purpose (not the promoted `s.estimator`
	// QF1008 suggests): the option assigns via `s.estimator`, so asserting the same
	// promoted name would be tautological and would silently pass if a shadow
	// `Server.estimator` field ever hid the handler's `backfillHandlers.estimator`.
	if s.backfillHandlers.estimator == nil { //nolint:staticcheck // QF1008 intentional — see above

		t.Fatal("WithBackfillEstimator left backfillHandlers.estimator nil — the value-receiver " +
			"preview handler reads h.estimator, so a shadow assignment would silently disable cost " +
			"pricing (the h.estimator != nil guard hides it) while every existing handler test still passes")
	}
}
