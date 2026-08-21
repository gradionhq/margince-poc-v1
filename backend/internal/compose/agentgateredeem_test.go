// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The auto-executed arm's half of the approval loop
// (gradionhq/margince/backend — gradionhq/margince-poc-v1#812).
//
// A staged 🟡 call can resolve 🟢 on its retry: the record moved, or the
// per-field split staged an otherwise auto-execute patch. The token that retry
// presents is an assertion of authority, and this door used to read it for
// update_record alone — so for every other tool the approval stayed pending in a
// human's inbox for work that had already happened, and an invalid or replayed
// token was accepted by being ignored.
//
// Admission runs through the real gate and the real tier input, so what these
// assert is what the middleware produces rather than what a hand-set context
// claims.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// countingRedeemer is the approvals engine as this door uses it: it records what
// each redemption was asked about, so a test can say the token was validated
// against THIS call rather than merely consumed.
type countingRedeemer struct {
	pin      int64
	pinned   bool
	err      error
	redeemed int
	tools    []string
	hashes   []string
}

// StageQuotaRelease satisfies the seam; a step-up never reaches these tests.
func (*countingRedeemer) StageQuotaRelease(context.Context, agents.QuotaReleaseRequest) (ids.ApprovalID, bool, error) {
	return ids.ApprovalID{}, false, nil
}

func (*countingRedeemer) StageCall(context.Context, agents.StageRequest) (ids.ApprovalID, bool, error) {
	return ids.New[ids.ApprovalKind](), false, nil
}

func (c *countingRedeemer) Redeem(_ context.Context, _ ids.ApprovalID, tool, diffHash string) (int64, bool, error) {
	c.redeemed++
	c.tools = append(c.tools, tool)
	c.hashes = append(c.hashes, diffHash)
	if c.err != nil {
		return 0, false, c.err
	}
	return c.pin, c.pinned, nil
}

// sawRequest is what the handler behind the door was handed, or that it was
// never reached at all. The released marker is read through the seam's own
// exported probe — the same answer the egress backstop asks for — so a pin
// forwarded without the marker, or the reverse, is visible here.
type sawRequest struct {
	ran      bool
	ifMatch  string
	released bool
}

// probeCounter records whether the per-field ownership split ran. A redeemed
// retry must not be re-split: it carries exactly the sub-patch a human released,
// and probing it again would stage a second approval for the overwrite that was
// just approved.
type probeCounter struct{ probes int }

func (p *probeCounter) HumanOwnedConflicts(context.Context, string, ids.UUID, json.RawMessage) ([]string, error) {
	p.probes++
	return nil, nil
}

// autoExecutedMove runs one open→open deal move — a move the tier gate admits
// 🟢, reading the deal at version 12 — carrying the token and the caller
// If-Match given, and answers what the handler saw plus the status the door
// replied with.
func autoExecutedMove(t *testing.T, staging agents.Approvals, token, callerIfMatch string) (sawRequest, int) {
	t.Helper()
	deal, stage := ids.NewV7(), ids.NewV7()
	deps := restCommandDeps{
		stages:  reopenStages{semantics: map[ids.UUID]string{stage: "open"}},
		records: versionedDeal{stageID: stage, version: 12},
	}
	body := []byte(`{"to_stage_id":"` + stage.String() + `"}`)
	r := requestForDeal(t, deal)
	if token != "" {
		r.Header.Set(approvalTokenHeader, token)
	}
	if callerIfMatch != "" {
		r.Header.Set("If-Match", callerIfMatch)
	}
	r = r.WithContext(agentRequestCtx(r.Context()))

	reg, spec := advanceSpec(t, deps)
	pol := agentPolicies["POST /v1/deals/{id}/advance"]
	ctx, err := auth.NewGate(fullSeat{}).Admit(r.Context(), spec, tierInput(r.Context(), spec, pol, deps, r, body))
	if err != nil {
		t.Fatalf("the open→open move was refused before its token was ever read: %v", err)
	}
	r = r.WithContext(ctx)

	var saw sawRequest
	next := http.HandlerFunc(func(_ http.ResponseWriter, got *http.Request) {
		saw = sawRequest{ran: true, ifMatch: got.Header.Get("If-Match"), released: agents.ApprovalRedeemed(got.Context())}
	})
	recorder := httptest.NewRecorder()
	admitAgentCall(recorder, r, next, admissionOutcome{
		staging: staging, pol: pol, body: body, commands: deps, spec: spec, registry: reg,
	})
	return saw, recorder.Code
}

