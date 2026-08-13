// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The per-field human-edit-precedence split (interfaces.md §2.1) on the
// REST transport of the agent gate: the shared partition lives in
// modules/agents (SplitHumanOwned); this file owns the REST-specific
// mechanics — body rewrite, response buffering/splicing, and the
// canonicalRESTCall hash that binds the staged sub-patch (ADR-0036).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// opRenameCustomField is the one patch operation both this file and
// agentcommand.go's restCommands table name: the sole action-shaped op
// (comment below) that is ALSO a whole-record field patch the governance seam
// stages, so it is the one place those two lists have to agree on the same
// operationId spelled the same way.
const opRenameCustomField = "renameCustomField"

// The remaining five action-shaped ops named below are ALSO both this
// file's and agentcommand.go's restCommands table's
// (agentcommandnested.go): named once here so the two do not spell an
// operationId twice each. opUpsertPartner is named alongside them for the
// same reason — agentcommand.go's restCommands entry needs the identical
// spelling — even though (its own comment below says why) upsertPartner is
// NOT a member of the map these five populate.
const (
	opAddListMember       = "addListMember"
	opApplyTag            = "applyTag"
	opAddOfferLineItem    = "addOfferLineItem"
	opUpdateOfferLineItem = "updateOfferLineItem"
	opRemoveOfferLineItem = "removeOfferLineItem"
	opUpsertPartner       = "upsertPartner"
)

// actionShapedUpdateOps are the update_record twins whose body is a
// membership/apply request naming ANOTHER record, or a mutation of a
// CHILD record (an offer's line items), NOT a field patch on the routed
// record itself — there is no human-typed field of the routed record the
// call could overwrite, so the ownership probe has nothing to ask, and the
// call runs 🟢 by design. Membership is earned by that one test; an op
// absent here gets the full split instead.
//
// upsertPartner LOOKS like it belongs — PUT .../partner, a body naming
// partner fields — but is deliberately ABSENT: the resolver
// (commandnested.go) maps partner→organization, so this patch really IS a
// field patch on the routed record (the organization), which is exactly
// the case this map exists to exclude. An agent overwriting a human-typed
// partner field (cert_status, margin_tier, …) has to stage, the same §2.1
// precedence protection every ordinary organization field patch gets —
// adding upsertPartner here would silently disable that protection for
// this one operation. TestUpsertPartnerStagesAHumanOwnedPartnerField pins
// the split running for it.
//
// renameCustomField is here for a different reason: its target is a
// catalog CONFIG row, not record data — §2.1 human-edit precedence
// protects human-typed record values from agent overwrite, while a
// catalog label rename is the action the contract deliberately pins 🟢;
// left to the split, the creating admin's audit trail would mark `label`
// human-owned and silently convert every agent rename into a 🟡 staging.
var actionShapedUpdateOps = map[string]bool{
	opApplyTag:            true,
	opAddListMember:       true,
	opAddOfferLineItem:    true,
	opUpdateOfferLineItem: true,
	opRemoveOfferLineItem: true,
	opRenameCustomField:   true,
}

