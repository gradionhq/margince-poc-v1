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
	"github.com/gradionhq/margince/backend/internal/modules/webhooks"
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
// inbox has no visibility rule for when the staging names a concrete record,
// each with what it costs.
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
	"record_grant": "createRecordGrant/revokeRecordGrant. Left waived rather than fixed because " +
		"visibility is not the only gap: share_record ALSO dead-ends at redemption (deadEndVerbs in " +
		"agentpolicysynthesis_test.go — the grant verbs reject any non-human principal, so an " +
		"agent-staged, human-approved grant is refused as the redeeming agent every time). An arm here " +
		"would make the row decidable and the operation would still never land, which is worse than an " +
		"honest dead end. Answer the redemption question first.",
}

// Every confirm-first operation that names a concrete record type must stage a
// row a human can actually SEE and DECIDE.
//
// The read-side twin of the pin gate above, and it closes the same shape of hole
// one level further on: a pinned target nobody can see is still a zombie. The
// invariant is derived from the generated policy table rather than from a list of
// the types someone remembered, so a verb that becomes confirm-first upstream
// fails here until its staged shape gains a visibility rule or a ratified reason.
//
// The subject is the staged SHAPE, not the record type alone. stageRefusal reads
// the target id out of the route's {id} parameter, so a route without one stages
// its type with a NULL id — a different decidability question from the same
// type's, and one a type-only walk answers green over.
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
		if !approvals.KindHasDecisionGrants(pol.Tool) {
			// No row is ever minted: stageRefusal refuses the call at exactly this
			// mapping check, which is an honest 403 rather than an authority
			// object nobody can decide. Every 🟡 tool the REGISTRY admits is held
			// to the mapping by TestEveryConfirmationRequiredToolHasADecisionGrantMapping,
			// so this names only the contract-only verbs whose kind was never
			// mapped, and it cannot become a way to skip the check below.
			continue
		}
		checked++
		if approvals.TargetShapeDecidable(string(pol.RecordType), strings.Contains(route, "{id}")) {
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

// The approvals inbox and the webhook fan-out each decide, from the staged
// target's type, whether an approval may be shown at all — and BOTH must have a
// rule for a type or NEITHER may. A type only the inbox classifies is an
// approval.requested silently dropped, so nobody is told authority is waiting; a
// type only the fan-out classifies is a staged row the inbox strands, which
// nothing then clears.
//
// Two hand-written classifications in two modules that must agree is the shape
// that drifts, and it did twice: `project` gained an inbox rule and the fan-out
// never noticed, and the rate sheets gained one the fan-out never noticed either.
// The assertion belongs in the composition layer because a module never imports a
// sibling and this layer imports both.
//
// THE SUBJECT SET IS THE UNION OF THREE SOURCES, and each is there because the
// others cannot see part of the invariant:
//
//   - every record type in the generated policy table — so a type the CONTRACT
//     adds is covered without anybody remembering to extend a list, and so the
//     gate cannot pass vacuously if both enumerators below went empty;
//   - every type the approvals inbox classifies — because a target staged by a
//     server-side proposal flow rather than by an agent's call (an effective-dated
//     rate sheet) appears in NO agent policy, which is precisely how the second
//     drift hid from a gate that walked the policy table alone;
//   - every type the fan-out classifies — the mirror direction, a type the
//     fan-out delivers on and the inbox strands.
//
// A gate whose subject set is narrower than the invariant it claims reads the
// wrong tree, which is quieter than reading it wrongly.
func TestTheInboxAndTheFanOutClassifyEveryTargetTypeAlike(t *testing.T) {
	subjects := map[string]bool{}
	for _, pol := range agentPolicies {
		if pol.RecordType != "" {
			subjects[string(pol.RecordType)] = true
		}
	}
	for _, targetType := range approvals.ClassifiedTargetTypes() {
		subjects[targetType] = true
	}
	for _, targetType := range webhooks.ClassifiedApprovalTargetTypes() {
		subjects[targetType] = true
	}

	for recordType := range subjects {
		// The pair is asked with a target id present because the question is
		// whether the TYPE carries a rule: the id-less shape is settled before
		// any type is consulted, and would report every type alike.
		inbox := approvals.TargetShapeDecidable(recordType, true)
		fanOut := webhooks.ApprovalTargetClassified(recordType)
		if inbox == fanOut {
			continue
		}
		known, missing := "the approvals inbox", "the webhook fan-out"
		if fanOut {
			known, missing = missing, known
		}
		t.Errorf("%s classifies target type %q and %s does not — give %s the arm that mirrors the "+
			"owning store's read rule, so a staged row is both decidable and announced",
			known, recordType, missing, missing)
	}
	// The floor that keeps agreement from being vacuous: both classifications
	// answer false for everything when both are empty, which is agreement over
	// nothing.
	if len(subjects) == 0 {
		t.Fatal("the union of the policy table and both classifications is empty — the parity gate covers nothing")
	}
}
