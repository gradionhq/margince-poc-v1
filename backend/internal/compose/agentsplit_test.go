// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The buffered response the split gate replays through. What matters here is
// the shape of what reaches the wire: a client of this surface parses RFC 7807
// on every refusal, so one path out of the handler answering something else is
// a client that cannot read its own error.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// bufferedFor builds a buffered response already holding a status and headers,
// as it would after the wrapped handler answered.
func bufferedFor(status string) *bufferedResponse {
	b := newBufferedResponse()
	b.header.Set("Content-Type", "application/json")
	b.header.Set("X-Recorded", status)
	return b
}

func TestFlushJSONReplacesTheBodyAndItsContentLength(t *testing.T) {
	b := bufferedFor("ok")
	b.WriteHeader(http.StatusOK)
	if _, err := b.Write([]byte(`{"id":"1"}`)); err != nil {
		t.Fatalf("buffering the original body: %v", err)
	}
	rec := httptest.NewRecorder()

	b.flushJSON(rec, httptest.NewRequest(http.MethodPatch, "/v1/deals/1", nil),
		map[string]any{"id": "1", "staged_approval": map[string]any{"approval_id": "a1"}})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want the buffered 200", rec.Code)
	}
	if got := rec.Header().Get("X-Recorded"); got != "ok" {
		t.Errorf("the buffered headers were dropped: X-Recorded = %q", got)
	}
	body := rec.Body.String()
	// The Content-Length of the ORIGINAL body no longer applies, and a stale
	// one truncates the record for every client that honours it.
	if got, want := rec.Header().Get("Content-Length"), len(body); got != "" && got != itoa(want) {
		t.Errorf("Content-Length = %q, want %d — the length of what was actually written", got, want)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("the replayed body is not JSON: %v (%s)", err, body)
	}
	if _, staged := decoded["staged_approval"]; !staged {
		t.Errorf("the staging note was not spliced into the replayed record: %s", body)
	}
}

// unmarshalable is a payload value encoding/json refuses. A map decoded from
// JSON cannot hold one in production — which is exactly why the branch needs a
// test: it is unreachable by the handler and would otherwise never run.
type unmarshalable struct{}

func (unmarshalable) MarshalJSON() ([]byte, error) { return nil, errNotEncodable }

var errNotEncodable = &json.UnsupportedValueError{Str: "deliberately unencodable"}

// TestFlushJSONAnswersAMarshalFailureAsAProblemDocument — the staging already
// exists at this point, so the client must be told the request failed rather
// than handed a truncated record. Through httperr like every other refusal on
// this surface: a text/plain body here is one a client parsing the contract's
// error shape cannot read, on the path it needs most.
func TestFlushJSONAnswersAMarshalFailureAsAProblemDocument(t *testing.T) {
	b := bufferedFor("ok")
	b.WriteHeader(http.StatusOK)
	rec := httptest.NewRecorder()

	b.flushJSON(rec, httptest.NewRequest(http.MethodPatch, "/v1/deals/1", nil),
		map[string]any{"boom": unmarshalable{}})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — the staging exists and the record could not be built", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "problem+json") {
		t.Errorf("Content-Type = %q, want an RFC 7807 problem document", ct)
	}
	// The marshal error names Go internals; it belongs in the server log.
	if strings.Contains(rec.Body.String(), "unencodable") {
		t.Errorf("the marshal error leaked to the client: %s", rec.Body.String())
	}
}

// itoa keeps the length comparison above readable without pulling strconv into
// a file that needs it once.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// allHumanOwned answers the ownership probe by naming every field the patch
// touches — the shape that leaves SplitHumanOwned's AutoExecute half empty,
// so splitHumanOwnedUpdate's terminal branch (agentsplit.go: "every touched
// field is human-owned") is the one under test rather than the mixed one.
type allHumanOwned struct{}

func (allHumanOwned) HumanOwnedConflicts(_ context.Context, _ string, _ ids.UUID, patch json.RawMessage) ([]string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(patch, &fields); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	return names, nil
}