// A valid token on an auto-executed call is CONSUMED, and consumed against this
// exact call: the approval it names stops being a pending question in a human's
// inbox about work that has already happened.
func TestAnAutoExecutedCallConsumesThePresentedToken(t *testing.T) {
	staging := &countingRedeemer{pin: 12, pinned: true}

	saw, status := autoExecutedMove(t, staging, ids.New[ids.ApprovalKind]().String(), "")

	if staging.redeemed != 1 {
		t.Fatalf("the token was redeemed %d times, want exactly once — an unread token leaves the "+
			"approval pending for work that already happened", staging.redeemed)
	}
	if staging.tools[0] != "advance_deal" || staging.hashes[0] == "" {
		t.Errorf("redeemed against tool %q and hash %q — a token must be validated against the call "+
			"presenting it, not merely spent", staging.tools[0], staging.hashes[0])
	}
	if !saw.ran || status != http.StatusOK {
		t.Fatalf("the released call ran=%v at status %d, want it through at 200", saw.ran, status)
	}
	if !saw.released {
		t.Error("the handler ran without the released marker — the seam's egress backstop reads exactly that")
	}
}

// An asserted authority that does not hold is REFUSED, never ignored. All three
// failures are one answer: nothing ran, and the caller is told why.
func TestAnAutoExecutedCallRefusesATokenThatDoesNotHold(t *testing.T) {
	for name, tc := range map[string]struct {
		staging agents.Approvals
		token   string
	}{
		"a token that is not an approval id": {staging: &countingRedeemer{}, token: "not-an-approval-id"},
		"a replayed or expired approval": {
			staging: &countingRedeemer{err: apperrors.ErrApprovalTokenInvalid},
			token:   ids.New[ids.ApprovalKind]().String(),
		},
		"a surface with no approvals engine": {staging: nil, token: ids.New[ids.ApprovalKind]().String()},
	} {
		t.Run(name, func(t *testing.T) {
			saw, status := autoExecutedMove(t, tc.staging, tc.token, "")

			if status != http.StatusForbidden {
				t.Errorf("status = %d, want 403 — an assertion of authority that does not hold must be "+
					"answered, not stepped over", status)
			}
			if saw.ran {
				t.Error("the handler ran anyway, which is the call being admitted with its own asserted " +
					"authority left unread")
			}
		})
	}
}

// A redeemed call takes the SERVER's pin over anything the caller sent, and is
// never refused for the disagreement.
//
// The refusal this replaced looked right and was not: consumePresentedToken has
// already spent the approval by the time the pin is decided, so a 409 here
// destroys a human's one-shot yes on a call that never ran and can never be
// redeemed again — while the 🟡 arm accepts the identical call. Forwarding
// concedes nothing, because the store re-checks the pin inside the transaction
// that mutates and refuses there unless the row is at the approved version.
func TestAReleasedRetryTakesTheServersPinOverTheCallersIfMatch(t *testing.T) {
	staging := &countingRedeemer{pin: 12, pinned: true}

	saw, status := autoExecutedMove(t, staging, ids.New[ids.ApprovalKind]().String(), "4")

	if status != http.StatusOK || !saw.ran {
		t.Fatalf("the released retry answered %d and ran=%v — refusing it burns the approval it just "+
			"consumed on a call that never happened", status, saw.ran)
	}
	if saw.ifMatch != "12" {
		t.Errorf("forwarded If-Match = %q, want \"12\" — the caller's header is a version nothing proved, "+
			"and this door hashes none into what the human approved", saw.ifMatch)
	}
}

