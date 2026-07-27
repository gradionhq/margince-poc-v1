// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// capturingApprovals records the StageRequest so a test can inspect what the
// gate actually staged.
type capturingApprovals struct{ last agents.StageRequest }

func (c *capturingApprovals) Stage(_ context.Context, in agents.StageRequest) (ids.ApprovalID, error) {
	c.last = in
	return ids.ApprovalID{}, nil
}

func (c *capturingApprovals) Redeem(_ context.Context, _ ids.ApprovalID, _, _ string) error {
	return nil
}

// fakeVersionReader returns a fixed current version, standing in for the
// system-of-record provider's server-side read.
type fakeVersionReader struct{ version int64 }

func (f fakeVersionReader) Read(_ context.Context, _ datasource.EntityRef) (datasource.Record, error) {
	return datasource.Record{Version: f.version}, nil
}

// A 🟡 REST mutation staged by an agent that sends NO If-Match must still
// carry the target's CURRENT version, read server-side — otherwise the
// redemption skew check short-circuits on a NULL pin and the approved
// effect applies to a drifted row (F-006). The agent cannot opt out of the
// version binding by omitting the header.
func TestStageRefusalPinsTargetVersionServerSide(t *testing.T) {
	staging := &capturingApprovals{}
	reader := fakeVersionReader{version: 7}
	pol := agentPolicy{Op: "archiveDeal", Access: "tool", Tool: "archive_record", RecordType: "deal"}

	dealID := ids.NewV7()
	req := httptest.NewRequest(http.MethodDelete, "/v1/deals/"+dealID.String(), nil) // no If-Match
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", dealID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	stageRefusal(httptest.NewRecorder(), req, staging, reader, pol, nil)

	if staging.last.TargetVersion == nil {
		t.Fatal("staged approval carries no target_version — omitting If-Match must not opt out of the skew check")
	}
	if *staging.last.TargetVersion != 7 {
		t.Fatalf("target_version = %d, want the server-read 7", *staging.last.TargetVersion)
	}
	if staging.last.TargetType != "deal" || staging.last.TargetID != dealID {
		t.Fatalf("staged target = (%s,%s), want (deal,%s)", staging.last.TargetType, staging.last.TargetID, dealID)
	}
}
