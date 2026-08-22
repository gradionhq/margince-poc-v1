// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// A staging whose effect writes AGAINST a second record, named in the payload
// rather than on the target columns.
//
// The target of a project_attribution card is the activity — the row the
// effect updates — but the effect also plants a reference TO a project, and a
// human releasing it is asserting that link. Somebody who may not read that
// project must not be shown the card (it names the project) and must not be
// able to decide it (the link they release is one they could not have typed:
// the relink door probes the destination with auth.EnsureLinkTarget). This is
// the same question for the second record that decidable already asks for the
// first, so it is asked here with the same probe, and it fails closed: a
// payload that does not name the record, or names one the asking human cannot
// see, is not decidable by them.

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// payloadReference names the payload field carrying the second record and the
// table it lives in.
type payloadReference struct {
	field      string
	entityType string
}

// payloadReferences is keyed on the kind. TestEveryPayloadReferenceIsAKindWeStage
// in compose holds it to registered kinds.
var payloadReferences = map[string]payloadReference{
	KindProjectAttribution: {field: "project_id", entityType: tableProject},
}

// PayloadReferenceKinds lists the kinds carrying a second-record gate, for the
// composition layer's fitness tests.
func PayloadReferenceKinds() []string {
	kinds := make([]string, 0, len(payloadReferences))
	for kind := range payloadReferences {
		kinds = append(kinds, kind)
	}
	return kinds
}

// payloadReferenceVisible answers whether the asking human may READ the second
// record this staging names — true for a kind that names none. Visibility, not
// writability: the effect writes the TARGET; the second record is only
// referenced, and a reference is a read of it (auth.EnsureLinkTarget asks the
// same).
func payloadReferenceVisible(ctx context.Context, tx pgx.Tx, a row) (bool, error) {
	ref, gated := payloadReferences[a.Kind]
	if !gated {
		return true, nil
	}
	id, named := referencedID(a.ProposedChange, ref.field)
	if !named {
		return false, nil
	}
	return targetVisible(ctx, tx, &ref.entityType, &id)
}

// referencedID reads one uuid field off a payload, reporting whether the
// payload names one at all. A payload that is not an object — a redacted
// card, say — or whose field is missing, malformed or zero names no record
// anyone could check: that is an answer (not decidable), not an error.
func referencedID(payload json.RawMessage, field string) (ids.UUID, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return ids.Nil, false
	}
	raw, ok := fields[field]
	if !ok {
		return ids.Nil, false
	}
	var id ids.UUID
	if json.Unmarshal(raw, &id) != nil || id.IsZero() {
		return ids.Nil, false
	}
	return id, true
}
