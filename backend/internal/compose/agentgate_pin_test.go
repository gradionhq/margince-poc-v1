// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// capturingApprovals records the StageRequest so a test can inspect what the
// gate actually staged.
type capturingApprovals struct{ last agents.StageRequest }

func (c *capturingApprovals) Stage(_ context.Context, in agents.StageRequest) (ids.ApprovalID, error) {
	c.last = in
	return ids.ApprovalID{}, nil
}

func (c *capturingApprovals) Redeem(_ context.Context, _ ids.ApprovalID, _, _ string) (int64, bool, error) {
	return 0, false, nil
}

// The gate's half of the version binding: it names the concrete target and
// hands NO pin of its own, so the approvals engine resolves the version
// server-side inside the staging transaction. The gate has only one pin it
// could offer — the caller's If-Match — and that is exactly the one an agent
// can decline to send.
func TestStageRefusalNamesTheTargetAndSuppliesNoClientPin(t *testing.T) {
	dealID := ids.NewV7()
	pol := agentPolicy{Op: "archiveDeal", Access: accessTool, Tool: "archive_record", RecordType: "deal"}

	for _, tc := range []struct{ name, ifMatch string }{
		{"no If-Match", ""},
		{"If-Match sent anyway", "7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			staging := &capturingApprovals{}
			req := httptest.NewRequest(http.MethodDelete, "/v1/deals/"+dealID.String(), nil)
			if tc.ifMatch != "" {
				req.Header.Set("If-Match", tc.ifMatch)
			}
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", dealID.String())
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			stageRefusal(httptest.NewRecorder(), req, staging, pol, nil)

			if staging.last.TargetType != "deal" || staging.last.TargetID != dealID {
				t.Fatalf("staged target = (%s,%s), want (deal,%s) — the engine cannot pin a target it was not given",
					staging.last.TargetType, staging.last.TargetID, dealID)
			}
			if staging.last.TargetVersion != nil {
				t.Errorf("the gate supplied target_version %d — the pin must come from the row, not from the caller",
					*staging.last.TargetVersion)
			}
		})
	}
}

// unpinnableConfirmFirstTypes are the confirm-first target types with no
// version column to pin, each with the rationale that ratified it. They fall
// back to the diff_hash identical-call binding, which still refuses a
// DIFFERENT call but not a drifted row, so every entry here is a known,
// bounded residue rather than an oversight — and a NEW confirm-first record
// type joins this list deliberately or fails the gate below.
var unpinnableConfirmFirstTypes = map[string]string{
	"custom_field":         "the field catalog is workspace-shared admin config with no version column; its DDL engine serializes on the catalog row itself",
	"webhook_subscription": "subscription rows carry no version column; the staged change is the whole subscription body, which diff_hash binds verbatim",
	"offer_template":       "template rows carry no version column; the staged change is the whole template body, which diff_hash binds verbatim",
	"saved_view":           "saved views carry no version column and hold no money or consent state; diff_hash binds the whole definition",
	"record_grant":         "a grant is created or revoked whole, never patched, so there is no prior version for a pin to bind",
	"overlay_connection":   "connection rows carry no version column; connect/disconnect are whole-row transitions the diff_hash binds",
}

// Every confirm-first operation that names a concrete record type must have
// a type the approvals engine can PIN — or sit in the ratified list above
// with a reason. This is the read-side twin the pin was missing: the gate
// used to take a server-side pin for exactly the five datasource-readable
// types and fall back to the agent's own If-Match for the rest, so most
// confirm-first routes carried a pin the agent could simply decline to
// supply, and nothing said so.
func TestConfirmFirstTargetsArePinnable(t *testing.T) {
	for recordType, rationale := range unpinnableConfirmFirstTypes {
		if strings.TrimSpace(rationale) == "" {
			t.Errorf("unpinnableConfirmFirstTypes[%s] has no rationale — a waiver must say why no pin is possible", recordType)
		}
	}

	used := map[string]bool{}
	checked := 0
	for route, pol := range agentPolicies {
		if pol.Access != accessTool || pol.Tier == "auto_execute" || pol.RecordType == "" {
			continue
		}
		checked++
		if approvals.TargetVersionCheckable(pol.RecordType) {
			continue
		}
		if _, ratified := unpinnableConfirmFirstTypes[pol.RecordType]; ratified {
			used[pol.RecordType] = true
			continue
		}
		t.Errorf("%s (%s) stages against %q, which carries no version pin — either give the table a version column "+
			"or ratify the residue in unpinnableConfirmFirstTypes", route, pol.Op, pol.RecordType)
	}
	if checked == 0 {
		t.Fatal("no confirm-first record-typed routes in the generated policy — the pin no longer covers anything")
	}
	for recordType := range unpinnableConfirmFirstTypes {
		if !used[recordType] {
			t.Errorf("unpinnableConfirmFirstTypes[%s] matches no confirm-first route — stale waiver, remove it", recordType)
		}
	}
}

// pinningApprovals redeems successfully and reports a pin, standing in for
// an approval whose target carried a version.
type pinningApprovals struct{ version int64 }

func (pinningApprovals) Stage(_ context.Context, _ agents.StageRequest) (ids.ApprovalID, error) {
	return ids.ApprovalID{}, nil
}

func (p pinningApprovals) Redeem(_ context.Context, _ ids.ApprovalID, _, _ string) (int64, bool, error) {
	return p.version, true, nil
}

// Redemption commits its own transaction and the handler below opens a
// fresh one, so the skew check inside the redemption proves only what was
// true at redeem-commit time. The gate therefore carries the pin forward as
// the request's own If-Match, which puts the version compare inside the
// transaction that actually writes — the same window the agent would
// otherwise control from both ends.
func TestRedemptionCarriesThePinOntoTheForwardedRequest(t *testing.T) {
	approvalID := ids.New[ids.ApprovalKind]()
	pol := agentPolicy{Op: "sendOffer", Access: accessTool, Tool: "send_offer", RecordType: "offer"}

	var forwarded string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		forwarded = r.Header.Get("If-Match")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/offers/x/send", nil)
	req.Header.Set(approvalTokenHeader, approvalID.String())

	if !redeemIfPresented(httptest.NewRecorder(), req, next, pinningApprovals{version: 9}, pol, nil) {
		t.Fatal("a presented token must be handled by the gate")
	}
	if forwarded != "9" {
		t.Errorf("forwarded If-Match = %q, want \"9\" — the store must re-check the pin in its own write transaction", forwarded)
	}
}

// An approval with no pin leaves the header alone: there is nothing to bind
// to, and inventing a version would refuse a legitimate redemption.
func TestRedemptionWithoutAPinLeavesIfMatchAlone(t *testing.T) {
	approvalID := ids.New[ids.ApprovalKind]()
	pol := agentPolicy{Op: "createCustomField", Access: accessTool, Tool: "create_record", RecordType: "custom_field"}

	var forwarded string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		forwarded = r.Header.Get("If-Match")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/custom-fields", nil)
	req.Header.Set(approvalTokenHeader, approvalID.String())

	if !redeemIfPresented(httptest.NewRecorder(), req, next, &capturingApprovals{}, pol, nil) {
		t.Fatal("a presented token must be handled by the gate")
	}
	if forwarded != "" {
		t.Errorf("forwarded If-Match = %q, want it unset for an unpinned approval", forwarded)
	}
}
