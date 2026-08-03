// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The signature-enrich pass (ai-operational-spec §2.9, ADR-0063):
// connector-created people still missing title AND phone get ONE model
// read of their latest inbound mail's signature block — evidence-or-omit
// (the gateEvidence discipline: a field whose snippet is not verbatim in
// the supplied lines is dropped in code, not trusted), confidence floor
// 0.6, fill-only-empty on apply. The pass proposes fields; it never
// overwrites a human's answer (GATE-AI-4).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
	"github.com/gradionhq/margince/backend/internal/shared/schema"
)

const (
	// signatureLineCount is the §2.9 input pin: the trailing non-quoted
	// lines of the person's most recent inbound mail.
	signatureLineCount = 15
	// enrichConfidenceFloor is the §2.9 acceptance floor.
	enrichConfidenceFloor = 0.6
	// enrichPassLimit bounds one pass's candidate set — the nightly cycle
	// picks up the rest tomorrow.
	enrichPassLimit = 100
)

// enrichFieldNames is the §2.9 closed vocabulary.
var enrichFieldNames = map[string]bool{
	"title": true, "phone": true, "role": true, "linkedin": true, "org_name": true,
}

const signatureEnrichSystem = `You extract contact fields from ONE email signature. Allowed fields ONLY: title, phone, role,
linkedin, org_name. Emit a field ONLY if the signature lines state it verbatim; the snippet
must appear character-for-character in the supplied text. Ignore quoted replies, legal
disclaimers, and marketing taglines. Phone numbers verbatim, never normalized.`

// signatureEnrichSystemFor names THIS call's data boundary; see promptfence.Fence.Rule.
func signatureEnrichSystemFor(fence promptfence.Fence) string {
	return signatureEnrichSystem + "\n" + fence.Rule("signature")
}

// CaptureEnricher drives the signature pass for every workspace.
type CaptureEnricher struct {
	pool  *pgxpool.Pool
	store *people.Store
	brain completer
	log   *slog.Logger
}

// NewCaptureEnricher builds the pass over the pool and one model lane.
func NewCaptureEnricher(pool *pgxpool.Pool, brain completer, log *slog.Logger) *CaptureEnricher {
	return &CaptureEnricher{pool: pool, store: people.NewStore(pool), brain: brain, log: log}
}

// RunWorkspace enriches up to enrichPassLimit candidates in the workspace
// already bound in ctx. A budget stop ends the pass cleanly; per-person model
// trouble is logged and the person is retried next cycle (their evidence rows
// are still absent).
func (e *CaptureEnricher) RunWorkspace(ctx context.Context) error {
	// The store's apply writes audit + outbox rows, so the pass binds
	// the system actor and an operation scope like every worker job.
	wsCtx := principal.WithCorrelationID(principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem,
		ID:   "agent:enrich",
	}), ids.NewV7())
	candidates, err := e.store.SignatureCandidates(wsCtx, enrichPassLimit)
	if err != nil {
		return err
	}
	// A candidate that fails is logged, not returned, and that survives the
	// fan-out deliberately: the retry is durable in the DATA rather than in
	// River. A person whose verdict could not be parsed wrote no evidence
	// rows, so SignatureCandidates re-selects them on the next pass. Failing
	// the workspace row for one unparseable reply would retry the whole
	// candidate set to re-reach the same person.
	for _, cand := range candidates {
		if err := e.enrichOne(wsCtx, cand); err != nil {
			if isBudgetStop(err) {
				e.log.InfoContext(wsCtx, "signature enrich: budget exhausted, stopping the pass")
				return nil
			}
			e.log.WarnContext(wsCtx, "signature enrich: candidate failed",
				"person", cand.PersonID.String(), "err", err)
		}
	}
	return nil
}

func isBudgetStop(err error) bool { return errors.Is(err, ai.ErrBudgetDeferred) }

// unparseableReply reports the one drop reason that means the model failed
// rather than the signature being silent — the distinction the read cursor
// turns on.
func unparseableReply(dropped []droppedFinding) bool {
	for _, d := range dropped {
		if d.Reason == dropUnparseableReply {
			return true
		}
	}
	return false
}

