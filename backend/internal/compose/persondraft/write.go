// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package persondraft

// The model lane: the prompt, the fence around the person's own text, and the
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

// Completer is the model seam: the draft lane, or nil.
type Completer interface {
	Complete(ctx context.Context, req model.Request) (model.Response, error)
}

// draftSystem is the person_draft site's prompt.
//
// Three rules it repeats because each has a failure mode a reader would not
// catch. The body must never explain itself — the reasons travel in their own
// field, and a body that argues for itself is one the rep deletes a paragraph
// from before sending. No figure may appear that the summary did not state: a
// draft that invents a price is the one mistake that goes out over a human's
// signature. And a claim is what this person said, so it may be answered but
// never attributed back to them as an accusation.
const draftSystem = `You draft an email to one contact, for a salesperson to send under their own name, from a JSON summary of that contact in their CRM.
Return ONLY a JSON object: {"subject":"...","body":"...","reasoning":[{"kind":"intent|recipient|relationship|deal|commitment|conversation","label":"...","entity_type":"deal|activity|person","entity_id":"..."}]}.
Write the body as plain text. No markdown, no HTML, no bullet characters.
Open by name using the recipient's first name exactly as given; never invent or shorten it.
Do NOT write a sign-off or a sender name. The composer adds the sender's own; a name you guessed would go out over the wrong signature.
Say one thing and ask for one thing. Three short paragraphs at most.
Never state a figure, a date or a commitment the summary did not give you. If you want one and do not have it, write around it.
The claims are things this contact said. Answer one of them if it helps; never quote it back at them as something they are on record as saying.
The body must NEVER explain why it was written. No "based on", no "I noticed", no reference to the CRM or to this summary. The reasoning array is where that goes.
Each reasoning entry names ONE input you actually used, in the reader's words, short enough to read as a chip ("pricing concern", "asked about onboarding"). Give entity_type and entity_id when the input was a record the summary identified; omit both when it was the caller's own intent.
sections_omitted names what the reader of this summary was not allowed to see. Say nothing about those subjects rather than inferring around the gap.
If the summary gives you nothing but the recipient, write a short honest opener and return an empty reasoning array. Do not invent a reason.`

// draftSystemFor names THIS call's data boundary; see promptfence.Fence.Rule.
func draftSystemFor(fence promptfence.Fence) string {
	return draftSystem + "\n" + fence.Rule("contact summary")
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

// Write produces the draft. lane may be nil, which is not an error state: it is
// the deployment saying this role runs no model, and the deterministic floor is
// the answer.
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
		// lane, not about this person, and there is nothing the caller could do
		// with it.
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
// boundary, and the contact summary INSIDE it.
//
// The caller's intent is the one input outside the fence, and it is outside
// because the caller typed it: fencing a person's own instruction would tell
// the model to treat the reader as an attacker.
func groundedRequest(in Input) (model.Request, error) {
	fence := promptfence.New()
	payload, err := json.Marshal(fencedInput(in))
	if err != nil {
		return model.Request{}, fmt.Errorf("marshal person draft input: %w", err)
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

// ParseDraft reads the lane's answer and grounds it.
func ParseDraft(raw string, in Input) (Draft, error) {
	var out modelDraft
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return Draft{}, fmt.Errorf("person draft response: %w", err)
	}
	subject := strings.TrimSpace(out.Subject)
	body := strings.TrimSpace(out.Body)
	if subject == "" || body == "" {
		return Draft{}, fmt.Errorf("person draft response: empty subject or body")
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
// A citation pointing at a record this caller's 360 did not carry is either a
// hallucinated id or a record outside their row scope, and both render as a
// chip that opens nothing. A reason with no citation at all is kept for the
// caller's own intent, which cites nothing by design.
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
			// The PAIR, not the id alone: an id checked without its type lets a
			// deal id come back labelled as a person, and the chip then opens the
			// wrong record's page rather than nothing at all — the worse of the
			// two failures, because it looks like it worked.
			if known[reason.EntityID] != reason.EntityType {
				continue
			}
			keep.EntityType = reason.EntityType
			keep.EntityID = reason.EntityID
		} else if kind != crmcontracts.AccountDraftReasonKindIntent {
			// A reason with no citation is only honest for the caller's own
			// intent. An uncited "deal" or "conversation" reason is a claim about
			// a record with no record behind it — exactly what the grounding
			// filter exists to drop.
			continue
		}
		out = append(out, keep)
	}
	return out
}

// parseKind narrows the model's string to the contract's closed vocabulary. An
// unknown kind is dropped rather than passed through: the composer groups
// reasons by kind, and one it does not know would render as an unlabelled chip.
//
// `dossier` is absent on purpose. It names a company's recorded facts, and this
// draft never reads them — accepting it would let the model label a person's
// claim as something the company published.
func parseKind(raw string) (crmcontracts.AccountDraftReasonKind, bool) {
	kind := crmcontracts.AccountDraftReasonKind(strings.TrimSpace(raw))
	switch kind {
	case crmcontracts.AccountDraftReasonKindIntent,
		crmcontracts.AccountDraftReasonKindRecipient,
		crmcontracts.AccountDraftReasonKindRelationship,
		crmcontracts.AccountDraftReasonKindDeal,
		crmcontracts.AccountDraftReasonKindCommitment,
		crmcontracts.AccountDraftReasonKindConversation:
		return kind, true
	default:
		return "", false
	}
}

// knownRecords maps every id this draft's own input carried to the KIND that id
// actually is — which is exactly the set the caller's 360 let through, so it is
// a row-scope check and not merely a typo check.
//
// A claim is registered under its SOURCE activity rather than its own id: the
// claim row has no page to open, so a chip citing it would lead nowhere.
func knownRecords(in Input) map[string]string {
	known := map[string]string{in.Recipient.ID: citePerson}
	if in.Deal != nil {
		known[in.Deal.ID] = citeDeal
	}
	for _, claim := range in.Claims {
		known[claim.SourceID] = citeActivity
	}
	for _, act := range in.Recent {
		known[act.ID] = citeActivity
	}
	return known
}

// The citable record kinds, DERIVED from the contract's own enum rather than
// re-spelled: a literal copy would let a contract rename leave the filter
// matching a type the wire no longer carries — a citation that silently stops
// grounding.
var (
	citeDeal     = string(crmcontracts.OrganizationBriefEvidenceEntityTypeDeal)
	citeActivity = string(crmcontracts.OrganizationBriefEvidenceEntityTypeActivity)
	citePerson   = string(crmcontracts.OrganizationBriefEvidenceEntityTypePerson)
)
