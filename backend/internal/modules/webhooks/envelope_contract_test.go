// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks_test

import (
	"encoding/json"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// The public delivery envelope is a trust-boundary contract: it must carry the
// documented public fields and none of the internal envelope metadata. Because
// the struct is generated from api/public-events.yaml, this is what keeps the
// generated shape honest — leaking an internal field would fail this test, not
// merely a review.
func TestPublicEnvelopeOmitsInternalFields(t *testing.T) {
	var e crmcontracts.PublicEventEnvelope
	j, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := string(j)
	for _, internal := range []string{"audit_log_id", "causation_id", "passport_id", "on_behalf_of", "workspace_id"} {
		if strings.Contains(s, internal) {
			t.Errorf("public envelope must not leak internal field %q: should not contain %v", internal, internal)
		}
	}
	for _, public := range []string{"event_id", "type", "version", "occurred_at", "entity", "correlation_id"} {
		if !strings.Contains(s, public) {
			t.Errorf("%q should contain %q", s, public)
		}
	}
}