// The split's all-human-owned branch resolves through the SAME command seam
// every other registered patch does (agentsplit.go's own comment on this
// branch states why), so it owes the record the same refusal
// refuseStagingElsewhere gives every other stager: an approval against a
// target whose authority lives elsewhere could never be redeemed, since
// redemption's version pin reads our own tables. Before that registration
// this branch ran no such check at all — an agent patching a mirrored deal
// with every field human-owned got a staged approval instead.
func TestSplitAllHumanOwnedRefusesAnExternallyHeldRecord(t *testing.T) {
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "updatePerson", Access: accessTool, Tool: "update_record", RecordType: recordTypePerson}
	personID := ids.NewV7()
	body := []byte(`{"full_name":"Overwritten"}`)

	req := patchRequest("/v1/people", personID, body)
	rec := httptest.NewRecorder()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("the handler ran — every field was human-owned, so nothing should have auto-executed")
	})

	splitHumanOwnedUpdate(rec, req, next,
		splitUpdateDeps{staging: staging, commands: restCommandDeps{records: mirroredRecord{}}, ownership: allHumanOwned{}},
		pol, body)

	if staging.last.Tool != "" {
		t.Errorf("an approval was staged for %q against a record whose authority lives elsewhere — "+
			"nobody could ever release it", staging.last.Tool)
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("an externally-held target answered %d, want %d (unsupported_by_sor) — the refusal a "+
			"caller gets must name why the patch cannot be governed here", rec.Code, http.StatusUnprocessableEntity)
	}
}

// TestUpsertPartnerStagesAHumanOwnedPartnerField pins actionShapedUpdateOps'
// own claim about upsertPartner (its comment, above the map): the resolver
// maps partner→organization, so this patch IS a field patch on the routed
// organization, and an agent overwriting a human-typed partner field
// (cert_status here) has to STAGE, the same §2.1 precedence protection
// every ordinary organization field patch gets — not silently apply, which
// is what would happen if upsertPartner were ever added to the map.
//
// Run through admitAgentCall — the layer that actually reads
// actionShapedUpdateOps to decide between splitHumanOwnedUpdate and a bare
// next.ServeHTTP (agentgate.go) — rather than splitHumanOwnedUpdate directly,
// so this test is sensitive to the membership question itself, not only to
// splitHumanOwnedUpdate's own internals. Verified by hand: adding
// `opUpsertPartner: true` back into actionShapedUpdateOps and rerunning
// this test fails it — the call takes the bare next.ServeHTTP branch
// instead, the handler runs, and nothing stages.
func TestUpsertPartnerStagesAHumanOwnedPartnerField(t *testing.T) {
	orgID := ids.NewV7()
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "upsertPartner", Access: accessTool, Tool: "update_record", RecordType: recordTypePartner}
	body := []byte(`{"cert_status":"certified"}`)
	req := operandRequest(http.MethodPut, "/v1/organizations", orgID.String(), "", "", body)
	rec := httptest.NewRecorder()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("the handler ran — cert_status is human-owned, so the write must stage, not apply")
	})

	admitAgentCall(rec, req, next, admissionOutcome{
		staging: staging, ownership: allHumanOwned{},
		commands: restCommandDeps{records: seamRecord{}}, pol: pol, body: body,
	})

	if staging.last.TargetType != "organization" || staging.last.TargetID != orgID {
		t.Fatalf("staged target = (%s,%s), want (organization,%s) — the resolver maps partner→organization, "+
			"so the approval binds to the org whose field this patch actually sets",
			staging.last.TargetType, staging.last.TargetID, orgID)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (approval_required) — a human-owned field applied silently is the "+
			"§2.1 precedence protection this test exists to hold, and an unclassified 500 must not read "+
			"as that refusal", rec.Code, http.StatusForbidden)
	}
}

// mixedHumanOwned answers the ownership probe by naming only ONE field as
// human-owned, leaving any other touched field to auto-execute — the shape
// that sends splitHumanOwnedUpdate down applyAutoExecuteAndStageResidue's
// residue path (split.AutoExecute != nil) rather than allHumanOwned's
// all-refused terminal branch.
type mixedHumanOwned struct{ conflict string }

func (m mixedHumanOwned) HumanOwnedConflicts(context.Context, string, ids.UUID, json.RawMessage) ([]string, error) {
	return []string{m.conflict}, nil
}

