package learn

import (
	"fmt"

	"github.com/gradionhq/margince/cli/craft/gate"
)

// A CRAFT-DISPUTE routes ONE contested finding to human adjudication. It is not a
// merge override: the residue gate keeps the merge blocked until the marker is
// resolved, whichever way adjudication goes. Adjudication only decides whether the
// finding becomes a negative example (dismissed) or stands (reverted to CRAFT-FIX).

// Status is where a dispute sits in adjudication.
type Status string

const (
	// StatusOpen is a dispute awaiting human adjudication.
	StatusOpen Status = "open"
	// StatusDismissed marks a finding adjudged wrong → negative example.
	StatusDismissed Status = "dismissed"
	// StatusUpheld marks a finding that stands → revert to CRAFT-FIX.
	StatusUpheld Status = "upheld"
)

// Dispute is one contested finding awaiting adjudication.
type Dispute struct {
	FindingID string `json:"finding_id"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Reason    string `json:"reason"`
	Status    Status `json:"status"`
}

// DisputesFromMarkers builds the adjudication queue from the CRAFT-DISPUTE markers
// found in the tree. CRAFT-FIX markers are not disputes and are ignored here.
func DisputesFromMarkers(markers []gate.Marker) []Dispute {
	var out []Dispute
	for _, m := range markers {
		if m.Kind != gate.KindDispute {
			continue
		}
		out = append(out, Dispute{
			FindingID: m.ID, File: m.File, Line: m.Line, Reason: m.Reason, Status: StatusOpen,
		})
	}
	return out
}

// Decision is the human adjudicator's call on a dispute.
type Decision string

const (
	// Dismiss adjudicates the finding as a false positive.
	Dismiss Decision = "dismiss"
	// Uphold adjudicates the finding as correct; the agent must fix it.
	Uphold Decision = "uphold"
)

// Resolution is the outcome of adjudicating a dispute. There is deliberately no
// "merge" outcome: adjudication never merges the PR by itself — the loop still
// runs through the residue gate. RevertToCraftFix tells the local agent to put the
// CRAFT-FIX marker back and fix the code.
type Resolution struct {
	Dispute          Dispute
	Signal           Signal // the adjudication learning signal (the gold signal)
	RevertToCraftFix bool
}

// Resolve adjudicates a dispute into a learning signal and an updated status. A
// dismiss makes the finding a negative example; an uphold confirms it (positive)
// and instructs a revert to CRAFT-FIX. Neither merges the PR.
func Resolve(d Dispute, decision Decision) (Resolution, error) {
	prov := fmt.Sprintf("dispute:%s@%s:%d", d.FindingID, d.File, d.Line)
	switch decision {
	case Dismiss:
		d.Status = StatusDismissed
		return Resolution{
			Dispute: d,
			Signal:  Signal{Type: SignalAdjudication, FindingID: d.FindingID, Polarity: Negative, Provenance: prov, Detail: d.Reason},
		}, nil
	case Uphold:
		d.Status = StatusUpheld
		return Resolution{
			Dispute:          d,
			Signal:           Signal{Type: SignalAdjudication, FindingID: d.FindingID, Polarity: Positive, Provenance: prov, Detail: d.Reason},
			RevertToCraftFix: true,
		}, nil
	default:
		return Resolution{}, fmt.Errorf("unknown decision %q", decision)
	}
}
