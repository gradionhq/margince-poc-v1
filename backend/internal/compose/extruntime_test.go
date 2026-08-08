// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The lifetime and the scope of the per-call Runtime, without a database:
// both properties are decided before a connection is ever taken from the
// pool, so they are pinned here rather than in the integration suite. What
// the Runtime does once it IS live — pin a workspace, wall off a sibling
// unit's secret namespace — is a property of real SQL under real RLS and
// lives in extruntime_integration_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// invokeTool adapts ONE handler-bearing declaration exactly as the boot does
// and calls it once. It is the only route to a Runtime in this package's
// tests, deliberately: an extension never constructs one either.
func invokeTool(t *testing.T, unit string, h extension.ToolHandler) {
	t.Helper()
	adapted, err := adaptExtensionTool(extension.Name(unit), extension.Tool{
		Name: "probe", Description: unitToolDescription, Version: "1.0.0",
		Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,
		Handle: h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapted.Handle(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

// TestRuntimeFailsClosedAfterHandlerReturns: Runtime wraps call-scoped
// resources. A handler that stashes it in a package var and uses it later
// must fail, not silently work against a released transaction — and it must
// fail that way on BOTH capabilities, because runtime.go promises it of both.
func TestRuntimeFailsClosedAfterHandlerReturns(t *testing.T) {
	// A non-nil pool, so ErrRuntimeExpired can only be the release talking:
	// against an unwired role every call below refuses anyway, and the test
	// would pass without a lifetime at all. Nothing here reaches the pool —
	// both refusals are decided before a connection is taken.
	BindExtensionRuntime(&pgxpool.Pool{}, nil)
	t.Cleanup(func() { BindExtensionRuntime(nil, nil) })

	var escaped extension.Runtime
	invokeTool(t, "demo", func(_ context.Context, rt extension.Runtime, _ json.RawMessage) (json.RawMessage, error) {
		escaped = rt
		return json.RawMessage(`{}`), nil
	})

	if _, err := escaped.Secrets().Get(context.Background(), "k"); !errors.Is(err, extension.ErrRuntimeExpired) {
		t.Fatalf("retained Runtime still served a secret read: err=%v", err)
	}
	// The Secrets VALUE outlives the call too when a handler stashes that
	// instead of the Runtime, so the guard cannot live on Runtime.Secrets().
	// All six verbs, because runtime.go promises EVERY method — and a wall
	// with one gate left open is not a wall. The user-scoped trio is checked
	// with a well-formed UUID, so a refusal here can only be the lifetime and
	// never the id parse that would otherwise front-run it.
	stale := escaped.Secrets()
	member := extension.UserID("0195d3f2-0000-7000-8000-000000000001")
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"Get":        func() error { _, err := stale.Get(ctx, "k"); return err },
		"Put":        func() error { return stale.Put(ctx, "k", []byte("v")) },
		"Delete":     func() error { return stale.Delete(ctx, "k") },
		"GetUser":    func() error { _, err := stale.GetUser(ctx, member, "k"); return err },
		"PutUser":    func() error { return stale.PutUser(ctx, member, "k", []byte("v")) },
		"DeleteUser": func() error { return stale.DeleteUser(ctx, member, "k") },
	} {
		if err := call(); !errors.Is(err, extension.ErrRuntimeExpired) {
			t.Errorf("a Secrets taken during the call still served %s: err=%v", name, err)
		}
	}

	ran := false
	err := escaped.Tx(context.Background(), func(context.Context, extension.Tx) error {
		ran = true
		return nil
	})
	if !errors.Is(err, extension.ErrRuntimeExpired) {
		t.Fatalf("retained Runtime still opened a transaction: err=%v", err)
	}
	if ran {
		t.Fatal("a released Runtime ran the transaction callback before refusing")
	}
}

// TestRuntimeIsScopedToTheInvokingUnit. Core builds the Runtime and knows
// which unit it is invoking, so a handler cannot reach another unit's
// namespace. Two halves, because the property has two halves:
//
//   - the WIRING: the Runtime the adapter mints carries the name of the unit
//     whose declaration carried the handler, not the tool's own verb and not
//     some ambient default. Get this wrong and every wall below it is drawn
//     around the wrong namespace.
//   - the SHAPE: there is no re-scoping method on the published type, and no
//     parameter through which a unit name could arrive. This is the half the
//     brief's stub gestured at, and it is checkable — a unit name is a string,
//     so a Runtime method (or a callback it hands out) that takes one is
//     exactly the surface this test exists to refuse.
//
// What neither half can show is that the wall HOLDS in the database; that is
// TestRuntimeSecretsCannotReachAnotherUnitsNamespace, over real RLS.
func TestRuntimeIsScopedToTheInvokingUnit(t *testing.T) {
	var got extension.Runtime
	invokeTool(t, "alpha", func(_ context.Context, rt extension.Runtime, _ json.RawMessage) (json.RawMessage, error) {
		got = rt
		return json.RawMessage(`{}`), nil
	})
	call, ok := got.(*callRuntime)
	if !ok {
		t.Fatalf("the adapter handed the handler a %T, not the core's per-call runtime", got)
	}
	if call.unit != "alpha" {
		t.Fatalf("Runtime is scoped to unit %q, want the invoking unit alpha", call.unit)
	}

	rt := reflect.TypeOf((*extension.Runtime)(nil)).Elem()
	for i := range rt.NumMethod() {
		m := rt.Method(i)
		if named := stringParam(m.Type); named != "" {
			t.Fatalf("extension.Runtime.%s takes a %s — a unit name is a string, so this is a parameter "+
				"through which a handler could ask to be re-scoped", m.Name, named)
		}
	}
}

// stringParam reports the first string-kinded parameter of fn, descending one
// level into a callback parameter (Tx hands the unit a func, and a unit name
// could arrive there just as easily). It returns the type's name, or "".
func stringParam(fn reflect.Type) string {
	for i := range fn.NumIn() {
		switch in := fn.In(i); in.Kind() {
		case reflect.String:
			return in.String()
		case reflect.Func:
			if named := stringParam(in); named != "" {
				return named
			}
		default:
		}
	}
	return ""
}

// TestRuntimeRefusesBeforeTouchingAnUnwiredPool: a role that never bound the
// runtime dependencies has no pool to open a transaction on. The refusal is
// by name, at the seam, rather than a nil dereference three frames down in
// pgx — and it is NOT ErrRuntimeExpired, which would tell a unit author to
// look at their own handler's lifetime for a deployment's wiring fault.
func TestRuntimeRefusesBeforeTouchingAnUnwiredPool(t *testing.T) {
	rt := runtimeFor("demo", nil, nil)
	if _, err := rt.Secrets().Get(context.Background(), "k"); !errors.Is(err, errExtensionRuntimeUnwired) {
		t.Fatalf("unwired Secrets().Get = %v, want errExtensionRuntimeUnwired", err)
	}
	if err := rt.Tx(context.Background(), func(context.Context, extension.Tx) error { return nil }); !errors.Is(err, errExtensionRuntimeUnwired) {
		t.Fatalf("unwired Tx = %v, want errExtensionRuntimeUnwired", err)
	}
}

// TestBoundExtensionRuntimeDepsReachTheHandler: the boot binds one pool and
// one custodian per process, and the per-call Runtime is built over them.
// Without this the whole tier is inert — every handler would meet the unwired
// refusal above.
func TestBoundExtensionRuntimeDepsReachTheHandler(t *testing.T) {
	// A non-nil pool value is enough: nothing here issues a query, and the
	// property under test is that the binding is what the adapter reads.
	pool := &pgxpool.Pool{}
	vault := keyvault.NewMemory()
	BindExtensionRuntime(pool, vault)
	t.Cleanup(func() { BindExtensionRuntime(nil, nil) })

	var got *callRuntime
	invokeTool(t, "alpha", func(_ context.Context, rt extension.Runtime, _ json.RawMessage) (json.RawMessage, error) {
		got, _ = rt.(*callRuntime)
		return json.RawMessage(`{}`), nil
	})
	if got == nil {
		t.Fatal("the adapter did not hand the handler the core's per-call runtime")
	}
	if got.pool != pool || got.vault != vault {
		t.Fatalf("the per-call Runtime was built over pool=%p vault=%v, not the bound pair", got.pool, got.vault)
	}
}
