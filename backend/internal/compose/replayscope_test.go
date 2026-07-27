// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The module-probe arm of the replay gate (API-CC-8). The approvals
// visibility RULE is approvals' own to prove; what is proven here is this
// package's wiring of it — that a refusal propagates, that a probe nobody
// wired refuses rather than waves through, and that a pass really passes.
// Without these, a probe silently disconnected at the composition root would
// look exactly like a probe that ran and approved.

import (
	"context"
	"errors"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

const approveRoute = "POST /v1/approvals/{id}/approve"

// routeCtx binds the chi route context the probe reads its id from, exactly
// as the middleware sees it mid-request.
func routeCtx(t *testing.T, pattern, param, value string) context.Context {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.RoutePatterns = []string{pattern}
	rctx.URLParams.Add(param, value)
	return context.WithValue(context.Background(), chi.RouteCtxKey, rctx)
}

func TestReplayModuleProbeDecidesTheReplay(t *testing.T) {
	approval := ids.NewV7()
	ctx := routeCtx(t, "/v1/approvals/{id}/approve", "id", approval.String())

	t.Run("a refusal from the module refuses the replay", func(t *testing.T) {
		called := false
		probes := map[string]replayProbe{probeApproval: func(_ context.Context, id ids.UUID) error {
			called = true
			if id != approval {
				t.Errorf("probe got id %s, want the approval named by the route %s", id, approval)
			}
			return apperrors.ErrNotFound
		}}
		err := ensureReplayVisible(ctx, nil, probes, approveRoute, `{"id":"`+approval.String()+`"}`)
		if !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound — an approval the caller can no longer see must not replay", err)
		}
		if !called {
			t.Error("the probe never ran, so the refusal came from somewhere else")
		}
	})

	t.Run("a pass from the module allows the replay", func(t *testing.T) {
		probes := map[string]replayProbe{probeApproval: func(context.Context, ids.UUID) error { return nil }}
		if err := ensureReplayVisible(ctx, nil, probes, approveRoute, `{}`); err != nil {
			t.Fatalf("err = %v, want nil — a still-visible approval replays", err)
		}
	})

	t.Run("a probe nobody wired refuses rather than waves through", func(t *testing.T) {
		err := ensureReplayVisible(ctx, nil, map[string]replayProbe{}, approveRoute, `{}`)
		if !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound — an unwired probe cannot show the caller may still see this", err)
		}
	})

	t.Run("an id the route does not carry refuses", func(t *testing.T) {
		blank := routeCtx(t, "/v1/approvals/{id}/approve", "other", approval.String())
		probes := map[string]replayProbe{probeApproval: func(context.Context, ids.UUID) error {
			t.Error("the probe ran on an unresolvable id")
			return nil
		}}
		if err := ensureReplayVisible(blank, nil, probes, approveRoute, `{}`); !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

// The middleware only reaches the probe for routes it can replay at all; an
// unclassified one must never pay out.
func TestReplayRefusesAnUnclassifiedRoute(t *testing.T) {
	err := ensureReplayVisible(context.Background(), nil, nil, "POST /v1/not-a-route", `{"id":"x"}`)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// The composition root must wire a probe for every route that says it needs
// one. An unwired key is not a compile error and not a runtime panic — it
// fails closed, which retires that route's replay promise silently. This is
// the assertion the hand-built probe maps above cannot make, because they are
// not the map the server runs.
func TestEveryModuleProbeIsWiredAtTheCompositionRoot(t *testing.T) {
	wired := replayProbes(nil) // keys only; nothing here calls a probe
	needed := map[string]string{}
	for route, target := range replayableOperations {
		if target.moduleProbe != "" {
			needed[target.moduleProbe] = route
		}
	}
	if len(needed) == 0 {
		t.Fatal("no route names a module probe — the extractor lost its source")
	}
	for key, route := range needed {
		if _, ok := wired[key]; !ok {
			t.Errorf("%s needs the %q probe and the composition root wires none — its replays would refuse silently", route, key)
		}
	}
	for key := range wired {
		if _, ok := needed[key]; !ok {
			t.Errorf("the composition root wires a %q probe no route asks for — delete the stale wiring", key)
		}
	}
}
