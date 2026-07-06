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

// actionShapedUpdateOps are the update_record twins whose body is a
// membership/apply request naming ANOTHER record, or a mutation of a
// CHILD record (an offer's line items), not a field patch on the routed
// record itself — there is no human-typed field of the routed record the
// call could overwrite, so the ownership probe has nothing to ask and the
// call runs 🟢 by design (an op absent here gets the full split; the
// deliberate inclusion is upsertPartner, which the resolver maps
// partner→organization so its patch IS a field patch on that org).
var actionShapedUpdateOps = map[string]bool{
	"applyTag":            true,
	"addListMember":       true,
	"addOfferLineItem":    true,
	"updateOfferLineItem": true,
	"removeOfferLineItem": true,
}

// splitOrRedeemUpdate is the per-field human-edit-precedence split
// (interfaces.md §2.1) on the REST twin of the 🟢 update_record verb. The
// body IS the field patch; the route's record_type annotation and {id}
// name the audited record. Fields whose current value a human last wrote
// are withheld and staged as a 🟡 approval while the rest of the patch
// proceeds to the handler in the same request — mirroring the MCP tool,
// so transport never changes what a human decision protects. An
// X-Approval-Token redeems a prior staging: the approved retry carries
// exactly the staged sub-patch, whose hash the staging was bound to.
func splitOrRedeemUpdate(w http.ResponseWriter, r *http.Request, next http.Handler, staging agents.Approvals, ownership agents.FieldOwnership, pol agentPolicy, body []byte) {
	ctx := r.Context()
	if redeemIfPresented(w, r, next, staging, pol, body) {
		return
	}
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
	split, err := agents.SplitHumanOwned(ctx, ownership, pol.RecordType, targetID, body)
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
	if split.Green == nil {
		// Every touched field is human-owned: nothing applies, the whole
		// request is the staged change — the approved retry is this exact
		// request again.
		stageRefusal(w, r, staging, pol, body)
		return
	}
	applyGreenAndStageResidue(w, r, next, staging, pol, targetID, split)
}

// applyGreenAndStageResidue handles the mixed patch: the green remainder
// runs through the real handler first, then the residue is staged against
// the post-write version — the state the approving human will actually
// judge, so this call's own green half cannot invalidate its staged half
// (ADR-0036 §2). The staging note is spliced into the handler's own 2xx
// record body, making the split legible in a single response.
func applyGreenAndStageResidue(w http.ResponseWriter, r *http.Request, next http.Handler, staging agents.Approvals, pol agentPolicy, targetID ids.UUID, split agents.PatchSplit) {
	r.Body = io.NopCloser(bytes.NewReader(split.Green))
	r.ContentLength = int64(len(split.Green))
	buffered := newBufferedResponse()
	next.ServeHTTP(buffered, r)
	if buffered.status < 200 || buffered.status > 299 {
		// The green half was refused (validation, version skew, …): that
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
	canonical, diffHash, cErr := canonicalRESTCall(pol.Op, r.URL.Path, split.Staged)
	if cErr != nil {
		httperr.Write(w, r, cErr)
		return
	}
	approvalID, sErr := staging.Stage(r.Context(), agents.StageRequest{
		Tool:           pol.Tool,
		ProposedChange: canonical,
		DiffHash:       diffHash,
		TargetType:     pol.RecordType,
		TargetID:       targetID,
		TargetVersion:  recordVersion(record),
		Summary: fmt.Sprintf("Agent REST %s %s: overwrite human-edited %s",
			r.Method, r.URL.Path, strings.Join(split.Conflicts, ", ")),
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
	buffered.flushJSON(w, record)
}

// recordVersion pins the staged residue to the record version the green
// half of the split produced. Contract record bodies carry the read-only
// `version` (RowVersion, ADR-0036 §3); a response without one yields no
// pin rather than a wrong one. The body is decoded with UseNumber, so the
// value arrives as json.Number and is read losslessly.
func recordVersion(record map[string]any) *int64 {
	number, ok := record["version"].(json.Number)
	if !ok {
		return nil
	}
	version, err := number.Int64()
	if err != nil {
		return nil
	}
	return &version
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
func (b *bufferedResponse) flushJSON(w http.ResponseWriter, payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		// Marshaling a map decoded from JSON cannot fail in practice; if
		// it ever does, the staging already exists — report rather than
		// send a truncated record.
		http.Error(w, "re-encoding the split update response failed", http.StatusInternalServerError)
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