// The released pin and the pin the tier gate read disagreeing is a DEFENSIVE
// assertion, not a case this door meets.
//
// Through the real stager it is unreachable: approvals pins server-side only for
// a concrete, version-checkable target, versions are monotonic, and the gate's
// read precedes the redemption's — so admitted ≤ current = released, and a row
// that really moved fails validateRedemptionTarget inside the redemption first.
// This reaches the branch only because countingRedeemer answers a pin without
// the target re-check production performs. Whether refusing is even the right
// answer for a disagreement both doors could only discover after the approval is
// consumed is gradionhq/margince-poc-v1#1069; what this pins meanwhile is that
// the branch is live and fails closed rather than picking a version.
func TestADisagreementBetweenTheTwoServerPinsFailsClosed(t *testing.T) {
	// versionedDeal reports 12 to the tier gate; the approval was granted at 9.
	staging := &countingRedeemer{pin: 9, pinned: true}

	saw, status := autoExecutedMove(t, staging, ids.New[ids.ApprovalKind]().String(), "")

	if status != http.StatusConflict {
		t.Errorf("status = %d, want 409 — picking either version would run the write against a state "+
			"nothing authorized", status)
	}
	if saw.ran {
		t.Errorf("the handler ran with If-Match %q despite the two pins naming different records", saw.ifMatch)
	}
}

// Where the gate read no record — a static auto-execute tier — the approval's
// own pin is what conditions the write, and the redeemed retry goes straight to
// the handler: it carries exactly the sub-patch a human released, so re-running
// the ownership split over it would stage a second approval for the overwrite
// just approved.
func TestAReleasedRetryOnAStaticTierIsPinnedAndNotResplit(t *testing.T) {
	person := ids.NewV7()
	pol := agentPolicies["PATCH /v1/people/{id}"]
	if pol.Tier != tierAutoExecute || pol.Tool != "update_record" {
		t.Fatalf("PATCH /v1/people/{id} is %q on %s — this case is about a redeemed retry of the split", pol.Tier, pol.Tool)
	}
	body := []byte(`{"display_name":"Ada"}`)
	r := httptest.NewRequest(http.MethodPatch, "/v1/people/"+person.String(), http.NoBody)
	r.Header.Set(approvalTokenHeader, ids.New[ids.ApprovalKind]().String())
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", person.String())
	r = r.WithContext(context.WithValue(agentRequestCtx(r.Context()), chi.RouteCtxKey, routeCtx))

	var saw sawRequest
	next := http.HandlerFunc(func(_ http.ResponseWriter, got *http.Request) {
		saw = sawRequest{ran: true, ifMatch: got.Header.Get("If-Match"), released: agents.ApprovalRedeemed(got.Context())}
	})
	ownership := &probeCounter{}
	reg := agents.NewRegistry(nil, auth.NewGate(fullSeat{}))
	agents.RegisterCoreTools(reg, seamRecord{}, nil, nil, nil)
	spec, _, ok := operationSpec(pol, reg)
	if !ok {
		t.Fatal("the registry serves no update_record spec for the REST twin to admit against")
	}
	admitAgentCall(httptest.NewRecorder(), r, next, admissionOutcome{
		staging: &countingRedeemer{pin: 7, pinned: true}, ownership: ownership,
		pol: pol, body: body, commands: restCommandDeps{records: seamRecord{}},
		spec: spec, registry: reg,
	})

	if !saw.ran || !saw.released {
		t.Fatalf("the released patch ran=%v with the marker=%v, want both", saw.ran, saw.released)
	}
	if saw.ifMatch != "7" {
		t.Errorf("forwarded If-Match = %q, want \"7\" — with no version read at admission the approval's "+
			"own pin is the only one anything proved", saw.ifMatch)
	}
	if ownership.probes != 0 {
		t.Errorf("the ownership split ran %d times on a redeemed retry — it would stage a second approval "+
			"for the overwrite a human just approved", ownership.probes)
	}
}

// countingCharges is the quota meter as the REST door uses it: it only ever
// receives charges, and keeps them per counter.
type countingCharges struct{ spent map[agentquota.Counter]int }

func (c *countingCharges) Consume(_ context.Context, counter agentquota.Counter, n int) error {
	c.spent[counter] += n
	return nil
}

// stagedDeal is a deal the record seam serves as its OWN — authoritative, at a
// known version, sitting in a known stage. Authority is stamped because
// refuseStagingElsewhere reads exactly that flag: an unstamped fixture would be
// refused before the 🟡 arm this exercises was ever reached.
type stagedDeal struct {
	datasource.SystemOfRecordProvider
	stageID ids.UUID
	version int64
}

func (p stagedDeal) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	return datasource.Record{
		Ref:       ref,
		Fields:    json.RawMessage(`{"stage_id":"` + p.stageID.String() + `","name":"Acme"}`),
		Version:   p.version,
		Freshness: datasource.FreshnessInfo{Authoritative: true},
	}, nil
}

