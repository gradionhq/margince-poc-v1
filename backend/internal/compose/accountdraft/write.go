// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package accountdraft

// The model lane: the prompt, the fence around the account's own text, and the
// grounding filter that drops a reason pointing at a record the caller cannot
// see.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// Completer is the model seam: the draft_reply lane, or nil.
type Completer interface {
	Complete(ctx context.Context, req model.Request) (model.Response, error)
}

// draftSystem is the account_draft site's prompt.
//
// Two rules it repeats because both have failure modes a reader would not
// catch. The body must never explain itself — the reasons travel in their own
// field, and a body that argues for itself is one the rep deletes a paragraph
// from before sending. And no figure may appear that the summary did not
// state: a draft that invents a price is the one mistake that goes out over a
// human's signature.
const draftSystem = `You draft the first email of a new conversation, for a salesperson to send under their own name, from a JSON summary of one account in their CRM.
Return ONLY a JSON object: {"subject":"...","body":"...","reasoning":[{"kind":"intent|recipient|relationship|deal|commitment|conversation|dossier","label":"...","entity_type":"deal|activity|person|organization|fact","entity_id":"..."}]}.
Write the body as plain text. No markdown, no HTML, no bullet characters.
Open by name using the recipient's first name exactly as given; never invent or shorten it.
Do NOT write a sign-off or a sender name. The composer adds the sender's own; a name you guessed would go out over the wrong signature.
Say one thing and ask for one thing. Three short paragraphs at most.
Never state a figure, a date or a commitment the summary did not give you. If you want one and do not have it, write around it.
The body must NEVER explain why it was written. No "based on", no "I noticed", no reference to the CRM or to this summary. The reasoning array is where that goes.
Each reasoning entry names ONE input you actually used, in the reader's words, short enough to read as a chip ("pricing concern", "follow-up due today"). Give entity_type and entity_id when the input was a record the summary identified; omit both when it was the caller's own intent.
If the summary gives you nothing but the recipient, write a short honest opener and return an empty reasoning array. Do not invent a reason.`

// draftSystemFor names THIS call's data boundary; see promptfence.Fence.Rule.
func draftSystemFor(fence promptfence.Fence) string {
	return draftSystem + "\n" + fence.Rule("account summary")
}

// draftSchema is the response shape the validated lane enforces.
const draftSchema = `{
  "type": "object",
  "required": ["subject", "body"],
  "properties": {
    "subject": {"type": "string"},
    "body": {"type": "string"},
    "reasoning": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["kind", "label"],
        "properties": {
          "kind": {"type": "string"},
          "label": {"type": "string"},
          "entity_type": {"type": "string"},
          "entity_id": {"type": "string"}
        }
      }
    }
  }
}`

// modelDraft is what the lane answers, before grounding.
type modelDraft struct {
	Subject   string        `json:"subject"`
	Body      string        `json:"body"`
	Reasoning []modelReason `json:"reasoning"`
}

type modelReason struct {
	Kind       string `json:"kind"`
	Label      string `json:"label"`
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
}

// Write produces the draft. lane may be nil, which is not an error state: it
// is the deployment saying this role runs no model, and the deterministic
// floor is the answer.
func Write(
	ctx context.Context, lane Completer, in Input,
) (Draft, crmcontracts.WrittenBy, error) {
	floor := Deterministic(in)
	if lane == nil {
		return floor, crmcontracts.Deterministic, nil
	}
	written, err := writeWithModel(ctx, lane, in)
	if err != nil {
		// A model that is down, over budget or answering nonsense must not cost
		// the rep their draft: the floor is a real message they can edit, and
		// generated_by tells the reader which writer produced it. The error is
		// deliberately swallowed rather than returned — it is a fact about the
		// lane, not about this account, and there is nothing the caller could
		// do with it.
		//nolint:nilerr // degrading to the floor IS the answer; see the doc comment
		return floor, crmcontracts.Deterministic, nil
	}
	return written, crmcontracts.Model, nil
}

func writeWithModel(ctx context.Context, lane Completer, in Input) (Draft, error) {
	req, err := groundedRequest(in)
	if err != nil {
		return Draft{}, err
	}
	res, err := lane.Complete(ctx, req)
	if err != nil {
		return Draft{}, err
	}
	return ParseDraft(res.Text, in)
}