// TestUpsertPartnerResidueStagesTheOrganizationNotPartner pins the third
// staging call site: applyAutoExecuteAndStageResidue builds its StageRequest
// through the resolver seam (stagedTarget) rather than straight off
// pol.RecordType ("partner" for this op) and the routed id, which is what the
// two call sites TestUpsertPartnerStagesAHumanOwnedPartnerField covers already
// do. "partner" has no rule in either
// targetProbes or existenceProbes (approvals/targetvisibility.go), so a
// row staged under it fails closed — invisible in the inbox, undecidable
// at the decision — the zombie authority object this whole seam exists to
// prevent, for the exact write §2.1 is supposed to protect.
//
// A MIXED patch (one human-owned field, one agent-owned) is what reaches
// the residue path rather than the all-human-owned terminal branch, so
// this drives one through admitAgentCall — the same layer
// TestUpsertPartnerStagesAHumanOwnedPartnerField uses — with an
// auto-execute handler that answers the shape a real 200 record has.
// Verified to fail against the pre-fix residue path: it staged
// target_type "partner" instead of "organization".
func TestUpsertPartnerResidueStagesTheOrganizationNotPartner(t *testing.T) {
	orgID := ids.NewV7()
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "upsertPartner", Access: accessTool, Tool: "update_record", RecordType: recordTypePartner}
	body := []byte(`{"cert_status":"certified","margin_tier":"tier1_15"}`)
	req := operandRequest(http.MethodPut, "/v1/organizations", orgID.String(), "", "", body)
	rec := httptest.NewRecorder()
	// The stand-in for the real UpsertPartner handler: applies the
	// agent-owned half and answers 200 with a record body, the shape
	// applyAutoExecuteAndStageResidue decodes to read the post-write
	// version and splice the staging note into.
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"organization_id":"` + orgID.String() + `","margin_tier":"tier1_15","version":3}`))
	})
	reg := agents.NewRegistry(nil, auth.NewGate(fullSeat{}))

	admitAgentCall(rec, req, next, admissionOutcome{
		staging: staging, ownership: mixedHumanOwned{conflict: "cert_status"},
		commands: restCommandDeps{records: seamRecord{}}, pol: pol, body: body,
		registry: reg,
	})

	if staging.last.TargetType != "organization" || staging.last.TargetID != orgID {
		t.Fatalf("residue staged target = (%s,%s), want (organization,%s) — the residue path must "+
			"resolve through the same seam the all-human-owned branch does, not off pol.RecordType "+
			"directly", staging.last.TargetType, staging.last.TargetID, orgID)
	}
}

// A refusal describes a request that changed nothing
// (gradionhq/margince-poc-v1#1073). The residue path's own refusals — the
// resolver's Guards among them — are settled BEFORE the auto-execute half is
// dispatched, so a target this door will not stage against costs the caller a
// retry rather than leaving half a patch committed under a 4xx that says the
// change was refused.
//
// Driven with the mirrored record the all-human-owned branch uses
// (TestSplitAllHumanOwnedRefusesAnExternallyHeldRecord) and the MIXED ownership
// that reaches the residue path: before the ordering fix the handler ran first,
// so this same call answered 422 with the agent-owned half already written.
func TestTheResiduePathRefusesAnExternallyHeldRecordBeforeAnythingIsWritten(t *testing.T) {
	orgID := ids.NewV7()
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "upsertPartner", Access: accessTool, Tool: "update_record", RecordType: recordTypePartner}
	body := []byte(`{"cert_status":"certified","margin_tier":"tier1_15"}`)
	req := operandRequest(http.MethodPut, "/v1/organizations", orgID.String(), "", "", body)
	rec := httptest.NewRecorder()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the handler ran — the agent-owned half was written for a call this door then refused, " +
			"which is the partial write a refusal must never describe")
	})

	admitAgentCall(rec, req, next, admissionOutcome{
		staging: staging, ownership: mixedHumanOwned{conflict: "cert_status"},
		commands: restCommandDeps{records: mirroredRecord{}}, pol: pol, body: body,
		registry: agents.NewRegistry(nil, auth.NewGate(fullSeat{})),
	})

	if staging.last.Tool != "" {
		t.Errorf("an approval was staged for %q against a record whose authority lives elsewhere — "+
			"nobody could ever release it", staging.last.Tool)
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("an externally-held target answered %d, want %d (unsupported_by_sor)", rec.Code,
			http.StatusUnprocessableEntity)
	}
}

// A residue staged under an Idempotency-Key must be redeemable by the retry
// the staging note actually instructs.
//
// This is the whole of the defect. The auto-execute half of a mixed patch
// answers 2xx under the caller's key, and only a 2xx settles an idempotency
// claim — so that key is spent, permanently. The retry the note asks for
// ("repeat this request with ONLY those fields and the X-Approval-Token
// header") therefore cannot present it: re-using it never even reaches the
// gate, because the idempotency middleware sits outside it and answers the
// residue body as a digest mismatch. Hashing the key into the residue's
// identity made the only retry that CAN arrive unable to match, so the human's
// approval was void and the withheld field could never be written.
//
// Asserted as the redemption side computes it, not as a property of the
// canonicalization alone: redeemIfPresented hashes the retry with
// keyBindsTheRetry — it cannot know which kind of approval it is about to
// redeem — so what has to agree is the STAGED hash and the hash of a keyless
// retry carrying the same residue.
func TestAResidueStagedUnderAnIdempotencyKeyIsRedeemableByItsRetry(t *testing.T) {
	orgID := ids.NewV7()
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "upsertPartner", Access: accessTool, Tool: "update_record", RecordType: recordTypePartner}
	body := []byte(`{"cert_status":"certified","margin_tier":"tier1_15"}`)
	req := operandRequest(http.MethodPut, "/v1/organizations", orgID.String(), "", "", body)
	req.Header.Set(idempotencyKeyHeader, "01J0-agent-retry-key")
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"organization_id":"` + orgID.String() + `","margin_tier":"tier1_15","version":3}`))
	})

	admitAgentCall(httptest.NewRecorder(), req, next, admissionOutcome{
		staging: staging, ownership: mixedHumanOwned{conflict: "cert_status"},
		commands: restCommandDeps{records: seamRecord{}}, pol: pol, body: body,
		registry: agents.NewRegistry(nil, auth.NewGate(fullSeat{})),
	})
	if staging.last.DiffHash == "" {
		t.Fatal("nothing was staged, so there is no residue identity to redeem")
	}

	// The retry the staging note instructs: the withheld fields alone, the
	// approval token, and NO idempotency key — the original is settled and a
	// fresh one would be a different call.
	retry := operandRequest(http.MethodPut, "/v1/organizations", orgID.String(), "", "",
		[]byte(staging.last.ProposedChange))
	_, retryHash, err := canonicalRESTCall(pol.Op, retry.URL.Path, retry.Header,
		[]byte(`{"cert_status":"certified"}`), keyBindsTheRetry)
	if err != nil {
		t.Fatalf("canonicalizing the retry answered %v", err)
	}

	if retryHash != staging.last.DiffHash {
		t.Errorf("the approved retry hashes to %s but the residue was staged as %s — the human's approval "+
			"is unredeemable and the withheld field can never be written",
			retryHash, staging.last.DiffHash)
	}
}

