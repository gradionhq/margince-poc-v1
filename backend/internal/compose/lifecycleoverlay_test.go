// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A record write the overlay provider cannot serve must refuse in overlay mode
// on EVERY transport, and the two transports reach it differently: REST passes
// through overlaywrite.go's middleware, while a tool that calls its owning
// module directly — which is what makes a tool and its route one behaviour —
// never touches a router. So the obligation is re-derived here for the tool
// path, from the same two sets the middleware reads, rather than from a list
// somebody has to remember to extend.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// unservableRecordWriteVerbs derives the set: a verb that writes a record
// (overlayRecordWriteTools) and has no seam verb the overlay provider can serve
// (overlayWriteVerbs) is refused for a mirrored type wherever it is reached.
func unservableRecordWriteVerbs() map[string]bool {
	verbs := map[string]bool{}
	for verb := range overlayRecordWriteTools {
		if _, servable := overlayWriteVerbs[verb]; !servable {
			verbs[verb] = true
		}
	}
	return verbs
}

// TestEveryUnservableRecordWriteVerbRefusesOnItsToolPath: each derived verb is
// registered as a tool, and reaching it in overlay mode answers the declared
// refusal — either because its seam carries a guard (disqualify_lead) or
// because it rides the Dispatcher, which refuses for it.
//
// The integration pin (compose/integration/overlay_toolsurface) drives these
// against a real overlay workspace; what this one holds is that the SET is
// derived, so a verb added to overlayRecordWriteTools without a seam the
// provider serves cannot slip past by not being listed anywhere.
func TestEveryUnservableRecordWriteVerbRefusesOnItsToolPath(t *testing.T) {
	derived := unservableRecordWriteVerbs()
	if len(derived) == 0 {
		t.Fatal("no unservable record-write verb resolved — this gate asserted nothing")
	}

	registry := NewRegistry(nil, SendPath{})
	for verb := range derived {
		if _, registered := registry.Spec(verb); !registered {
			t.Errorf("%s is an unservable record write with no registered tool — either it is not a "+
				"tool verb at all (drop it from overlayRecordWriteTools) or its tool is missing", verb)
			continue
		}
		if !overlayGuardedToolVerbs[verb] {
			t.Errorf("%s writes a record the overlay provider cannot serve, so an overlay workspace must "+
				"meet a declared refusal on the TOOL path too. Say which mechanism refuses it in "+
				"overlayGuardedToolVerbs, and drive it in the integration pin (nativeOnlyAgentTools).", verb)
		}
	}
}

// overlayGuardedToolVerbs records, for each derived verb, that its tool path has
// a refusal and which mechanism provides it. Two mechanisms exist and the
// distinction is load-bearing: a tool that rides the Dispatcher inherits the
// provider's own refusal, while one that calls a module store directly needs a
// guard at its seam — the second kind is the one that silently wrote to an
// empty native table before.
var overlayGuardedToolVerbs = map[string]bool{
	// Dispatcher-routed: the overlay provider declares the write unsupported.
	"advance_deal": true, "merge_records": true, "promote_lead": true,
	// Seam-guarded: the tool calls people.Store directly, so nativeOnlyDisqualifier
	// carries the refusal the REST middleware makes for the route.
	"disqualify_lead": true,
}

// TestTheDisqualifyGuardRefusesInOverlayAndPassesInNative: the one seam guard,
// exercised on both sides — a guard that refused in either mode would satisfy a
// one-sided assertion while breaking every native workspace.
func TestTheDisqualifyGuardRefusesInOverlayAndPassesInNative(t *testing.T) {
	guard := nativeOnlyDisqualifier(overlayMode(), refusingDisqualifier{})
	if _, err := guard.DisqualifyLead(context.Background(), ids.NewV7()); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Errorf("overlay mode = %v, want ErrUnsupportedBySoR", err)
	}

	guard = nativeOnlyDisqualifier(nativeMode(), refusingDisqualifier{})
	if _, err := guard.DisqualifyLead(context.Background(), ids.NewV7()); !errors.Is(err, errSeamReached) {
		t.Errorf("native mode = %v, want the seam reached", err)
	}
}

// errSeamReached proves the guard let the call THROUGH in native mode.
var errSeamReached = errors.New("seam reached")

type refusingDisqualifier struct{}

func (refusingDisqualifier) DisqualifyLead(context.Context, ids.UUID) (json.RawMessage, error) {
	return nil, errSeamReached
}
