// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A record write the overlay provider cannot serve must refuse in overlay mode
// on EVERY transport, and the transports reach it differently: REST passes
// through overlaywrite.go's middleware, while a tool that calls its owning
// module directly — which is what makes a tool and its route one behaviour —
// never touches a router. So the obligation is re-derived here for the tool
// path, from the same two sets the middleware reads.
//
// This file proves the SET and the one seam guard. That each derived verb
// actually answers ErrUnsupportedBySoR is proved where it can only be proved —
// against a real overlay workspace, in
// compose/integration/overlay/overlay_toolsurface_integration_test.go, whose
// nativeOnlyAgentTools drives every one of them through the live registry.

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// overlayPinName is the integration suite two gates in this package defer to: it
// drives every native-only verb through the live registry against a real overlay
// workspace, so the lists here can be derived from it rather than hand-kept.
const overlayPinName = "overlay_toolsurface_integration_test.go"

// overlayPin locates that suite by NAME, anywhere under integration/.
//
// Not a fixed relative path, which is what both gates used to hold: the suite
// moved once — into its own package, to give the lane another scheduling slot —
// and both gates failed on a path rather than on anything about the code. A gate
// that breaks when a file it only READS is relocated is asserting where the file
// lives, which is not the obligation it exists for.
func overlayPin(t *testing.T) string {
	t.Helper()
	var found string
	err := filepath.WalkDir("integration", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == overlayPinName {
			found = path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking integration/ for %s: %v", overlayPinName, err)
	}
	if found == "" {
		t.Fatalf("%s is nowhere under integration/ — the pin these gates defer to is gone, "+
			"so they would assert nothing", overlayPinName)
	}
	return found
}

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

// TestEveryUnservableRecordWriteVerbIsARegisteredToolTheOverlayPinDrives: each
// derived verb is a registered tool, and each is driven against a real overlay
// workspace by the integration pin. Deriving the set is what makes that pin
// total: a fifth unservable record write cannot be omitted from it silently,
// because this fails until it is named there.
func TestEveryUnservableRecordWriteVerbIsARegisteredToolTheOverlayPinDrives(t *testing.T) {
	derived := unservableRecordWriteVerbs()
	if len(derived) == 0 {
		t.Fatal("no unservable record-write verb resolved — this gate asserted nothing")
	}

	registry := NewRegistry(nil, SendPath{})
	driven := overlayPinnedToolVerbs(t)
	for verb := range derived {
		if _, registered := registry.Spec(verb); !registered {
			t.Errorf("%s is an unservable record write with no registered tool — either it is not a "+
				"tool verb at all (drop it from overlayRecordWriteTools) or its tool is missing", verb)
			continue
		}
		if !driven[verb] {
			t.Errorf("%s writes a record the overlay provider cannot serve, so an overlay workspace must "+
				"meet a declared refusal on the TOOL path too — and nothing proves it does. Add it to "+
				"nativeOnlyAgentTools in compose/integration/overlay/overlay_toolsurface_integration_test.go, "+
				"which drives each verb through the live registry against a real overlay workspace.", verb)
		}
	}
}

// overlayPinnedToolVerbs reads the verbs the integration pin drives, from the
// pin's own source — so this gate cannot be satisfied by a list that agrees
// with it while the pin covers something else.
func overlayPinnedToolVerbs(t *testing.T) map[string]bool {
	t.Helper()
	pin := overlayPin(t)
	// Two fixtures, because the two refusal mechanisms are different facts:
	// nativeOnlyAgentTools are decorator-guarded, providerRefusedRecordWrites
	// inherit the provider's own refusal. The same suite drives both.
	verbs := map[string]bool{}
	for _, fixture := range []string{"func nativeOnlyAgentTools(", "func providerRefusedRecordWrites("} {
		found, err := quotedMapKeys(pin, fixture)
		if err != nil {
			t.Fatalf("reading %s: %v", pin, err)
		}
		if len(found) == 0 {
			t.Fatalf("%s's %s drives no tools — a pin this gate defers to is empty", pin, fixture)
		}
		for verb := range found {
			verbs[verb] = true
		}
	}
	return verbs
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