// ownedOnlyFor answers the ownership probe for exactly ONE record id and
// nothing else, which is what makes the test below able to fail.
//
// allHumanOwned above ignores the id entirely, so a probe asked about the WRONG
// record would still report a conflict and the test would pass while the gate
// was broken. The defect this guards against is precisely an id mismatch, so
// the double has to be the thing that can tell two ids apart.
type ownedOnlyFor struct{ id ids.UUID }

func (o ownedOnlyFor) HumanOwnedConflicts(_ context.Context, _ string, target ids.UUID, patch json.RawMessage) ([]string, error) {
	if target != o.id {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(patch, &fields); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	return names, nil
}

// A sub-resource patch asks the ownership probe about the record it WRITES.
//
// On /deal-rooms/{id}/tasks/{taskId} the route's own {id} is the ROOM, so a
// probe reading {id} asks "who typed this field on task ⟨room-id⟩" — a question
// no audit row answers. It misses, the split sees no conflict, and an agent
// overwrite of a human-typed document title auto-executes instead of staging. The §2.1
// protection would be off while the route still looked governed.
func TestASubResourcePatchProbesTheRecordItWrites(t *testing.T) {
	roomID, documentID := ids.NewV7(), ids.NewV7()
	staging := &capturingApprovals{}
	pol := agentPolicy{
		Op: opUpdateDealRoomDocument, Access: accessTool,
		Tool: "update_record", RecordType: recordTypeDealRoomDocument,
	}
	body := []byte(`{"title":"wording a human typed"}`)
	req := operandRequest(http.MethodPatch, "/v1/deal-rooms", roomID.String(), "documentId", documentID.String(), body)
	rec := httptest.NewRecorder()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("the handler ran — the title is human-owned, so the write must stage, not apply")
	})

	admitAgentCall(rec, req, next, admissionOutcome{
		staging: staging, ownership: ownedOnlyFor{id: documentID},
		commands: restCommandDeps{records: seamRecord{}}, pol: pol, body: body,
	})

	if staging.last.TargetID != documentID {
		t.Fatalf("staged target id = %s, want the task %s — an approval binding to the room names a "+
			"different record than the one the released call goes on to write", staging.last.TargetID, documentID)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (approval_required) — an agent silently overwriting a human-typed "+
			"to-do is the §2.1 protection this test exists to hold", rec.Code, http.StatusForbidden)
	}
}