// groundedRequest builds the model call: the system prompt naming this call's
// boundary, and the account summary INSIDE it.
//
// The caller's intent is the one input outside the fence, and it is outside
// because the caller typed it: fencing a person's own instruction would tell
// the model to treat the reader as an attacker.
func groundedRequest(in Input) (model.Request, error) {
	fence := promptfence.New()
	payload, err := json.Marshal(fencedInput(in))
	if err != nil {
		return model.Request{}, fmt.Errorf("marshal account draft input: %w", err)
	}
	content := fence.Wrap(string(payload))
	if in.Intent != "" {
		content += "\n\nThe salesperson asks for: " + in.Intent
	}
	return model.Request{
		System:         draftSystemFor(fence),
		Messages:       []model.Message{{Role: "user", Content: content}},
		ResponseSchema: json.RawMessage(draftSchema),
		SecretStripper: ai.NewSecretStripper(),
	}, nil
}

// fencedInput is the payload minus the caller's own intent, which travels
// outside the fence. Copying the struct rather than clearing the field keeps
// the caller's Input untouched — it is read again by the deterministic floor.
func fencedInput(in Input) Input {
	in.Intent = ""
	return in
}

// ParseDraft reads the lane's answer and grounds it. Exported for the
// certification case, which drives the same parse the runtime does.
func ParseDraft(raw string, in Input) (Draft, error) {
	var out modelDraft
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return Draft{}, fmt.Errorf("account draft response: %w", err)
	}
	subject := strings.TrimSpace(out.Subject)
	body := strings.TrimSpace(out.Body)
	if subject == "" || body == "" {
		return Draft{}, fmt.Errorf("account draft response: empty subject or body")
	}
	return Draft{
		Subject:   subject,
		Body:      body,
		To:        toAddresses(in),
		Reasoning: keepGroundedReasons(out.Reasoning, in),
	}, nil
}

func toAddresses(in Input) []string {
	if in.Recipient.Email == "" {
		return nil
	}
	return []string{in.Recipient.Email}
}

// keepGroundedReasons drops a reason the reader could not check.
//
// Same rule the brief's sentence filter keeps: a citation pointing at a record
// this caller's 360 did not carry is either a hallucinated id or a record
// outside their row scope, and both render as a chip that opens nothing. A
// reason with no citation at all is kept — the caller's own intent is a real
// reason and cites nothing by design.
func keepGroundedReasons(reasons []modelReason, in Input) []Reason {
	known := knownRecords(in)
	out := make([]Reason, 0, len(reasons))
	for _, reason := range reasons {
		kind, ok := parseKind(reason.Kind)
		label := strings.TrimSpace(reason.Label)
		if !ok || label == "" {
			continue
		}
		keep := Reason{Kind: kind, Label: label}
		if reason.EntityID != "" {
			if !known[reason.EntityID] {
				// A cited record the caller cannot open. The reason may still
				// be true, but it is no longer checkable, so it is dropped
				// rather than shown as a chip that leads nowhere.
				continue
			}
			keep.EntityType = reason.EntityType
			keep.EntityID = reason.EntityID
		}
		out = append(out, keep)
	}
	return out
}

// parseKind narrows the model's string to the contract's closed vocabulary. An
// unknown kind is dropped rather than passed through: the composer groups
// reasons by kind, and one it does not know would render as an unlabelled chip.
func parseKind(raw string) (crmcontracts.AccountDraftReasonKind, bool) {
	kind := crmcontracts.AccountDraftReasonKind(strings.TrimSpace(raw))
	switch kind {
	case crmcontracts.AccountDraftReasonKindIntent,
		crmcontracts.AccountDraftReasonKindRecipient,
		crmcontracts.AccountDraftReasonKindRelationship,
		crmcontracts.AccountDraftReasonKindDeal,
		crmcontracts.AccountDraftReasonKindCommitment,
		crmcontracts.AccountDraftReasonKindConversation,
		crmcontracts.AccountDraftReasonKindDossier:
		return kind, true
	default:
		return "", false
	}
}

// knownRecords is every id this draft's own input carried — which is exactly
// the set the caller's 360 let through, so it is a row-scope check and not
// merely a typo check.
func knownRecords(in Input) map[string]bool {
	known := map[string]bool{in.Recipient.ID: true}
	if in.Deal != nil {
		known[in.Deal.ID] = true
	}
	if in.Commitment != nil {
		known[in.Commitment.ID] = true
	}
	for _, act := range in.Recent {
		known[act.ID] = true
	}
	return known
}