// enrichOne reads one candidate's signature block, gates the model's
// fields against it, and applies the survivors fill-only-empty.
func (e *CaptureEnricher) enrichOne(ctx context.Context, cand people.SignatureCandidate) error {
	lines := signatureBlock(cand.Body)
	if lines == "" {
		// Nothing to read in this mail. The read still counts: without the
		// cursor a person whose latest mail has no signature block would be
		// selected again every night for the same empty window.
		return e.store.MarkSignatureRead(ctx, cand.PersonID, cand.ActivityID)
	}
	req := signatureEnrichRequest(cand, lines)
	var resp model.Response
	var err error
	if structured, ok := e.brain.(validatedBrain); ok {
		resp, err = structured.CompleteValidated(ctx, req, signatureShapeValid)
	} else {
		resp, err = e.brain.Complete(ctx, req)
	}
	if err != nil {
		return err
	}

	// The code-side gate: verbatim-snippet-or-drop against the exact lines
	// the model was shown, then the confidence floor.
	gated, dropped := gateEvidence(resp.Text, lines, "activity:"+cand.ActivityID.String(),
		func(name string) bool { return enrichFieldNames[name] })
	if unparseableReply(dropped) {
		// A reply no reader can parse is a fault in the ANSWER, not evidence
		// that the signature states nothing — so the read goes unrecorded and
		// this person is asked again next pass.
		return fmt.Errorf("compose: unparseable signature reply for person %s", cand.PersonID)
	}
	if len(dropped) > 0 {
		e.log.DebugContext(ctx, "signature enrich: fields dropped by the evidence gate",
			"person", cand.PersonID.String(), "dropped", len(dropped))
	}
	fields := make([]people.SignatureField, 0, len(gated))
	for _, f := range gated {
		if float64(f.Confidence) < enrichConfidenceFloor {
			continue
		}
		fields = append(fields, people.SignatureField{
			Name: f.Field, Value: f.Value, Evidence: f.EvidenceSnippet, Confidence: float64(f.Confidence),
		})
	}
	if len(fields) == 0 {
		// The model answered and nothing survived the gate — an answer, and
		// the same answer next time unless this person writes again.
		return e.store.MarkSignatureRead(ctx, cand.PersonID, cand.ActivityID)
	}
	if _, err := e.store.ApplySignatureFields(ctx, cand.PersonID, cand.ActivityID, fields); err != nil {
		return err
	}
	// A model error above returns before this point on purpose: an
	// unanswered call is a call still owed, so the person stays a candidate.
	return e.store.MarkSignatureRead(ctx, cand.PersonID, cand.ActivityID)
}

// signatureEnrichRequest builds the ONE model call that reads one candidate's
// signature. It is a pure function of the candidate and the window their mail
// yielded so the same request can be issued outside the pass — by the
// certification lane — without re-creating it, because a re-creation certifies a
// copy rather than the prompt that ships.
//
// The lines arrive already derived by signatureBlock rather than being derived
// here: the evidence gate matches every quote against the SAME window the model
// was shown, so one derivation feeds both readers and neither can drift.
//
// The fence is minted here, per request: a boundary reused across calls is one a
// previous sender has already been shown, and every field of this prompt is
// their own writing.
func signatureEnrichRequest(cand people.SignatureCandidate, lines string) model.Request {
	fence := promptfence.New()
	var prompt strings.Builder
	// The person's own name and address are theirs to write, so they go INSIDE
	// the boundary like the signature does. Interpolated into a header line they
	// would be reading in the prompt's own voice, which is the whole attack.
	prompt.WriteString("Person (untrusted):\n")
	prompt.WriteString(fence.Wrap(fmt.Sprintf("Name: %s\nEmail: %s", cand.FullName, cand.Email)) + "\n")
	prompt.WriteString("Fields currently empty: [\"title\",\"phone\"]\n")
	prompt.WriteString("Signature block (untrusted; the trailing lines of their last email):\n")
	prompt.WriteString(fence.WrapAttr("source_id", cand.ActivityID.String(), lines) + "\n")
	prompt.WriteString(`Return JSON: { "fields": [ { "field", "value", "evidence_snippet", "confidence" } ] }`)

	return model.Request{
		System:         signatureEnrichSystemFor(fence),
		Messages:       []model.Message{{Role: chatRoleUser, Content: prompt.String()}},
		MaxTokens:      ai.ReasoningOutputMaxTokens,
		ResponseSchema: signatureEnrichSchema(),
		SecretStripper: ai.NewSecretStripper(),
	}
}

// signatureBlock returns the trailing signatureLineCount non-quoted,
// non-empty-tail lines of a stored email body — the §2.9 source window.
// Quoted history (">"-prefixed) is not identity evidence and is excluded.
func signatureBlock(body string) string {
	lines := strings.Split(body, "\n")
	var kept []string
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), ">") {
			continue
		}
		kept = append(kept, l)
	}
	// Trim trailing blank lines so the window holds content, not padding.
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if len(kept) > signatureLineCount {
		kept = kept[len(kept)-signatureLineCount:]
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// signatureShapeValid is the §5.2 retry validator: parseable fields with
// the closed vocabulary — evidence checking stays code-side in
// gateEvidence (the model must not be trusted to self-certify).
func signatureShapeValid(text string) error {
	var parsed struct {
		Fields []extractedField `json:"fields"`
	}
	if err := json.Unmarshal([]byte(ai.Unfence(text)), &parsed); err != nil {
		return fmt.Errorf("output is not the required JSON shape: %w", err)
	}
	for _, f := range parsed.Fields {
		// The field name is MODEL output, so it is bounded before it reaches an
		// error that is logged and, on a retry, fed back into the prompt.
		if !enrichFieldNames[f.Field] {
			return fmt.Errorf("field %q is not in the allowed set", clampToken(f.Field))
		}
	}
	return nil
}

// signatureEnrichSchema is the generation-time shape guardrail (§2.9).
func signatureEnrichSchema() json.RawMessage {
	return schema.Must(schema.Object(
		map[string]schema.Node{
			laneFields: schema.Array(schema.Object(
				map[string]schema.Node{
					extractionFieldKey: schema.Enum("title", "phone", "role", "linkedin", "org_name"),
					"value":            schema.String(),
					"evidence_snippet": schema.String(),
					"confidence":       schema.Number(),
				},
				"field", "value", "evidence_snippet", "confidence",
			)),
		},
		"fields",
	))
}
