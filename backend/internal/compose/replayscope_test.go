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

// The gate's fail-closed edge, in the shapes a recorded body can actually
// arrive in. Every one of these must refuse: a body the gate cannot read is a
// body whose record it cannot name, and serving it on the strength of a parse
// failure is what this whole path exists to prevent.
func TestReplayRecordIDFailsClosedOnAnUnusableBody(t *testing.T) {
	for _, tc := range []struct{ name, body, path string }{
		{"not JSON at all", `<html>gateway error</html>`, "id"},
		{"an array where an object was expected", `[{"id":"x"}]`, "id"},
		{"the field is absent", `{"other":"x"}`, "id"},
		{"the field is null", `{"id":null}`, "id"},
		{"the field is a number, not an id", `{"id":42}`, "id"},
		{"the id is not a UUID", `{"id":"not-a-uuid"}`, "id"},
		{"a dotted path through a missing parent", `{"lead_id":"x"}`, "person.id"},
		{"a dotted path through a non-object", `{"person":"x"}`, "person.id"},
		{"an empty body", ``, "id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := recordIDAt(tc.body, tc.path); !errors.Is(err, apperrors.ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestReplayRecordIDReadsBothShapes(t *testing.T) {
	id := ids.NewV7()
	t.Run("a body that names its own record", func(t *testing.T) {
		got, err := recordIDAt(`{"id":"`+id.String()+`","full_name":"x"}`, "id")
		if err != nil || got != id {
			t.Fatalf("got (%v, %v), want (%s, nil)", got, err, id)
		}
	})
	t.Run("a body that nests it", func(t *testing.T) {
		got, err := recordIDAt(`{"merged":true,"person":{"id":"`+id.String()+`"}}`, "person.id")
		if err != nil || got != id {
			t.Fatalf("got (%v, %v), want (%s, nil)", got, err, id)
		}
	})
}

// A route whose body carries no row-scoped record is allowed through without
// a probe — the reason is recorded in its entry, and the fitness test holds
// that reason to the contract.
func TestReplayAllowsARouteWithNothingToProbe(t *testing.T) {
	if err := ensureReplayVisible(context.Background(), nil, nil, "POST /v1/products", `{"id":"x"}`); err != nil {
		t.Fatalf("err = %v, want nil — this body carries no row-scoped record", err)
	}
}

// The polymorphic arm resolves its table from the body, so a body that does
// not name one cannot be probed and must refuse before reaching SQL.
func TestReplayPolymorphicProbeRefusesWithoutItsTable(t *testing.T) {
	const grants = "POST /v1/record-grants"
	for _, body := range []string{`{"record_id":"x"}`, `{"record_type":null,"record_id":"x"}`, `{}`} {
		if err := ensureReplayVisible(context.Background(), nil, nil, grants, body); !errors.Is(err, apperrors.ErrNotFound) {
			t.Fatalf("body %s: err = %v, want ErrNotFound", body, err)
		}
	}
}

// A row-scoped route whose recorded body no longer yields its record id
// refuses before it ever opens a transaction: there is nothing to probe, and
// "cannot tell" is not "allowed".
func TestReplayRefusesBeforeQueryingWhenTheIDIsUnreadable(t *testing.T) {
	for _, tc := range []struct{ name, route, body string }{
		{"the record's own id is unusable", "PATCH /v1/people/{id}", `{"id":"garbage"}`},
		{"the referenced parent id is unusable", "POST /v1/offers/{id}/send", `{"id":"x","deal_id":"garbage"}`},
		{"the nested id is unusable", "POST /v1/leads/{id}/promote", `{"person":{"id":"garbage"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A nil pool would panic if the gate reached the database, so
			// surviving this call is itself the assertion that it did not.
			if err := ensureReplayVisible(context.Background(), nil, nil, tc.route, tc.body); !errors.Is(err, apperrors.ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
		})
	}
}
