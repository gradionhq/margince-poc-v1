// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestCompanyContextMetadataReachesTheCallTrace(t *testing.T) {
	calls := &fakeCallStore{}
	router := newTracingRouter(t, stubClient{resp: model.Response{Text: "hi"}}, calls)
	fingerprint := strings.Repeat("a", 64)
	request := model.Request{
		ContextScopes: []string{"identity", "offer"}, ContextFingerprint: fingerprint,
	}
	if _, _, err := router.serveCompletion(wsCtx(), TaskDraftReply, []Tier{TierCheapCloud}, request); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(calls.recorded) != 1 {
		t.Fatalf("recorded calls = %d, want 1", len(calls.recorded))
	}
	got := calls.recorded[0]
	if !reflect.DeepEqual(got.ContextScopes, request.ContextScopes) || got.ContextFingerprint != fingerprint {
		t.Fatalf("trace lost company-context metadata: %+v", got)
	}
}
