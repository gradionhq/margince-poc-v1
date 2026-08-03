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
	"github.com/gradionhq/margince/backend/internal/modules/identity"
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

// unpinnableConfirmFirstTypes are the confirm-first target types no version pin
// can bind, each with the rationale that ratified it. They fall back to the
// diff_hash identical-call binding, which still refuses a DIFFERENT call but not
// a drifted row, so every entry here is a known, bounded residue rather than an
// oversight — and a NEW confirm-first record type joins this list deliberately or
// fails the gate below.
//
// Unpinnable is not the same as "no version column", and each rationale must say
// WHICH it is. A dead column is the trap: the pin reads and re-checks a number
// nothing ever changes, so the binding passes always and reads as protection
// nobody has. The gate can only see membership in approvals.versionTables, so
// whether a column is live is a claim these rationales make and a reader must be
// able to check.
var unpinnableConfirmFirstTypes = map[string]string{
	"custom_field": "custom_field HAS a version column (migrations/core/0063) but nothing maintains it: " +
		"the catalog's own writers (customfields' rename, options and retire paths) issue bare UPDATEs " +
		"rather than storekit's guarded patch, so the column never leaves 1 and never takes an If-Match. " +
		"A pin over it would re-check a constant and pass always. The serialization that does hold is the " +
		"DDL engine's lock on the catalog row itself. Pinning becomes correct when those three writers " +
		"move onto the guarded patch — not before.",
	"record_grant": "record_grant HAS a version column (migrations/core/0011) and no writer that could " +
		"ever bump it: a grant is created or revoked whole, with no update path at all, so a pin would " +
		"bind a value that cannot change for a live row. Deletion is what redemption must catch here, and " +
		"the target probe already answers not-found for a revoked row. This type is undecidable for a " +
		"separate and prior reason (undecidableConfirmFirstTypes) — nothing is ever redeemed against it.",
	"overlay_connection": "there is no `overlay_connection` table to pin: the connection row lives in " +
		"`incumbent_connection` (migrations/custom/20260716120000_overlay), under a different name and " +
		"with no version column, so approvals.targetVersion cannot even resolve a table for this target " +
		"type. connect/disconnect are whole-row transitions the diff_hash binds.",
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

// undecidableConfirmFirstTypes are the confirm-first target types whose staged
// row no human can act on, each with what it costs.
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
	"record_grant": "createRecordGrant/revokeRecordGrant, undecidable on BOTH halves of the " +
		"claim, and neither is fixable here. AUTHORITY: share_record resolves its decision grant " +
		"from the target's entity type, so deciding one demands `record_grant.update` — and " +
		"`record_grant` is no entry in identity's RBAC object vocabulary, so that grant is held by " +
		"no principal that can exist and the row is refused for everyone forever. Sharing is gated " +
		"by the manage-sharing permission instead, which is not an object grant at all. REDEMPTION: " +
		"even granted, share_record dead-ends (deadEndVerbs in agentpolicysynthesis_test.go — the " +
		"grant verbs reject any non-human principal, so an agent-staged, human-approved grant is " +
		"refused as the redeeming agent every time). Mapping share_record onto some other object, or " +
		"minting `record_grant` as one, is a product decision about whether an agent may propose " +
		"sharing at all; making the row merely decidable would leave an operation that still never " +
		"lands, which is worse than an honest dead end.",
}

// stagedRowDecidable answers the gate's WHOLE claim about one route's staged
// row: that a human can see it, and that the grants deciding it derives are ones
// a role document may actually hold. It returns the reason it failed, so the
// route names its own defect.
//
// Every half, because any one alone certifies a row nobody can release. A shape
// with no visibility rule is invisible in the inbox; a grant on an object outside
// identity's RBAC vocabulary is refused for every principal that can exist —
// which applies to the target-READ floor targetVisible composes above every arm
// exactly as it applies to the decision grants. Same dead row, reached from the
// sides of `decidable` — and a gate that proves one dimension reads green over
// the others.
func stagedRowDecidable(pol agentPolicy, hasTargetID bool) (bool, string) {
	recordType := string(pol.RecordType)
	if !approvals.TargetShapeDecidable(recordType, hasTargetID) {
		return false, "approvals.targetVisible has no rule for the shape it stages, so the row is " +
			"invisible in the inbox and undecidable at the decision"
	}
	if !identity.RBACObjectGrantable(recordType) {
		return false, "stages against a type outside the RBAC object vocabulary a role document may name, so " +
			"the target-read floor every visibility arm rides is satisfied by no principal that can exist"
	}
	objects, err := approvals.DecisionGrantObjects(pol.Tool, recordType)
	if err != nil {
		return false, "derives no decision grants (" + err.Error() + ")"
	}
	for _, object := range objects {
		if !identity.RBACObjectGrantable(object) {
			return false, "requires the decision grant " + object + ", which is outside the RBAC object " +
				"vocabulary a role document may name, so no principal that can exist may decide it"
		}
	}
	return true, ""
}

