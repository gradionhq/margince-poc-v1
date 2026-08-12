// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The REST door's half of the fourteen single-purpose commands
// (agentcommandsend.go, agentcommandlifecycle.go, agentcommandrecord.go,
// agentcommandauto.go): the derived coverage gate that pins all sixteen routes
// off the route walk, and the four places where this door's answer actually
// CHANGES — a merge that used to stage the wrong half, two enrich routes that
// could not be told apart, a booking whose approval bound to no record at all,
// and two sends that used to stage refusals no human could act on.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	chi "github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// singlePurposeTools are the fourteen verbs whose every contract operation
// this task put on the seam. Named as VERBS rather than as operationIds
// because the mapping is the point: two of them serve two operations each
// (merge_records is the person and organization halves, enrich is the two
// depths), so a walk keyed on tool names finds sixteen routes and would find a
// seventeenth the contract grew for any of them.
var singlePurposeTools = []string{
	"send_email", "send_message", "send_account_email", "book_meeting",
	"promote_lead", "disqualify_lead", "advance_project_phase", "advance_deal",
	"merge_records", "enrich",
	"log_activity", "draft_email", "relink_activity", "run_report",
}

// Every route these fourteen verbs reach must decode into a command, and the
// decoder bound to it must accept a well-formed request — a decoder that
// errors on the shape its own route produces would answer the whole operation
// as a refusal the moment a tier floor tightened it.
//
// Derived from agentPolicies rather than from a list of sixteen operationIds
// someone remembered, so a route added upstream for any of these verbs fails
// here rather than quietly falling back to the route walk.
func TestEverySinglePurposeToolRouteDecodesIntoACommand(t *testing.T) {
	verbs := make(map[string]bool, len(singlePurposeTools))
	for _, tool := range singlePurposeTools {
		verbs[tool] = true
	}
	checked := 0
	for route, pol := range agentPolicies {
		method, _, _ := strings.Cut(route, " ")
		if pol.Access != accessTool || !verbs[pol.Tool] || !mutatingMethod(method) {
			continue
		}
		checked++
		decode, described := restCommands[pol.Op]
		if !described {
			t.Errorf("%s (%s) is a %s operation that decodes into no command, so its staged target is still "+
				"guessed from the route while the tool door reads it from the call", route, pol.Op, pol.Tool)
			continue
		}
		call, err := decode(pol, restCommandDeps{records: seamRecord{}, channels: channelKinds{}}, syntheticOperandRequest(route, ids.NewV7()), nil)
		if err != nil {
			t.Errorf("%s (%s): decoding a well-formed request answered %v", route, pol.Op, err)
			continue
		}
		if call == nil {
			t.Errorf("%s (%s): decoded into no call at all", route, pol.Op)
		}
	}
	if checked != 16 {
		t.Errorf("the policy table carries %d mutating routes for these fourteen verbs, want 16 — if the "+
			"contract gained or lost one, this seam's coverage moved with it", checked)
	}
}

