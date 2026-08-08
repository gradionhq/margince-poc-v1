// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentquota

// The question a human is asked when an agent crosses a releasable threshold,
// and the answer that widens the window.
//
// It lives HERE, in the package that owns the counters, because two modules read
// it and neither may import the other: the tool surface writes one when the gate
// refuses a call, and the approvals engine reads it back when the human says
// continue. A struct per side would be two spellings of one stored document, and
// the day they disagree is the day a human's yes releases a counter they were
// never shown.

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ReleaseProposal is the staged proposal's payload: what the agent has spent,
// against what, in which window.
//
// Observed and Limit are the sentence the approval screen shows — "this agent
// has been handed 2,431 of its 2,000 records for this window; continue?" — and
// they are stored rather than re-read at decision time on purpose. The human
// answers the question they were ASKED; re-reading the counter would let the
// number move between the asking and the answering, and the release would then
// be granted against a figure nobody saw.
type ReleaseProposal struct {
	Counter  Counter `json:"counter"`
	Observed int     `json:"observed"`
	Limit    int     `json:"limit"`
	// Allowance is what approving ADDS — the configured threshold, not the
	// effective one, which have differed since the first release.
	Allowance int `json:"allowance"`
	// Bucket is the window, decimal, as a STRING. The approvals engine's
	// proposal identity is a map of string values and is matched against the
	// stored payload by jsonb containment, so a numeric window here would be
	// refused at staging and — worse, if it were ever accepted on one side only
	// — would stop matching the identity it is supposed to deduplicate on.
	Bucket string `json:"bucket"`
	// Passport names WHOSE window this is, and it is part of the identity below
	// rather than decoration. Two agents lent by two different humans in one
	// workspace cross the same counter in the same window constantly; without
	// this the two questions are byte-identical, the second joins the first's
	// row, and whichever lender answers releases only the other's passport —
	// leaving one agent refused with no question anybody can see.
	//
	// It is NOT authority. applyQuotaRelease takes the passport from the staged
	// row's own passport_id, stamped from the authenticated principal, precisely
	// so an edited payload cannot re-aim a release.
	Passport string `json:"passport"`
	// Tool is the call that was refused, for the screen to name. It is not
	// authority: releasing the window releases the counter, not one tool.
	Tool string `json:"tool"`
}

// NewReleaseProposal builds the proposal one refusal asks about.
func NewReleaseProposal(reading Reading, passport ids.UUID, tool string) ReleaseProposal {
	return ReleaseProposal{
		Counter: reading.Counter, Observed: reading.Observed,
		Limit: reading.Limit, Allowance: reading.Allowance,
		Bucket: strconv.FormatInt(reading.Bucket, 10), Passport: passport.String(), Tool: tool,
	}
}

// DecodeReleaseProposal reads a staged payload back, refusing anything that is
// not a proposal this package would have written.
//
// It validates rather than trusts because the stored payload is EDITABLE: the
// approvals engine's modify-then-approve arm (ADR-0036 §4) lets a human rewrite
// a staged proposal before approving it, and the pins that arm applies are on
// entity references — of which this payload has none. So the counter is checked
// against the set a release may name at all, and the rest is judged where it is
// applied, against the live window (see Meter.Release).
func DecodeReleaseProposal(raw json.RawMessage) (ReleaseProposal, error) {
	var p ReleaseProposal
	if err := json.Unmarshal(raw, &p); err != nil {
		return ReleaseProposal{}, fmt.Errorf("agentquota: reading a staged release proposal: %w", err)
	}
	if !p.Counter.Releasable() {
		return ReleaseProposal{}, fmt.Errorf(
			"agentquota: a staged release names %q, which is not a quota a human can release", p.Counter)
	}
	return p, nil
}

// Window is the bucket this proposal was staged for. A payload whose window is
// unreadable releases NOTHING: it names no window, and picking the current one
// for it would widen a window nobody was shown.
func (p ReleaseProposal) Window() (int64, error) {
	bucket, err := strconv.ParseInt(p.Bucket, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("agentquota: a staged release names window %q, which is not a window: %w", p.Bucket, err)
	}
	return bucket, nil
}

// Identity is the proposal's logical identity for the approvals engine: one
// pending question per counter per window.
//
// Without it every refused call in a crossed window stages its own row, so an
// agent looping on a refusal fills its human's inbox with copies of one
// question — and the human answering the third of forty has released the window
// while thirty-seven identical rows remain to be dismissed. The tool is NOT part
// of the identity: the question is about the counter, and answering it for one
// tool answers it for all of them.
func (p ReleaseProposal) Identity() (json.RawMessage, error) {
	raw, err := json.Marshal(struct {
		Counter  Counter `json:"counter"`
		Bucket   string  `json:"bucket"`
		Passport string  `json:"passport"`
	}{p.Counter, p.Bucket, p.Passport})
	if err != nil {
		return nil, fmt.Errorf("agentquota: naming a release proposal's identity: %w", err)
	}
	return raw, nil
}