// Every confirm-first operation that names a concrete record type must stage a
// row a human can actually SEE and DECIDE.
//
// The read-side twin of the pin gate above, and it closes the same shape of hole
// one level further on: a pinned target nobody can see is still a zombie. The
// invariant is derived from the generated policy table rather than from a list of
// the types someone remembered, so a verb that becomes confirm-first upstream
// fails here until its staged shape is decidable or it carries a ratified reason.
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
		decidable, why := stagedRowDecidable(pol, strings.Contains(route, "{id}"))
		if decidable {
			continue
		}
		if _, ratified := undecidableConfirmFirstTypes[string(pol.RecordType)]; ratified {
			used[string(pol.RecordType)] = true
			continue
		}
		t.Errorf("%s (%s) stages against %q, which %s. No human could ever release or reject that row. "+
			"Give the type a visibility arm, map the decision onto a grantable object, or ratify the "+
			"residue in undecidableConfirmFirstTypes with what it costs.", route, pol.Op, pol.RecordType, why)
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
// that drifts: each is complete on its own terms, so neither module's own tests
// can see the disagreement. The assertion belongs in the composition layer
// because a module never imports a sibling and this layer imports both.
//
// THE SUBJECT SET IS THE UNION OF THREE SOURCES, and each is there because the
// others cannot see part of the invariant:
//
//   - every record type in the generated policy table — so a type the CONTRACT
//     adds is covered without anybody remembering to extend a list, and so the
//     gate cannot pass vacuously if both enumerators below went empty;
//   - every type the approvals inbox classifies — a target staged by a
//     server-side proposal flow rather than by an agent's call (an effective-dated
//     rate sheet) appears in NO agent policy, so the policy table alone reads
//     green over it;
//   - every type the fan-out classifies — the mirror direction, a type the
//     fan-out delivers on and the inbox strands.
//
// A gate whose subject set is narrower than the invariant it claims reads the
// wrong tree, which is quieter than reading it wrongly.
//
// WHAT IS DERIVED HERE, AND WHAT IS NOT. Two dimensions are derived from the
// union above: that both surfaces classify a type or neither does, and that a
// classified type is an RBAC object a role document may name — the second because
// BOTH surfaces gate a classified target on read of that type, so a type outside
// identity's vocabulary is a floor no principal can pass and a disclosure rule
// that silently means "never".
//
// What this layer canNOT derive is the CONTENT of each surface's floor. Both
// probes are package-internal on purpose (a module does not export its
// authorization internals so a test can drive them), so "do both actually require
// object-read for every type they classify" is not observable from here — which is
// exactly how the two agreed on the vocabulary while disagreeing on the floor.
// That half is gated inside each module, over each module's own classification
// table, by approvals.TestEveryClassifiedTargetTypeRequiresReadOnItsOwnType and
// webhooks.TestEveryClassifiedApprovalTargetRidesTheObjectReadFloor. Those two
// tests and this one are the whole invariant; none of the three is complete alone.
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
		if inbox != fanOut {
			known, missing := "the approvals inbox", "the webhook fan-out"
			if fanOut {
				known, missing = missing, known
			}
			t.Errorf("%s classifies target type %q and %s does not — give %s the arm that mirrors the "+
				"owning store's read rule, so a staged row is both decidable and announced",
				known, recordType, missing, missing)
			continue
		}
		if inbox && !identity.RBACObjectGrantable(recordType) {
			t.Errorf("both surfaces classify target type %q, which is outside the RBAC object vocabulary a role "+
				"document may name — each gates a classified target on read of its type, so no principal that "+
				"can exist may be shown or told about one", recordType)
		}
	}
	// The floor that keeps agreement from being vacuous: both classifications
	// answer false for everything when both are empty, which is agreement over
	// nothing.
	if len(subjects) == 0 {
		t.Fatal("the union of the policy table and both classifications is empty — the parity gate covers nothing")
	}
}