// splitHumanOwnedUpdate is the per-field human-edit-precedence split
// (interfaces.md §2.1) on the REST twin of the 🟢 update_record verb. The
// body IS the field patch; the route's record_type annotation and {id}
// name the audited record. Fields whose current value a human last wrote
// are withheld and staged as a 🟡 approval while the rest of the patch
// proceeds to the handler in the same request — mirroring the MCP tool,
// so transport never changes what a human decision protects.
//
// A retry presenting an X-Approval-Token never arrives here: the auto-execute
// arm consumes the token and forwards the released call straight to the handler
// (runAutoExecuted, agentgateauto.go), because that retry carries exactly the staged
// sub-patch whose hash the approval was bound to and re-splitting it would stage
// a second approval for the overwrite just approved. This function used to
// redeem the token itself, which is how every OTHER tool's 🟢 arm came to ignore
// one (gradionhq/margince-poc-v1#812).
func splitHumanOwnedUpdate(w http.ResponseWriter, r *http.Request, next http.Handler, staging agents.Approvals, commands restCommandDeps, ownership agents.FieldOwnership, pol agentPolicy, body []byte) {
	ctx := r.Context()
	raw := chi.URLParam(r, "id")
	if raw == "" {
		// Every field-patch twin routes with {id} today; a future route
		// without one cannot answer the ownership question, so it is
		// refused, never admitted unprobed.
		httperr.Write(w, r, fmt.Errorf(
			"agent gate: %s routes update_record without a target id — the ownership probe cannot run: %w",
			pol.Op, apperrors.ErrPermissionDenied))
		return
	}
	targetID, err := ids.Parse(raw)
	if err != nil {
		httperr.Write(w, r, apperrors.ErrNotFound)
		return
	}
	split, err := agents.SplitHumanOwned(ctx, ownership, string(pol.RecordType), targetID, body)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if len(split.Conflicts) == 0 {
		next.ServeHTTP(w, r)
		return
	}
	if staging == nil {
		httperr.Write(w, r, fmt.Errorf("fields %s were last edited by a human, and this surface has no approvals engine to stage the overwrite: %w",
			strings.Join(split.Conflicts, ", "), apperrors.ErrRequiresApproval))
		return
	}
	if split.AutoExecute == nil {
		// Every touched field is human-owned: nothing applies, the whole
		// request is the staged change — the approved retry is this exact
		// request again.
		//
		// stageRefusal resolves through the SAME command seam every other
		// registered patch op does (agentcommand.go's patchCommand, now that
		// all thirteen whole-record patch routes are registered), which means
		// this branch carries patchResolver.Guards too: a records.Read plus
		// refuseStagingElsewhere the split path never ran on its own before
		// that registration. That is deliberate, not a side effect nobody
		// noticed — an approval staged here for a mirror-held
		// (Authoritative:false) record could never be redeemed anyway, since
		// redemption's version pin reads our own tables, so refusing now
		// beats spending a human's yes on a call that cannot be released.
		stageRefusal(w, r, staging, commands, pol, body)
		return
	}
	applyAutoExecuteAndStageResidue(w, r, next, staging, commands, pol, split)
}

