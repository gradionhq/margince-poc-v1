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
	pol := agentPolicy{Op: "archiveDeal", Access: accessTool, Tool: "archive_record", RecordType: recordTypeDeal}

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
		if pol.Access != accessTool || pol.Tier == tierAutoExecute || pol.RecordType == "" {
			continue
		}
		checked++
		if approvals.TargetVersionCheckable(string(pol.RecordType)) {
			continue
		}
		if _, ratified := unpinnableConfirmFirstTypes[string(pol.RecordType)]; ratified {
			used[string(pol.RecordType)] = true
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
	pol := agentPolicy{Op: "sendOffer", Access: accessTool, Tool: "send_offer", RecordType: recordTypeOffer}

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
	pol := agentPolicy{Op: "createCustomField", Access: accessTool, Tool: "create_record", RecordType: recordTypeCustomField}

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

// undecidableConfirmFirstTypes are the confirm-first target types the approvals
// inbox has no visibility rule for, each with what it costs.
//
// The cost is the same for every entry and it is severe: `decidable` backs the
// inbox list, the single Get and the Decide, so a row it rejects is invisible AND
// undecidable — an authority object a human can neither release nor reject. The
// approval.requested fan-out is dropped for the same reason
// (webhooks.approvalTargetVisible has the matching arms), so nobody is even told.
// The row then sits pending until the staging TTL clears it, and an agent
// retrying a legitimate refusal accumulates more of them.
//
// This is a backlog, not a design: every entry is a confirm-first verb the tool
// surface or a passport's REST call can stage today. It is written down so the
// class is enumerated rather than invisible, and so it can only shrink — a NEW
// confirm-first record type joins it deliberately or fails the gate below.
var undecidableConfirmFirstTypes = map[string]string{
	"list": "archiveList stages against a list; a curator cannot release or reject the deletion of " +
		"their own segment. The row is owner-scoped, so this is an auth.VisibleTo arm.",
	"tag": "archiveTag — same shape and the same one-line fix as `list`.",
	"record_grant": "createRecordGrant/revokeRecordGrant. Left waived rather than fixed because " +
		"visibility is not the only gap: share_record ALSO dead-ends at redemption (deadEndVerbs in " +
		"agentpolicysynthesis_test.go — the grant verbs reject any non-human principal, so an " +
		"agent-staged, human-approved grant is refused as the redeeming agent every time). An arm here " +
		"would make the row decidable and the operation would still never land, which is worse than an " +
		"honest dead end. Answer the redemption question first.",
	"saved_view": "archiveSavedView; the owner cannot release the deletion of their own view. " +
		"Owner-scoped, so an auth.VisibleTo arm.",
	"offer_template": "archiveOfferTemplate; workspace-shared config with no row scope, so it wants " +
		"the targetExists floor `product` and `custom_field` already use, not a scope probe.",
	"overlay_connection": "disconnectIncumbent stages against the connection row, so nobody can " +
		"approve cutting the workspace back to native mode — the one transition an operator most needs " +
		"to confirm deliberately.",
	"webhook_subscription": "archiveWebhookSubscription; the subscription owner cannot release the " +
		"deletion of their own endpoint. Owner-scoped.",
}

// Every confirm-first operation that names a concrete record type must stage a
// row a human can actually SEE and DECIDE.
//
// The read-side twin of the pin gate above, and it closes the same shape of hole
// one level further on: a pinned target nobody can see is still a zombie. The
// invariant is derived from the generated policy table rather than from a list of
// the types someone remembered, so a verb that becomes confirm-first upstream
// fails here until its target type gains a visibility rule or a ratified reason.
func TestEveryConfirmFirstTargetTypeIsDecidable(t *testing.T) {
	for recordType, cost := range undecidableConfirmFirstTypes {
		if strings.TrimSpace(cost) == "" {
			t.Errorf("undecidableConfirmFirstTypes[%s] has no reason — a waiver must say what it costs", recordType)
		}
	}

	used := map[string]bool{}
	checked := 0
	for route, pol := range agentPolicies {
		if pol.Access != accessTool || pol.Tier == tierAutoExecute || pol.RecordType == "" {
			continue
		}
		checked++
		if approvals.TargetTypeDecidable(string(pol.RecordType)) {
			continue
		}
		if _, ratified := undecidableConfirmFirstTypes[string(pol.RecordType)]; ratified {
			used[string(pol.RecordType)] = true
			continue
		}
		t.Errorf("%s (%s) stages against %q, which approvals.targetVisible has no rule for — the staged "+
			"row would be invisible in the inbox and undecidable at the decision, so no human could ever "+
			"release or reject it. Give the type a visibility arm, or ratify the residue in "+
			"undecidableConfirmFirstTypes with what it costs.", route, pol.Op, pol.RecordType)
	}
	if checked == 0 {
		t.Fatal("no confirm-first record-typed routes in the generated policy — the gate no longer covers anything")
	}
	for recordType := range undecidableConfirmFirstTypes {
		if !used[recordType] {
			t.Errorf("undecidableConfirmFirstTypes[%s] matches no confirm-first route, or the type gained a "+
				"visibility rule — stale waiver, remove it", recordType)
		}
	}
}
