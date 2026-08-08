// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// The growth-fit model lane, and the floor it degrades to.
//
// The model proposes a band and the claims behind it. It does NOT decide
// whether that band may be served: the completeness gate in Assess runs
// afterwards and can lower it to `unknown` or cap it at `moderate`. A model
// that is confident about a company we hold four facts on is exactly the
// failure DOSS-FORM-2 exists to catch, so its confidence is an input to the
// decision rather than the decision.
//
// The workspace's own offering reaches the model through the task's company
// context, never through the input. That asymmetry is deliberate: the model
// must READ what we sell to judge fit, and must never CITE it, because our own
// profile is not a record the reader can open (DOSS-AC-6). The grounding filter
// enforces the second half — the known set holds target-side records only, so a
// sentence citing our profile has nowhere to land and is dropped.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// Completer is the model seam: the growth-fit lane, or nil. A deployment with
// no lane configured is a declared posture, not an error.
type Completer interface {
	Complete(ctx context.Context, req model.Request) (model.Response, error)
}

const growthFitSystem = `You assess how well one company fits what WE sell, from a JSON summary of that company and a description of our own offering.
Return ONLY a JSON object: {"band":"strong|moderate|weak","positive_factors":[CLAIM],"negative_factors":[CLAIM],"whitespace":[CLAIM],"objections":[CLAIM],"recommended_angle":CLAIM}.
A CLAIM is {"text":"...","nature":"fact|assessment|recommendation","evidence":[{"entity_type":"organization|fact|profile_field","entity_id":"..."}]}.
Judge only on evidence. Do NOT report a band of "unknown" and do not comment on how much data you were given — a separate step counts that and can overrule your band. Give the band the evidence you have actually supports.
Label every claim. A FACT restates something the summary says and cites the record it came from. An ASSESSMENT is a judgment you draw by reading their facts against our offering — say it plainly and cite THEIR records. A RECOMMENDATION is one concrete move.
positive_factors and negative_factors are why they do or do not fit. whitespace is what we sell that they do not appear to buy yet. objections are what they are likely to push back with. recommended_angle is the single best approach, and is always a recommendation.
Our offering describes US. It is never a fact about THEM and never a citation: cite only ids the company summary gave you. A claim about the company itself cites the organization.
Put ids ONLY in evidence. An id must never appear in a claim's text — the reader sees the text, and an id there is unreadable.
Never invent a fact. If the summary does not say it, you may still ASSESS it, but then it is an assessment and must be labelled one.
Write one claim per sentence, plainly.`

// growthFitSystemFor names THIS call's data boundary; see promptfence.Fence.Rule.
func growthFitSystemFor(fence promptfence.Fence) string {
	return growthFitSystem + "\n" + fence.Rule("company summary")
}

// GrowthFitRequest builds the one-shot call. Exported so the AI cert case
// measures the request production actually sends rather than a copy of it.
func GrowthFitRequest(in Input) model.Request {
	fence := promptfence.New()
	return model.Request{
		System:   growthFitSystemFor(fence),
		Messages: []model.Message{{Role: "user", Content: fence.Wrap(encodeInput(in))}},
		// Our own offering is what makes this a FIT rather than a description,
		// so it is requested unconditionally — a growth fit written without it
		// is the guess the band cap exists to flag.
		IncludeCompanyContext: true,
		MaxTokens:             ai.ReasoningOutputMaxTokens,
		SecretStripper:        ai.NewSecretStripper(),
	}
}

func encodeInput(in Input) string {
	encoded, _ := json.Marshal(in) //nolint:errchkjson // Input is a plain struct of scalars; marshal cannot fail
	return string(encoded)
}

// growthFitClaims is the model's answer, before any of it is believed.
type growthFitClaims struct {
	Band             string            `json:"band"`
	PositiveFactors  []claims.Sentence `json:"positive_factors"`
	NegativeFactors  []claims.Sentence `json:"negative_factors"`
	Whitespace       []claims.Sentence `json:"whitespace"`
	Objections       []claims.Sentence `json:"objections"`
	RecommendedAngle *claims.Sentence  `json:"recommended_angle"`
}