// gateCallCharges runs ONE request through the whole middleware — the only place
// both call-charge points are reachable — and answers what it spent against the
// call ceiling, plus the status.
//
// sourceSemantic decides the arm: an open source is the 🟢 move, a won one is
// the 🟡 refusal, and the tool, the route and the policy are identical either
// way. That is the point: the claim is one charge per CALL, not one per arm.
func gateCallCharges(t *testing.T, sourceSemantic, token string, redeemErr error) (int, int) {
	t.Helper()
	deal, current, target := ids.NewV7(), ids.NewV7(), ids.NewV7()
	stages := reopenStages{semantics: map[ids.UUID]string{current: sourceSemantic, target: "open"}}
	var records datasource.SystemOfRecordProvider = stagedDeal{stageID: current, version: 12}
	charger := &countingCharges{spent: map[agentquota.Counter]int{}}

	reg := agents.NewRegistry(nil, auth.NewGate(fullSeat{}), agents.WithQuotaCharger(charger))
	agents.RegisterCoreTools(reg, records, stages, nil, nil)

	r := httptest.NewRequest(http.MethodPost, "/v1/deals/"+deal.String()+"/advance",
		strings.NewReader(`{"to_stage_id":"`+target.String()+`"}`))
	if token != "" {
		r.Header.Set(approvalTokenHeader, token)
	}
	routeCtx := chi.NewRouteContext()
	routeCtx.RoutePatterns = []string{"/v1/deals/{id}/advance"}
	routeCtx.URLParams.Add("id", deal.String())
	r = r.WithContext(context.WithValue(agentRequestCtx(r.Context()), chi.RouteCtxKey, routeCtx))

	staging := &countingRedeemer{pin: 12, pinned: true, err: redeemErr}
	recorder := httptest.NewRecorder()
	gate := agentGate(reg, staging, stages, records, nil, nil, auth.NewGate(fullSeat{}))
	gate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(recorder, r)

	return charger.spent[agentquota.Calls], recorder.Code
}

// The call ceiling counts CALLS THAT RAN: one apiece whichever arm carried them,
// and none at all for a call that ran nothing.
//
// The last row is the one that costs something to get wrong. Exhausting
// MCP-SESS-CALLS suspends the Passport for the window, so charging a call whose
// token opened nothing lets an agent looping one consumed token spend its own
// credential's whole day — on REST only, since the MCP door refuses to count
// exactly that (registry.go: "a replayed token that opens nothing"). Charging
// the redemption on top of the admission would bill a 🟢 retry twice for one
// act; leaving the 🟡 redemption uncharged would leave the arm that redeems free.
func TestOneRestCallSpendsOneOfTheCallCeiling(t *testing.T) {
	for name, tc := range map[string]struct {
		source, token string
		redeemErr     error
		wantCalls     int
		wantStatus    int
	}{
		"auto-executed, no token": {source: "open", wantCalls: 1, wantStatus: http.StatusOK},
		"auto-executed, redeeming a token": {
			source: "open", token: ids.New[ids.ApprovalKind]().String(), wantCalls: 1, wantStatus: http.StatusOK,
		},
		"confirm-first, redeeming a token": {
			source: "won", token: ids.New[ids.ApprovalKind]().String(), wantCalls: 1, wantStatus: http.StatusOK,
		},
		"confirm-first, staged and never run": {source: "won", wantCalls: 0, wantStatus: http.StatusForbidden},
		"auto-executed, presenting a token that opens nothing": {
			source: "open", token: ids.New[ids.ApprovalKind]().String(),
			redeemErr: apperrors.ErrApprovalTokenInvalid, wantCalls: 0, wantStatus: http.StatusForbidden,
		},
		"confirm-first, presenting a token that opens nothing": {
			source: "won", token: ids.New[ids.ApprovalKind]().String(),
			redeemErr: apperrors.ErrApprovalTokenInvalid, wantCalls: 0, wantStatus: http.StatusForbidden,
		},
	} {
		t.Run(name, func(t *testing.T) {
			calls, status := gateCallCharges(t, tc.source, tc.token, tc.redeemErr)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d — this case is not the arm it names", status, tc.wantStatus)
			}
			if calls != tc.wantCalls {
				t.Errorf("the call ceiling was charged %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}
