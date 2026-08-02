// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The batched capture-classify engine (ai-operational-spec §2.8, ADR-0063):
// label captured mail commitment | meeting | noise for attention routing.
// The backlog IS the partial index (capture_label IS NULL on connector
// email activities) — no work table; each model call labels up to ten
// messages and COMMITS per call, so a budget stop or a crash loses nothing
// and the remainder simply stays unlabeled for the next cycle. A noise
// label demotes attention only — it never deletes, archives, or suppresses
// the record an email created (§3.2 hard floor).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
	"github.com/gradionhq/margince/backend/internal/shared/schema"
)

const (
	// classifyBatchSize is the AIRT-PARAM-35 batch pin: ten messages per
	// call, bodies truncated, fits the light tier's window.
	classifyBatchSize = 10
	// classifyBodyLimit truncates each body for the prompt (AIRT-PARAM-35).
	classifyBodyLimit = 1500
	// classifyConfidenceFloor: below it the item is re-asked SOLO on the
	// routing ladder's fallback rather than guessed in-batch (§2.8).
	classifyConfidenceFloor = 0.7
	// classifyCatchUpCap bounds one catch-up pass (ADR-0063: hourly cap
	// 500); the nightly pass runs the same engine with a higher cap.
	classifyCatchUpCap = 500
)

var classifyLabels = map[string]bool{"commitment": true, "meeting": true, "noise": true}

const classifySystem = `You label captured emails for attention routing. For EACH supplied message emit exactly one
label: "commitment" (a promise or request to act), "meeting" (scheduling or follow-through),
or "noise" (neither). Labels route attention; they change no data. If a message fits both
commitment and meeting, choose commitment.`

// classifySystemFor names THIS call's data boundary; see promptfence.Fence.Rule.
func classifySystemFor(fence promptfence.Fence) string {
	return classifySystem + "\n" + fence.Rule("message")
}

// CaptureClassifier drives the batched label pass for every workspace.
// The label columns are the activities module's; this engine reads and
// writes them only through its store.
type CaptureClassifier struct {
	pool  *pgxpool.Pool
	store *activities.Store
	brain completer
	log   *slog.Logger
}

// NewCaptureClassifier builds the engine over the pool and one model lane.
func NewCaptureClassifier(pool *pgxpool.Pool, brain completer, log *slog.Logger) *CaptureClassifier {
	return &CaptureClassifier{pool: pool, store: activities.NewStore(pool), brain: brain, log: log}
}

// unlabeledMessage is one backlog row as the prompt sees it.
type unlabeledMessage = activities.UnlabeledEmail

// classifyResult is one model verdict.
type classifyResult struct {
	ID         string            `json:"id"`
	Label      string            `json:"label"`
	Confidence schema.Confidence `json:"confidence"`
}

type classifyPayload struct {
	Results []classifyResult `json:"results"`
}

// RunWorkspace drains up to cap backlog messages in the workspace already
// bound in ctx. A budget stop ends the pass cleanly — the remainder requeues
// implicitly (it is simply still unlabeled). Only infrastructure faults return
// an error; per-batch model trouble is logged and skipped.
//
// The cap is PER WORKSPACE, matching capture_counterparty_verdict, whose own
// counter is declared inside its workspace loop for a stated reason: a shared
// counter lets one large backlog consume the whole budget and starve every
// workspace after it. The two sibling passes implementing the same ADR-0063
// shape disagreed; this resolves them toward the one carrying a rationale.
// The number itself is unchanged.
func (c *CaptureClassifier) RunWorkspace(ctx context.Context, maxLabels int) error {
	if maxLabels <= 0 {
		maxLabels = classifyCatchUpCap
	}
	labeled := 0
	for labeled < maxLabels {
		batch, err := c.store.UnlabeledCaptureEmails(ctx, classifyBatchSize, classifyBodyLimit)
		if err != nil {
			return fmt.Errorf("classify: reading backlog: %w", err)
		}
		if len(batch) == 0 {
			return nil
		}
		n, err := c.classifyBatch(ctx, batch)
		labeled += n
		if errors.Is(err, ai.ErrBudgetDeferred) {
			// ≥100% band: non-interactive work stops for this cycle;
			// what is labeled is committed, the rest waits (§2.8).
			c.log.InfoContext(ctx, "capture classify: budget exhausted, stopping the pass", "labeled", labeled)
			return nil
		}
		if err != nil {
			// This workspace's pass FAILS. Before the fan-out a bad batch was
			// logged and skipped so it could not starve the rest of the fleet;
			// each workspace now has its own row, so there is no fleet left to
			// starve and swallowing it would put the green row back. The capped
			// ladder is what stops it spinning on the same rows.
			return fmt.Errorf("classify: draining the backlog: %w", err)
		}
		if n == 0 {
			// Every verdict stayed below the floor: the same rows would be
			// fetched again forever. They wait for the next cycle.
			c.log.InfoContext(ctx, "capture classify: batch made no progress, moving on")
			return nil
		}
	}
	return nil
}