// WriteGrowthFit assesses the company, degrading to the deterministic floor on
// any model-side failure.
//
// The floor's answer is an abstention (DOSS-PARAM-7), so a reader whose lane is
// down sees "here is what I would need to know" rather than a band nobody
// stands behind — and `generated_by` says which of the two they are reading.
func WriteGrowthFit(ctx context.Context, lane Completer, in Input,
	selfConfirmed bool, now nowFunc,
) (Assessment, crmcontracts.WrittenBy) {
	floor := Assess(in, crmcontracts.GrowthFitBandUnknown, selfConfirmed, now())
	if lane == nil {
		return floor, crmcontracts.Deterministic
	}
	assessed, err := assessWithModel(ctx, lane, in, selfConfirmed, now)
	if err != nil {
		// The declared degrade posture (on_budget_exhausted: degrade), not a
		// swallowed error. A lane that is unavailable, over budget, or
		// answering unparseable JSON must not take the panel down: the reader
		// gets the floor's abstention, labelled as the floor's.
		return floor, crmcontracts.Deterministic
	}
	return assessed, crmcontracts.Model
}

func assessWithModel(ctx context.Context, lane Completer, in Input,
	selfConfirmed bool, now nowFunc,
) (Assessment, error) {
	resp, err := lane.Complete(ctx, GrowthFitRequest(in))
	if err != nil {
		return Assessment{}, err
	}
	proposed, kept, err := ParseGrowthFit(resp.Text, in)
	if err != nil {
		return Assessment{}, err
	}

	// The model proposed; the formula decides. Assess re-counts the inputs
	// itself and may lower the band to `unknown` or cap it at `moderate` —
	// it never raises what the model said.
	out := Assess(in, proposed, selfConfirmed, now())
	if out.Band == crmcontracts.GrowthFitBandUnknown {
		// The evidence did not support judging at all, so the claims written to
		// justify a judgment are withheld with it. Serving "not enough evidence"
		// beside four confident reasons would read as a band the surface is
		// merely too shy to state.
		return out, nil
	}
	out.Claims = kept
	return out, nil
}

// ParseGrowthFit decodes the reply and keeps only what the reader can check.
// Exported so the AI cert case measures the parser production runs rather than
// a copy of it — a copy stays green through the change that breaks the
// original.
func ParseGrowthFit(reply string, in Input) (crmcontracts.GrowthFitBand, GrowthFitClaims, error) {
	var parsed growthFitClaims
	if err := json.Unmarshal([]byte(ai.Unfence(reply)), &parsed); err != nil {
		return "", GrowthFitClaims{}, fmt.Errorf("parse the growth-fit reply: %w", err)
	}
	proposed := crmcontracts.GrowthFitBand(parsed.Band)
	if _, known := bandRank[proposed]; !known {
		return "", GrowthFitClaims{}, fmt.Errorf("the growth-fit reply named no band this contract knows: %q", parsed.Band)
	}
	if proposed == crmcontracts.GrowthFitBandUnknown {
		// Abstention is the counting step's verdict, never the model's. A model
		// that answers `unknown` has declined the question it was asked, and
		// letting it through would hide a real judgment behind a figure the
		// formula did not compute.
		return "", GrowthFitClaims{}, errors.New("the growth-fit reply abstained, which is the counting step's decision to make")
	}

	known := KnownRecords(in)
	kept := GrowthFitClaims{
		PositiveFactors: claims.Keep(parsed.PositiveFactors, known, knownNature, natureFact),
		NegativeFactors: claims.Keep(parsed.NegativeFactors, known, knownNature, natureFact),
		Whitespace:      claims.Keep(parsed.Whitespace, known, knownNature, natureFact),
		Objections:      claims.Keep(parsed.Objections, known, knownNature, natureFact),
	}
	if parsed.RecommendedAngle != nil {
		if angle := claims.Keep([]claims.Sentence{*parsed.RecommendedAngle}, known, knownNature, natureFact); len(angle) == 1 {
			kept.RecommendedAngle = &angle[0]
		}
	}
	if kept.empty() {
		// A band with nothing checkable behind it is the shape this whole
		// surface exists to refuse. It is a model failure, so it degrades to
		// the floor rather than being served as a bare verdict.
		return "", GrowthFitClaims{}, errors.New("the growth-fit reply cited nothing in the company")
	}
	return proposed, kept, nil
}