// mergeRequest is a POST against a merge route, carrying the {id} chi would
// have bound plus the body naming the survivor. body is the caller's own copy
// — the gate buffers it once and hands the same bytes to the decoder, so a
// request built with different bytes than the test passes on would prove
// nothing about the pair that actually travels together.
func mergeRequest(collection string, routed ids.UUID, body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, collection+"/"+routed.String()+"/merge", bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", routed.String())
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// The behaviour change a merge's command buys, and it is a correction rather
// than an addition.
//
// POST /v1/people/{id}/merge merges the ROUTED person INTO the body's
// target_id: the routed row is the one archived. The route walk read that
// routed id as the staged target, so the approval bound to — and the pin was
// taken from — the record about to be retired, while the tool door had always
// bound it to the survivor. One operation, two doors, two different rows.
func TestAMergeStagesTheSurvivorTheBodyNamesRatherThanTheRoutedRecord(t *testing.T) {
	for _, c := range []struct {
		op, collection string
		recordType     agentRecordType
	}{
		{"mergePerson", "/v1/people", recordTypePerson},
		{"mergeOrganization", "/v1/organizations", recordTypeOrganization},
	} {
		t.Run(c.op, func(t *testing.T) {
			source, survivor := ids.NewV7(), ids.NewV7()
			staging := &capturingApprovals{}
			pol := agentPolicy{Op: c.op, Access: accessTool, Tool: "merge_records", RecordType: c.recordType}
			body := []byte(`{"target_id":"` + survivor.String() + `"}`)

			stageRefusal(httptest.NewRecorder(), mergeRequest(c.collection, source, body), staging,
				restCommandDeps{records: seamRecord{}}, pol, body)

			if staging.last.TargetID != survivor {
				t.Errorf("staged target %s, want the survivor %s the body names — the routed id is the record "+
					"being archived, and an approval bound to it pins a row the merge retires",
					staging.last.TargetID, survivor)
			}
			if staging.last.TargetType != string(c.recordType) {
				t.Errorf("staged target type %q, want %q", staging.last.TargetType, c.recordType)
			}
		})
	}
}

// The two enrich routes are ONE verb at two depths, and nothing on the wire
// tells them apart — no `depth` field, no differing body, only which path was
// taken. So the decoders set it structurally, and the resolver's own summary is
// where that shows: a site crawl a resolver describes as a page read is an
// approval given for something else entirely.
//
// The SUMMARY rather than the inbox line, and the difference is worth stating.
// stagedTarget (agentgatestaging.go) takes only the TARGET from the resolver
// and writes restSummary as the line an approver reads on this door — that line
// names the concrete path, so a human here does see which read they are
// releasing. What the summary proves is that the erased command underneath
// carries the right depth at all, which is the only handle this door has on it.
func TestTheTwoEnrichRoutesStageTheDepthTheirOwnRouteMeans(t *testing.T) {
	staged := map[string]string{}
	for _, c := range []struct{ op, path string }{
		{"scrapeCompany", "/v1/organizations/%s/enrich"},
		{"deepReadCompany", "/v1/organizations/%s/deep-read"},
	} {
		org := ids.NewV7()
		pol := agentPolicy{Op: c.op, Access: accessTool, Tool: "enrich", RecordType: recordTypeOrganization}
		req := httptest.NewRequest(http.MethodPost, strings.Replace(c.path, "%s", org.String(), 1), nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", org.String())
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		decode := restCommands[c.op]
		call, err := decode(pol, restCommandDeps{records: seamRecord{}}, req, nil)
		if err != nil {
			t.Fatalf("%s: decoding answered %v", c.op, err)
		}
		info, err := agents.StageSubject(req.Context(), call)
		if err != nil {
			t.Fatalf("%s: staging answered %v", c.op, err)
		}
		staged[c.op] = info.Summary
	}

	if staged["scrapeCompany"] == staged["deepReadCompany"] {
		t.Fatalf("both enrich routes stage the sentence %q — a human releasing a whole-site crawl would "+
			"read the line written for a single page", staged["scrapeCompany"])
	}
	if !strings.Contains(staged["scrapeCompany"], string(agents.EnrichDepthPage)) {
		t.Errorf("scrapeCompany staged %q, which does not say it reads one page", staged["scrapeCompany"])
	}
	if !strings.Contains(staged["deepReadCompany"], string(agents.EnrichDepthSite)) {
		t.Errorf("deepReadCompany staged %q, which does not say it reads the whole site", staged["deepReadCompany"])
	}
}

// A booking's route carries no {id}, so the walk could only stage the policy
// table's declared record type against the ZERO id — an approval floating free
// of any record, which the approvals surface can scope to nobody in particular.
// The command binds it to the first record the booking attaches to: the row a
// meeting is a commitment ON, and whose scope the deciding human must reach.
func TestABookingStagesTheRecordItAttachesTo(t *testing.T) {
	deal := ids.NewV7()
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "bookMeeting", Access: accessTool, Tool: "book_meeting", RecordType: recordTypeActivity}
	body := []byte(`{"start":"2026-08-10T09:00:00Z","end":"2026-08-10T09:30:00Z","subject":"Review",` +
		`"links":[{"entity_type":"deal","entity_id":"` + deal.String() + `"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/bookings", bytes.NewReader(body))

	stageRefusal(httptest.NewRecorder(), req, staging, restCommandDeps{records: seamRecord{}}, pol, body)

	if staging.last.TargetID != deal {
		t.Errorf("staged target %s, want the deal %s the booking attaches to — an approval bound to no row "+
			"is one the approvals surface cannot scope to anybody", staging.last.TargetID, deal)
	}
	if staging.last.TargetType != string(datasource.EntityDeal) {
		t.Errorf("staged target type %q, want %q — the type comes from the link the booking names, not from "+
			"the route's own declared record type", staging.last.TargetType, datasource.EntityDeal)
	}
}

// The behaviour change the outbound commands buy: a refusal the tool door has
// always made, now made by this door too.
//
// A send with an empty `to` reaches nobody and the store refuses it. Without
// the command, this door staged it anyway — a human read a send addressed to
// no one, approved it, and the approved retry spent the one-shot authority
// discovering what could have been said first.
func TestASendWithNoAddresseeStagesNothing(t *testing.T) {
	anchor := ids.NewV7()
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "sendEmail", Access: accessTool, Tool: "send_email", RecordType: recordTypeActivity}
	body := []byte(`{"to":[],"subject":"Q3","body":"hi","consent_purpose":"sales"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/activities/"+anchor.String()+"/send-email", bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", anchor.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	stageRefusal(rec, req, staging, restCommandDeps{records: seamRecord{}}, pol, body)

	if staging.last.Tool != "" {
		t.Errorf("an approval was staged for %q against a send that reaches nobody", staging.last.Tool)
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a send with no addressee answered %d, want %d — the caller is told what to fix instead of "+
			"being handed an approval nobody can act on", rec.Code, http.StatusUnprocessableEntity)
	}
}

// The channel guard is the one refusal this door could not previously make at
// all: it needs the ANCHOR's kind, which the route does not carry. The command
// carries the question instead (restCommandDeps.channels), so a reply proposed
// on an email thread is refused before a human is asked to release it.
func TestAChannelReplyOnANonChannelAnchorStagesNothing(t *testing.T) {
	anchor := ids.NewV7()
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "sendMessage", Access: accessTool, Tool: "send_message", RecordType: recordTypeActivity}
	body := []byte(`{"body":"hello","consent_purpose":"support"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/activities/"+anchor.String()+"/send-message", bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", anchor.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	// seamRecord answers every read with an activity whose fields carry no
	// `kind` at all, which is not a channel kind — the same answer an email
	// anchor gives, reached without a second fixture.
	stageRefusal(rec, req, staging, restCommandDeps{records: seamRecord{}, channels: channelKinds{}}, pol, body)

	if staging.last.Tool != "" {
		t.Errorf("an approval was staged for %q against an anchor no channel reply can transmit through",
			staging.last.Tool)
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a reply on a non-channel anchor answered %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
}
