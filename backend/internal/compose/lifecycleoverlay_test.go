// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The lifecycle tools reach their owning module's entry point directly rather
// than through the datasource seam, which is what makes a tool and its REST
// route one behaviour — and what takes them off the path of the REST-only
// overlay write guard. So the obligation that guard carries has to be
// re-derived on the tool path, from the same two sets, not maintained as a list
// here.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// lifecycleToolVerbs are the verbs whose tools bypass the provider by calling a
// module store directly (RegisterLifecycleTools). Adding a fourth without
// deciding its overlay answer is exactly what this gate is for.
var lifecycleToolVerbs = []string{"relink_activity", "disqualify_lead", "advance_project_phase"}

// TestEveryUnservableRecordWriteVerbIsGuardedOnItsToolPath: a lifecycle verb
// the overlay provider cannot serve must refuse on the TOOL path too. REST
// refuses it in middleware; a tool that committed to the empty native table
// instead would be the silent divergence AC-OV-2 forbids.
func TestEveryUnservableRecordWriteVerbIsGuardedOnItsToolPath(t *testing.T) {
	needsGuard := map[string]bool{}
	for _, verb := range lifecycleToolVerbs {
		if _, servable := overlayWriteVerbs[verb]; servable {
			continue
		}
		if overlayRecordWriteTools[verb] {
			needsGuard[verb] = true
		}
	}
	if len(needsGuard) == 0 {
		t.Fatal("no lifecycle verb resolved as an unservable record write — this gate asserted nothing")
	}
	// One guard per verb that needs one. A verb the derivation adds without a
	// wrapper here fails below rather than shipping unguarded.
	guarded := map[string]func(overlayModeChecker) error{
		"disqualify_lead": func(mode overlayModeChecker) error {
			_, err := nativeOnlyDisqualifier(mode, refusingDisqualifier{}).DisqualifyLead(context.Background(), ids.NewV7())
			return err
		},
	}
	for verb := range needsGuard {
		call, wired := guarded[verb]
		if !wired {
			t.Errorf("%s is a record write the overlay provider cannot serve, and its tool calls the module "+
				"store directly — wrap its seam in a native-only guard (nativeonlytools.go) so the tool "+
				"refuses where the REST route already does", verb)
			continue
		}
		if err := call(overlayMode()); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
			t.Errorf("%s in overlay mode = %v, want ErrUnsupportedBySoR", verb, err)
		}
		if err := call(nativeMode()); !errors.Is(err, errSeamReached) {
			t.Errorf("%s in native mode = %v, want the seam reached", verb, err)
		}
	}
}

// errSeamReached proves the guard let the call THROUGH in native mode — a
// guard that refused both ways would pass a one-sided assertion.
var errSeamReached = errors.New("seam reached")

type refusingDisqualifier struct{}

func (refusingDisqualifier) DisqualifyLead(context.Context, ids.UUID) (json.RawMessage, error) {
	return nil, errSeamReached
}
