// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The certification case for brief_ranking/rank.
//
// It certifies the shipped path rather than a description of it: the request
// comes from briefs.RankRequest, the reply is read by briefs.ParseRankOrder and
// bounded by briefs.BoundToCandidates — the same three the ranker itself runs,
// because the ranker was changed to call them. A case that rebuilt any of them
// would measure a copy, and a copy stays green through the change that breaks
// the original.
//
// What the expectation MEANS here: the deals that must LEAD the re-ordered
// queue, best-first, named by the labels the fixture gives them. It is a prefix
// claim, not a whole order — a rep acts on the top of their morning queue, and
// pinning the tail would fail a good re-order for disagreeing about the deal
// nobody was going to open.
//
// It is deliberately NOT "the result is a permutation of the candidates".
// BoundToCandidates already guarantees that for every reply, including one that
// says nothing at all, so a scenario asserting it would pass forever without the
// model contributing anything. The two things production does not guarantee are
// the ones this case measures: WHICH deals lead, and whether the model returned
// a complete order on its own rather than one the ranker had to repair.
//
// The fixture names its candidates by LABEL and never by id. Production takes
// each id from a deal row, and the model is being asked to return those ids
// back — so an id supplied by the corpus would be an id whoever authored the
// expected reply could write into it, and a model echoing one it was handed
// would be indistinguishable from one that ranked the right deal. Prepare mints
// the ids, the labels stay corpus-side, and the prompt therefore stays what it
// is: ids and numbers, nothing a human wrote.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/compose/briefs"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// briefRankingFixture is one deterministic candidate queue, in the order the
// §10.1 fold hands the ranker: composite descending.
type briefRankingFixture struct {
	Candidates []briefRankingCandidate `json:"candidates"`
}

// briefRankingCandidate is one candidate in exactly what the ranker renders: the
// five factors and the composite they fold to. The composite is stated rather
// than recomputed here because the fold belongs to the deterministic layer, not
// to this site — the ranker is handed the number.
//
// Evidence row ids are deliberately absent. The evidence-or-omit gate runs
// before and after the L2 pass and never inside it, so neither the prompt nor
// the bounding reads them; a fixture carrying them would describe a field this
// site cannot use.
type briefRankingCandidate struct {
	Label       string  `json:"label"`
	Winnability float64 `json:"winnability"`
	Revenue     float64 `json:"revenue"`
	Timing      float64 `json:"timing"`
	Momentum    float64 `json:"momentum"`
	Warmth      float64 `json:"warmth"`
	Composite   float64 `json:"composite"`
}

// briefRankingCases serves the one site that re-orders a rep's morning queue.
type briefRankingCases struct{}

func (briefRankingCases) Site() aitasks.Site {
	return aitasks.Site{
		Task:    ai.TaskBriefRanking,
		Variant: "rank",
		Kind:    ai.SiteKindOneShot,
	}
}

// Prepare turns one candidate queue and the deals the scenario says must lead it
// into a runnable case, MINTING an id per candidate.
//
//nolint:ireturn // PreparedCase IS the seam: one implementation per site behind the one interface the cert lane runs.
func (briefRankingCases) Prepare(fixture, expected json.RawMessage) (aitasks.PreparedCase, error) {
	var f briefRankingFixture
	if err := json.Unmarshal(fixture, &f); err != nil {
		return nil, fmt.Errorf("brief_ranking/rank: the fixture is not the shape this site takes: %w", err)
	}
	if err := refuseUnrankableQueue(f); err != nil {
		return nil, err
	}
	var want []string
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, fmt.Errorf(
			"brief_ranking/rank: the expected answer is not a best-first list of candidate labels: %w", err)
	}
	if err := refuseUnreachableRanking(want, f); err != nil {
		return nil, err
	}
	candidates := make([]briefs.BriefQueueItem, 0, len(f.Candidates))
	label := make(map[ids.UUID]string, len(f.Candidates))
	for _, c := range f.Candidates {
		dealID := ids.NewV7()
		label[dealID] = c.Label
		candidates = append(candidates, briefs.BriefQueueItem{
			DealID:    dealID,
			Composite: c.Composite,
			Features: briefs.BriefFeatureVector{
				Winnability: c.Winnability,
				Revenue:     c.Revenue,
				Timing:      c.Timing,
				Momentum:    c.Momentum,
				Warmth:      c.Warmth,
			},
		})
	}
	return &briefRankingCase{candidates: candidates, label: label, expected: want}, nil
}

// refuseUnrankableQueue names a fixture the deterministic layer could never have
// produced. A queue of fewer than two is its own order and returns unread, so the
// model is never called at all; a queue whose composites climb is not the
// descending §10.1 candidate order, and against a queue in no known order an
// expectation about which deal LEADS says nothing about the model's judgment. A
// candidate the corpus cannot name — blank or repeated — is one no expectation
// could refer to.
func refuseUnrankableQueue(f briefRankingFixture) error {
	if len(f.Candidates) < 2 {
		return fmt.Errorf(
			"brief_ranking/rank: the fixture supplies %d candidates, and a queue of fewer than two is returned unread — the model is never called",
			len(f.Candidates))
	}
	seen := make(map[string]bool, len(f.Candidates))
	for i, c := range f.Candidates {
		switch {
		case strings.TrimSpace(c.Label) == "":
			return fmt.Errorf("brief_ranking/rank: the fixture's candidate at position %d carries no label to rank it by", i+1)
		case seen[c.Label]:
			return fmt.Errorf("brief_ranking/rank: the fixture labels two candidates %q, so an expectation naming it means neither", c.Label)
		case i > 0 && c.Composite > f.Candidates[i-1].Composite:
			return fmt.Errorf(
				"brief_ranking/rank: the fixture lists %q (composite %g) above %q (composite %g), which the deterministic composite-descending order never hands the ranker",
				f.Candidates[i-1].Label, f.Candidates[i-1].Composite, c.Label, c.Composite)
		}
		seen[c.Label] = true
	}
	return nil
}