// classifyBatch labels one batch with ONE model call and commits the
// labels — the per-call commit IS the checkpoint (AIRT-PARAM-35). Items
// below the confidence floor are re-asked solo; a solo re-ask that still
// fails floors leaves the row unlabeled for the next cycle rather than
// guessing. Returns how many rows were labeled.
func (c *CaptureClassifier) classifyBatch(ctx context.Context, batch []unlabeledMessage) (int, error) {
	verdicts, err := c.ask(ctx, batch)
	if err != nil {
		return 0, err
	}
	labeled := 0
	var retry []unlabeledMessage
	byID := indexByID(batch)
	for _, v := range verdicts {
		msg, ok := byID[v.ID]
		if !ok {
			continue // validator guarantees this cannot happen; belt and braces
		}
		if v.Confidence < classifyConfidenceFloor {
			retry = append(retry, msg)
			continue
		}
		applied, err := c.store.SetCaptureLabel(ctx, msg.ID, v.Label)
		if err != nil {
			return labeled, err
		}
		if applied {
			labeled++
		}
	}
	for _, msg := range retry {
		// The solo re-ask escalates the ladder (L-S → C-C) by being its
		// own structured call; still below the floor = still unlabeled.
		solo, err := c.ask(ctx, []unlabeledMessage{msg})
		if err != nil {
			return labeled, err
		}
		if len(solo) == 1 && solo[0].Confidence >= classifyConfidenceFloor {
			applied, err := c.store.SetCaptureLabel(ctx, msg.ID, solo[0].Label)
			if err != nil {
				return labeled, err
			}
			if applied {
				labeled++
			}
		}
	}
	return labeled, nil
}

// classifyRequest builds the ONE model call that labels one batch. It is a pure
// function of the batch so the same request can be issued outside the engine —
// by the certification lane — without re-creating it, because a re-creation
// certifies a copy rather than the prompt that ships.
//
// One fence for the whole call, wrapping each message in its own span. This
// prompt carries several senders at once, and none of them has seen the nonce,
// so no message can close its own span — and therefore none can reach the text
// of another sender's mail and label it. It is minted here, per request: a
// boundary reused across calls is one a previous sender has already been shown.
func classifyRequest(batch []unlabeledMessage) model.Request {
	fence := promptfence.New()
	var prompt strings.Builder
	prompt.WriteString("Messages (untrusted; classify each by its id):\n")
	for _, m := range batch {
		message := fmt.Sprintf("Subject: %s\n%s", m.Subject, m.Body)
		prompt.WriteString(fence.WrapAttr("source_id", m.ID.String(), message) + "\n")
	}
	prompt.WriteString(`Return JSON: { "results": [ { "id", "label", "confidence" } ] } — one entry per supplied id.`)

	return model.Request{
		System:         classifySystemFor(fence),
		Messages:       []model.Message{{Role: chatRoleUser, Content: prompt.String()}},
		MaxTokens:      ai.ReasoningOutputMaxTokens,
		ResponseSchema: classifySchema(),
		SecretStripper: ai.NewSecretStripper(),
	}
}

// ask makes one structured classify call for the given messages.
func (c *CaptureClassifier) ask(ctx context.Context, batch []unlabeledMessage) ([]classifyResult, error) {
	req := classifyRequest(batch)
	validate := classifyShapeValid(batch)
	var resp model.Response
	var err error
	if structured, ok := c.brain.(validatedBrain); ok {
		resp, err = structured.CompleteValidated(ctx, req, validate)
	} else {
		resp, err = c.brain.Complete(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	var payload classifyPayload
	if err := json.Unmarshal([]byte(ai.Unfence(resp.Text)), &payload); err != nil {
		return nil, fmt.Errorf("classify: unparseable model output: %w", err)
	}
	if msg := validateClassifyPayload(payload, batch); msg != "" {
		return nil, fmt.Errorf("classify: %s", msg)
	}
	return payload.Results, nil
}

// classifyShapeValid is the §5.2 validator: every requested id exactly
// once, ids verbatim, labels in the closed set — schema fidelity is a
// deterministic hard floor (§3.2).
func classifyShapeValid(batch []unlabeledMessage) ai.Validator {
	return func(text string) error {
		var payload classifyPayload
		if err := json.Unmarshal([]byte(ai.Unfence(text)), &payload); err != nil {
			return fmt.Errorf("output is not the required JSON shape: %w", err)
		}
		if msg := validateClassifyPayload(payload, batch); msg != "" {
			return errors.New(msg)
		}
		return nil
	}
}

// validateClassifyPayload names the first §2.8 batch-fidelity violation,
// or "" when the payload is exact.
func validateClassifyPayload(payload classifyPayload, batch []unlabeledMessage) string {
	seen := map[string]bool{}
	want := map[string]bool{}
	for _, m := range batch {
		want[m.ID.String()] = true
	}
	for _, r := range payload.Results {
		// Every echoed token is MODEL output, and a sender who got the model to
		// obey can choose it — so it is bounded before it reaches an error string
		// that ends up in the operator's log and, on a retry, back in the prompt.
		if !want[r.ID] {
			return fmt.Sprintf("result id %q was not requested", clampToken(r.ID))
		}
		if seen[r.ID] {
			return fmt.Sprintf("result id %q appears twice", clampToken(r.ID))
		}
		seen[r.ID] = true
		if !classifyLabels[r.Label] {
			return fmt.Sprintf("label %q is not commitment|meeting|noise", clampToken(r.Label))
		}
		if r.Confidence < 0 || r.Confidence > 1 {
			return fmt.Sprintf("confidence %v is outside [0,1]", r.Confidence)
		}
	}
	for id := range want {
		if !seen[id] {
			return fmt.Sprintf("requested id %q is missing from the results", id)
		}
	}
	return ""
}

// classifySchema is the generation-time shape guardrail (§2.8).
func classifySchema() json.RawMessage {
	return schema.Must(schema.Object(
		map[string]schema.Node{
			"results": schema.Array(schema.Object(
				map[string]schema.Node{
					"id":                    schema.String(),
					"label":                 schema.Enum("commitment", "meeting", "noise"),
					extractionConfidenceKey: schema.Number(),
				},
				"id", "label", "confidence",
			)),
		},
		"results",
	))
}

func indexByID(batch []unlabeledMessage) map[string]unlabeledMessage {
	out := make(map[string]unlabeledMessage, len(batch))
	for _, m := range batch {
		out[m.ID.String()] = m
	}
	return out
}
