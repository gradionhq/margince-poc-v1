// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// What a proposal was read out of, so confirming it is a check rather than a
// vote of confidence.
//
// Evidence is per CLAIM, not per approval: a proposal that asserts three things
// carries three elements, each naming the record and — where that record's body
// is line-addressed — the exact lines behind that one claim. A reviewer can
// then disagree with one claim without having to re-read everything.
//
// It is deliberately generic. Transcript proposals are the first kind to
// populate it, but nothing here knows what a transcript is, so every other
// staging kind gains evidence by filling the same field.

import (
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// evidenceSourceTypes is the closed set crm.yaml's Approval.evidence[].source_type
// declares. A value outside it is refused at staging: the read side would hand
// the client an enum member its generated type has no constant for.
var evidenceSourceTypes = map[string]bool{
	"activity": true, "deal": true, "signal": true, "relationship": true, "page": true,
}

// maxEvidenceSnippet bounds one quoted fragment. Evidence is a pointer back to
// the source, not a copy of it — a snippet longer than this is being used to
// re-tell the record rather than to locate a claim inside it.
const maxEvidenceSnippet = 500

// Evidence is one claim's backing material.
type Evidence struct {
	// Snippet is the fragment as it reads in the source, quoted not paraphrased.
	Snippet string `json:"evidence_snippet"`
	// SourceType and SourceID are the polymorphic pointer to the record read.
	// Both empty is allowed for a claim whose source is the proposal's own
	// target, already named on the approval row.
	SourceType string   `json:"source_type,omitempty"`
	SourceID   ids.UUID `json:"-"`
	// SourceLines are 1-based line numbers within the source record's body, for
	// a body that is line-addressed (ADR-0058: line N is the Nth newline-split
	// segment of activity.body). Empty for a source that is not.
	//
	// Positions, not content: they stay true for exactly as long as the body
	// does, which is why the transcript write path re-normalizes a body PATCH
	// instead of letting the two drift.
	SourceLines []int `json:"source_lines,omitempty"`
}

// evidenceJSON is the persisted shape. It exists because ids.UUID marshals as
// text and the zero id must persist as JSON null rather than the nil UUID
// string, which would read back as a pointer to a record that does not exist.
type evidenceJSON struct {
	Snippet     string    `json:"evidence_snippet"`
	SourceType  *string   `json:"source_type"`
	SourceID    *ids.UUID `json:"source_id"`
	SourceLines []int     `json:"source_lines,omitempty"`
}

// validateEvidence refuses evidence that could not be checked by the human it
// is shown to. Staging is the one gate: an unreadable citation is worse than no
// citation, because it reads as corroboration.
func validateEvidence(evidence []Evidence) error {
	for i, e := range evidence {
		switch {
		case e.Snippet == "":
			return fmt.Errorf("crmapprovals: evidence[%d] has no snippet; quote the fragment the claim was read from", i)
		case len(e.Snippet) > maxEvidenceSnippet:
			return fmt.Errorf("crmapprovals: evidence[%d] snippet is %d bytes, over the %d-byte cap; cite the fragment, do not re-tell the record",
				i, len(e.Snippet), maxEvidenceSnippet)
		case e.SourceType != "" && !evidenceSourceTypes[e.SourceType]:
			return fmt.Errorf("crmapprovals: evidence[%d] source_type %q is not one of activity, deal, signal, relationship, page", i, e.SourceType)
		case e.SourceType == "" && !e.SourceID.IsZero():
			return fmt.Errorf("crmapprovals: evidence[%d] names a source id with no source_type, so nothing can resolve it", i)
		case e.SourceType != "" && e.SourceID.IsZero():
			return fmt.Errorf("crmapprovals: evidence[%d] names source_type %q with no source id", i, e.SourceType)
		}
		for _, line := range e.SourceLines {
			if line < 1 {
				return fmt.Errorf("crmapprovals: evidence[%d] cites line %d; lines are 1-based positions in the source body", i, line)
			}
		}
	}
	return nil
}

// marshalEvidence renders evidence for the approval row. Empty evidence
// persists as SQL NULL (nil RawMessage), never as [] — "nothing was read"
// and "it was read and nothing backed it" are different answers, and the
// column is nullable so the first can be told from the second.
func marshalEvidence(evidence []Evidence) (json.RawMessage, error) {
	if len(evidence) == 0 {
		return nil, nil
	}
	if err := validateEvidence(evidence); err != nil {
		return nil, err
	}
	out := make([]evidenceJSON, 0, len(evidence))
	for _, e := range evidence {
		item := evidenceJSON{Snippet: e.Snippet, SourceLines: e.SourceLines}
		if e.SourceType != "" {
			item.SourceType = &e.SourceType
			id := e.SourceID
			item.SourceID = &id
		}
		out = append(out, item)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("crmapprovals: rendering evidence: %w", err)
	}
	return raw, nil
}
