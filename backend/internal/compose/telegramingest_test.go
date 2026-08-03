// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// telegramChannelPrincipal carries the only hand-written ObjectGrant map in
// non-test product code, keyed on the tableActivity/tablePerson constants
// (replayscope.go). Nothing else asserts those keys are the object names
// platform/auth actually admits on, so a rename would otherwise surface only
// in the real-Postgres lane — loudly, but minutes later.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The workspace-channel connector holds design §6.4's fixed minimum, and the
// admission gate has to agree on both halves: the two creates the ingest path
// exercises are admitted, and nothing wider is. Asserting only the admitted
// half would also pass on a map that granted everything.
func TestTelegramChannelPrincipalAdmitsOnlyTheGrantsTheIngestPathExercises(t *testing.T) {
	ctx := principal.WithActor(context.Background(), telegramChannelPrincipal())

	// The activity the worker captures, and the person the channel ensure
	// auto-creates for an unmatched sender (design D1).
	for _, object := range []string{"activity", "person"} {
		if err := auth.Require(ctx, object, principal.ActionCreate); err != nil {
			t.Errorf("%s.create refused: %v — the grant map is keyed on a name platform/auth does not admit on",
				object, err)
		}
	}

	// Everything else, including the wider verbs on the two objects it does
	// hold: a connector that could open a deal, or rewrite the conversations it
	// captured, is not a capture identity.
	for _, refused := range []struct {
		object string
		action principal.Action
	}{
		{"deal", principal.ActionCreate},
		{"channel_connection", principal.ActionRead},
		{"activity", principal.ActionUpdate},
		{"activity", principal.ActionDelete},
		{"person", principal.ActionUpdate},
		{"person", principal.ActionDelete},
	} {
		err := auth.Require(ctx, refused.object, refused.action)
		if !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Errorf("%s.%s returned %v, want ErrPermissionDenied — the connector holds more than the ingest path exercises",
				refused.object, refused.action, err)
		}
	}
}