// refuseUnreachableRanking names an expectation the ranker can never satisfy.
// The queue it returns is always a permutation of the candidates, so a label the
// fixture does not carry, a label expected twice, and a ranking longer than the
// queue are each unreachable; an empty expectation is reachable by every reply,
// which measures just as little. Each would measure nothing for as long as it
// stayed in the corpus. Naming it here costs a parse; finding it later costs a
// paid run.
func refuseUnreachableRanking(want []string, f briefRankingFixture) error {
	if len(want) == 0 {
		return errors.New("brief_ranking/rank: the scenario expects no ranked deal, so no reply could disagree with it")
	}
	if len(want) > len(f.Candidates) {
		return fmt.Errorf(
			"brief_ranking/rank: the scenario expects %d ranked deals where the fixture supplies %d candidates",
			len(want), len(f.Candidates))
	}
	carried := make(map[string]bool, len(f.Candidates))
	for _, c := range f.Candidates {
		carried[c.Label] = true
	}
	expected := make(map[string]bool, len(want))
	for _, label := range want {
		switch {
		case !carried[label]:
			return fmt.Errorf(
				"brief_ranking/rank: the scenario expects %q, which the fixture does not supply, and the queue only ever contains its own candidates",
				label)
		case expected[label]:
			return fmt.Errorf(
				"brief_ranking/rank: the scenario expects %q twice, and the queue ranks every candidate exactly once", label)
		}
		expected[label] = true
	}
	return nil
}

// briefRankingCase is one candidate queue ready to be re-ordered, closed over the
// minted ids, the labels the corpus knows them by, and the deals the scenario
// says must lead.
type briefRankingCase struct {
	candidates []briefs.BriefQueueItem
	label      map[ids.UUID]string
	expected   []string
}

// Run issues the one request this site sends.
func (c *briefRankingCase) Run(ctx context.Context, completer aitasks.Completer) (aitasks.Trace, error) {
	req := briefs.RankRequest(c.candidates)
	trace := aitasks.Trace{Requests: []model.Request{req}}
	resp, err := completer.Complete(ctx, req)
	if err != nil {
		return trace, fmt.Errorf("brief_ranking/rank: %w", err)
	}
	trace.Output = resp.Text
	return trace, nil
}

// Evaluate applies the ranker's own steps in the ranker's own order — read the
// reply, then bound it to the candidates — and only then asks whether the queue
// leads with the deals the scenario expects. The order is the meaning: a reply
// the ranker could not read has no ranking to disagree with.
//
// A reply the ranker had to repair is OutcomeInvalid, not a wrong answer. The
// prompt asks for EVERY given id exactly once; a reply that omits, invents or
// repeats one is unusable as given, and the bounding silently completing it is
// what makes that invisible in production. Grading it as a ranking would let a
// model that named nothing pass any scenario whose expected leader already leads
// the deterministic order.
func (c *briefRankingCase) Evaluate(trace aitasks.Trace) aitasks.Outcome {
	order, err := briefs.ParseRankOrder(trace.Output)
	if err != nil {
		return aitasks.Outcome{
			Result: aitasks.OutcomeInvalid,
			Detail: fmt.Sprintf("unparseable model output: %v", err),
		}
	}
	ranked := briefs.BoundToCandidates(order, c.candidates)
	if repaired := c.repaired(order, ranked); repaired != "" {
		return aitasks.Outcome{Result: aitasks.OutcomeInvalid, Detail: repaired}
	}
	if disagreements := c.disagreements(ranked); len(disagreements) > 0 {
		return aitasks.Outcome{Result: aitasks.OutcomeWrongAnswer, Detail: strings.Join(disagreements, "; ")}
	}
	return aitasks.Outcome{Result: aitasks.OutcomeAccepted}
}

// repaired names what the bounding had to do for this reply, in the one place it
// can be seen: the difference between what the model said and the queue the
// ranker built from it. The comparison reads production's own output rather than
// re-deciding which ids were known — a second copy of that decision is the copy
// that stops failing when the first one changes.
//
// The bounded queue always holds every candidate exactly once, so a reply of the
// same length that agrees with it position by position is one the ranker took
// verbatim.
func (c *briefRankingCase) repaired(order []ids.UUID, ranked []briefs.BriefQueueItem) string {
	if len(order) != len(ranked) {
		return fmt.Sprintf(
			"the model ordered %d ids where the queue holds %d candidates, so the ranker completed the order itself",
			len(order), len(ranked))
	}
	for i := range order {
		if ranked[i].DealID != order[i] {
			return fmt.Sprintf(
				"the model's id at position %d is not a candidate it was given, or one it had already ranked", i+1)
		}
	}
	return ""
}

// disagreements names every leading position the queue does not fill the way the
// scenario says. All of them, not the first: a run that led with the right deal
// and then inverted the next two is not the near miss one line would read as.
func (c *briefRankingCase) disagreements(ranked []briefs.BriefQueueItem) []string {
	var out []string
	for i, want := range c.expected {
		if got := c.label[ranked[i].DealID]; got != want {
			out = append(out, fmt.Sprintf("position %d ranks %q where the scenario expects %q", i+1, got, want))
		}
	}
	return out
}
