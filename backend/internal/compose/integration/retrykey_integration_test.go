// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A4b end to end: a mutating tool called twice under one retry key acts once.
//
// Everything below the transport is real — the composed registry, the admission
// gate, the claim table, the datasource seam the replay re-reads through. That
// is the point: the unit tests prove the registry's branches against a scripted
// claim store and the adapter suite proves the row, but only this proves the
// WIRING — that composing the surface installs a claim store at all, and that a
// replay's evidence probe reaches the same provider a live read does. Both are
// silent failures otherwise: a surface with no claim store refuses keys, and one
// with no reader refuses replays, and either reads as "idempotency is broken"
// long after the composition changed.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func createPersonWithKey(ctx context.Context, t *testing.T, registry *agents.Registry, name, key string) (json.RawMessage, error) {
	t.Helper()
	args := `{"record_type":"person","fields":{"full_name":"` + name + `"}`
	if key != "" {
		args += `,"idempotency_key":"` + key + `"`
	}
	return registry.Invoke(ctx, "create_record", json.RawMessage(args+`}`))
}

func TestATooledCreateRetriedUnderOneKeyCreatesOneRecord(t *testing.T) {
	e := Setup(t)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)

	first, err := createPersonWithKey(ctx, t, registry, "Retried Once", "create-k-1")
	if err != nil {
		t.Fatalf("the first keyed create: %v", err)
	}
	second, err := createPersonWithKey(ctx, t, registry, "Retried Once", "create-k-1")
	if err != nil {
		t.Fatalf("the retry under the same key: %v", err)
	}

	// One record, and the retry answered with the FIRST call's result rather
	// than a second record's.
	if n := e.WsCount(t, `SELECT count(*) FROM person WHERE full_name = $1`, "Retried Once"); n != 1 {
		t.Fatalf("the retried create wrote %d people, want 1", n)
	}
	if string(first) != string(second) {
		t.Fatalf("the retry answered differently:\nfirst  %s\nsecond %s", first, second)
	}
	// A DIFFERENT call under the same key is refused rather than replayed —
	// answering the first result here would report a create that never happened.
	if _, err := createPersonWithKey(ctx, t, registry, "Something Else", "create-k-1"); err == nil {
		t.Fatal("a different payload under a spent key was accepted")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM person WHERE full_name = $1`, "Something Else"); n != 0 {
		t.Fatalf("the refused call wrote %d people", n)
	}
	// And the key is per-call: a new key makes the same arguments a new record.
	if _, err := createPersonWithKey(ctx, t, registry, "Retried Once", "create-k-2"); err != nil {
		t.Fatalf("a fresh key on the same arguments: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM person WHERE full_name = $1`, "Retried Once"); n != 2 {
		t.Fatalf("a fresh key produced %d people in total, want 2", n)
	}
}

// A recorded result is a receipt, and it must not outlive the authority it was
// produced under. Revocation binds mid-session, and a retry is where that
// promise is easiest to lose: the answer already exists, so nothing about
// handing it back looks like a read.
func TestAReplayIsRefusedOnceTheCallerCanNoLongerReadWhatItCarries(t *testing.T) {
	e := Setup(t)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)

	out, err := createPersonWithKey(ctx, t, registry, "Receipt Holder", "receipt-k-1")
	if err != nil {
		t.Fatalf("the first keyed create: %v", err)
	}
	var created struct {
		ID ids.UUID `json:"id"`
	}
	if err := json.Unmarshal(ToolPayload(t, out), &created); err != nil {
		t.Fatalf("unreadable create_record answer %s: %v", out, err)
	}
	// The record leaves this caller's reach. Art. 17 erasure is the honest
	// spelling — the row survives with its owner intact, and every live read
	// path refuses it, which is exactly the case a frozen snapshot would sail
	// past.
	e.WsExec(t, `UPDATE person SET archived_at = now() WHERE id = $1`, created.ID)

	replay, err := createPersonWithKey(ctx, t, registry, "Receipt Holder", "receipt-k-1")
	if err == nil {
		t.Fatalf("the recorded answer was replayed after its record left the caller's reach: %s", replay)
	}
	// Existence-hiding: the refusal says the record is not there, not that it
	// used to be.
	if strings.Contains(strings.ToLower(err.Error()), "archiv") {
		t.Errorf("the refusal describes what changed rather than answering not-found: %v", err)
	}
}