// applyAutoExecuteAndStageResidue handles the mixed patch: everything that can
// refuse the residue is settled first, then the auto-execute remainder runs
// through the real handler, and only then is the residue staged — against the
// post-write version, the state the approving human will actually judge, so this
// call's own auto-execute half cannot invalidate its staged half (ADR-0036 §2).
// The staging note is spliced into the handler's own 2xx record body, making the
// split legible in a single response.
func applyAutoExecuteAndStageResidue(w http.ResponseWriter, r *http.Request, next http.Handler, staging agents.Approvals, commands restCommandDeps, pol agentPolicy, split agents.PatchSplit) {
	// Everything this function can REFUSE on is settled before the auto-execute
	// half runs, because none of it depends on that write: the canonical form and
	// the staged target are functions of pol, the request's own path and headers,
	// and split.Staged, all of which exist before the handler is dispatched. Asked
	// afterwards — which is where they used to be — a resolver or Guards refusal
	// answered a refusal status for a request whose first half had already
	// committed, and the buffered 2xx was dropped: the agent was told the change
	// was refused and could retry the whole patch against a record that had
	// already moved (gradionhq/margince-poc-v1#1073). Asked here, a refusal costs
	// the caller only a retry, which is the rule pinAutoExecutedWrite already
	// applies to the unconsumed case.
	//
	// This does NOT move the pin: approvals resolves target_version itself, inside
	// the staging transaction (the comment on the Stage call below), which still
	// runs after the write — so the residue is still staged against the post-write
	// state the approving human judges (ADR-0036 §2).
	canonical, diffHash, cErr := canonicalRESTCall(pol.Op, r.URL.Path, r.Header, split.Staged, keySettledByThisCall)
	if cErr != nil {
		httperr.Write(w, r, cErr)
		return
	}
	// The staged target is resolved through the SAME seam stageRefusal uses
	// (stagedTarget, which is resolveStagedTarget plus the untyped-target
	// check), not read off pol.RecordType directly:
	// upsertPartner's declared record_type is "partner", but its resolver
	// (modules/agents/commandnested.go) stages "organization" — the row a
	// human's decision and the approvals surface's own visibility probe
	// actually depend on. Staging target_entity_type="partner" here would
	// have named a pair neither targetProbes nor existenceProbes
	// (approvals/targetvisibility.go) has a rule for, so the approval fails
	// closed as invisible and undecidable — the zombie authority object
	// this whole seam exists to prevent, for the one write this branch is
	// supposed to protect. body is split.Staged, the sub-patch this
	// approval actually binds to (canonicalRESTCall's own argument, above),
	// not the full original request half of which already ran.
	info, ok := stagedTarget(w, r, commands, pol, split.Staged)
	if !ok {
		// stagedTarget already wrote the refusal, and nothing has been
		// written to the record yet, so it is the whole answer.
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(split.AutoExecute))
	r.ContentLength = int64(len(split.AutoExecute))
	buffered := newBufferedResponse()
	// Metered onto the BUFFER: the replay below writes raw bytes rather than
	// going back through WriteJSON, so the record this half serves is counted
	// where the handler produces it or it is never counted at all.
	next.ServeHTTP(remeter(w, buffered), r)
	if buffered.status < 200 || buffered.status > 299 {
		// The auto-execute half was refused (validation, version skew, …): that
		// refusal is the whole answer, and nothing is staged — the agent
		// must fix the call, which re-runs the split from scratch.
		buffered.flushTo(w)
		return
	}
	// UseNumber keeps integers exact: a plain interface{} decode renders
	// every JSON number as float64, silently truncating any value past
	// 2^53 on this re-encode path (money-minor fields, version).
	var record map[string]any
	dec := json.NewDecoder(bytes.NewReader(buffered.body.Bytes()))
	dec.UseNumber()
	if uErr := dec.Decode(&record); uErr != nil {
		httperr.Write(w, r, fmt.Errorf(
			"agent gate: %s applied the permitted fields, but its response cannot carry the staging note for the withheld human-edited fields (%s): %w",
			pol.Op, strings.Join(split.Conflicts, ", "), uErr))
		return
	}
	// No version pin travels from here, and the residue is still staged against
	// the state the approving human will actually judge (ADR-0036 §2). What
	// makes that true is WHEN the pin is read, not who supplies it: the
	// approvals engine resolves target_version itself, inside the staging
	// transaction (approvals.insertProposalInTx), which this call reaches only
	// after the auto-execute half above committed — so the version it takes is
	// the post-write one, and this call's own successful half cannot invalidate
	// its own staged half. A pin named here would not survive the trip anyway:
	// approvalsAdapter.Stage (registry.go) forwards none, deliberately.
	approvalID, sErr := staging.Stage(r.Context(), agents.StageRequest{
		Tool:           pol.Tool,
		ProposedChange: canonical,
		DiffHash:       diffHash,
		TargetType:     info.TargetType,
		TargetID:       info.TargetID,
		// The staged sub-patch is what the approval binds to, so the summary
		// names the values it would write, not only the field names it would
		// write them to: "overwrite human-edited amount_minor" told an
		// approver which field was at stake and never with what.
		Summary: "overwrite human-edited " + strings.Join(split.Conflicts, ", ") + " — " +
			restSummary(pol.Op, r.Method, r.URL.Path, split.Staged),
	})
	if sErr != nil {
		httperr.Write(w, r, fmt.Errorf("the other fields were updated, but staging the human-edited fields (%s) failed: %w",
			strings.Join(split.Conflicts, ", "), sErr))
		return
	}
	record["staged_approval"] = map[string]any{
		"approval_id": approvalID,
		"fields":      split.Conflicts,
		"replay":      json.RawMessage(split.Staged),
		"message": fmt.Sprintf(
			"fields %s were last edited by a human and were NOT applied; staged as approval %s — once a human approves it, repeat this request with ONLY those fields and the %s: %s header",
			strings.Join(split.Conflicts, ", "), approvalID, approvalTokenHeader, approvalID),
	}
	buffered.flushJSON(w, r, record)
}

// bufferedResponse holds a handler's answer so the gate can decide
// whether to stage against it and splice the staging note in before
// anything reaches the wire.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: http.Header{}}
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

// flushTo replays the buffered answer verbatim.
func (b *bufferedResponse) flushTo(w http.ResponseWriter) {
	copyHeaders(w, b.header)
	w.WriteHeader(b.status)
	//craft:ignore swallowed-errors a failed write here means the client hung up — there is no channel left to report on
	_, _ = w.Write(b.body.Bytes())
}

// flushJSON replays the buffered status and headers with a re-encoded
// JSON body (the Content-Length of the original no longer applies).
func (b *bufferedResponse) flushJSON(w http.ResponseWriter, r *http.Request, payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		// Marshaling a map decoded from JSON cannot fail in practice; if
		// it ever does, the staging already exists — report rather than
		// send a truncated record. Through httperr like every other
		// refusal on this surface: a client parsing RFC 7807 must not meet
		// a text/plain body on one path out of a handler, and the marshal
		// error itself stays server-side.
		httperr.Write(w, r, fmt.Errorf("re-encoding the split update response failed: %w", err))
		return
	}
	copyHeaders(w, b.header)
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(b.status)
	//craft:ignore swallowed-errors a failed write here means the client hung up — there is no channel left to report on
	_, _ = w.Write(body)
}

func copyHeaders(w http.ResponseWriter, headers http.Header) {
	for name, values := range headers {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	// The buffered body may be re-encoded; a stale length would truncate.
	w.Header().Del("Content-Length")
}
