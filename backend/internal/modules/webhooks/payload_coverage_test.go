// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

// This gate defines "done" for Phase 4 (typed per-event webhook payloads,
// A7): every subscribable event type — the full events.md §5 catalog minus
// the entity-less pipeline-event class, which is never subscribable
// (BYO-EVT-4, the create-time validateEventTypes gate) — must carry a
// WebhookPayload<Event> schema in api/public-events.yaml. gen-payloads
// projects that schema set into crmcontracts.WebhookPayloadVersions (one
// generated map, keyed by x-event-type), so this test needs no manual
// enumeration of its own: it walks the SAME catalog the runtime routes on
// and asserts against the SAME registry gen-payloads emits.
//
// It is EXPECTED RED today: only the deal.stage_changed pilot has a schema.
// It goes green only at Task 5-final, once every Phase-4 family task has
// added its event's schema — that is the intended definition of "Phase 4
// complete", not a regression to chase down now.

import (
	"testing"

	"github.com/stretchr/testify/require"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
)

func TestEverySubscribableEventHasAPayloadSchema(t *testing.T) {
	var uncovered []string
	for _, tp := range events.Types() {
		if events.IsPipelineEvent(tp) {
			continue
		}
		if _, ok := crmcontracts.WebhookPayloadVersions[tp]; !ok {
			uncovered = append(uncovered, tp)
		}
	}
	// One failure naming every still-uncovered type (not just the first) —
	// so the expected-red count during Phase 4 stays legible, and a
	// regression that drops coverage for one already-migrated type is
	// distinguishable from the still-in-progress baseline by WHICH types
	// the message names, not just that the test failed.
	require.Empty(t, uncovered, "no WebhookPayload schema for %d subscribable event(s): %v (add each to public-events.yaml)", len(uncovered), uncovered)
}
