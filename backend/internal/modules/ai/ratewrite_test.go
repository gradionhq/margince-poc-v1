// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// modelRateCtx binds a human holding exactly one ai_model_rate grant row.
func modelRateCtx(g principal.ObjectGrant) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test",
		Permissions: principal.Permissions{
			RoleKeys: []string{"fixture"},
			Objects:  map[string]principal.ObjectGrant{"ai_model_rate": g},
		},
	})
}

// The upfront half of the sheet's admission pair (the fx_rate sibling carries
// the identical shape): it cannot know yet whether the write inserts or
// replaces, so EITHER write grant gets past it and a principal holding neither
// is refused here — before a pool connection is taken. The grant-specific half
// runs inside the transaction and is proven end to end in the integration lane
// (TestModelRateCreateAndUpdateGrantsGateSeparately).
func TestPrepareModelRateAdmitsEitherWriteGrant(t *testing.T) {
	in := SetModelRateInput{
		Provider: "anthropic", ModelID: "m",
		InputUsd: "1", OutputUsd: "1", CacheReadUsd: "0", CacheWriteUsd: "0",
	}
	admitted := map[string]principal.ObjectGrant{
		"create only":       {Create: true},
		"update only":       {Update: true},
		"create and update": {Create: true, Update: true},
	}
	for name, g := range admitted {
		t.Run("admits "+name, func(t *testing.T) {
			if _, err := NewRateStore(nil).prepareModelRate(modelRateCtx(g), in); err != nil {
				t.Fatalf("prepareModelRate = %v, want admitted", err)
			}
		})
	}

	refused := map[string]principal.ObjectGrant{
		"read only": {Read: true},
		"no grant":  {},
		// delete is the one verb the sheet never grants (a past-dated row
		// prices historical usage), so it must not open the write either.
		"delete only": {Delete: true},
	}
	for name, g := range refused {
		t.Run("refuses "+name, func(t *testing.T) {
			_, err := NewRateStore(nil).prepareModelRate(modelRateCtx(g), in)
			if !errors.Is(err, apperrors.ErrPermissionDenied) {
				t.Fatalf("prepareModelRate = %v, want ErrPermissionDenied", err)
			}
		})
	}
}
