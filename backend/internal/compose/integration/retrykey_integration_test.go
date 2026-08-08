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
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// createPersonWithKey builds the call through the encoder rather than by
// splicing strings: a name or key carrying a quote would otherwise emit invalid
// JSON and fail as a decode error, which reads exactly like the refusal these
// tests are trying to observe.
func createPersonWithKey(ctx context.Context, t *testing.T, registry *agents.Registry, name, key string) (json.RawMessage, error) {
	t.Helper()
	args := map[string]any{
		"record_type": "person",
		"fields":      map[string]any{"full_name": name},
	}
	if key != "" {
		args["idempotency_key"] = key
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encoding the call: %v", err)
	}
	return registry.Invoke(ctx, "create_record", encoded)
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
	// The SENTINEL, not merely an error: a claim-store failure, an envelope
	// decode failure and a budget refusal all answer non-nil, and only one of
	// them is the existence-hiding refusal this test is about.
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v (out %s), want ErrNotFound", err, replay)
	}
	// Refused, and refused BEFORE the tool — a late write would look the same
	// from the error alone.
	if n := e.WsCount(t, `SELECT count(*) FROM person WHERE full_name = $1`, "Receipt Holder"); n != 1 {
		t.Fatalf("the refused replay left %d people, want the 1 the first call created", n)
	}
	// Existence-hiding: the refusal says the record is not there, not that it
	// used to be.
	if strings.Contains(strings.ToLower(err.Error()), "archiv") {
		t.Errorf("the refusal describes what changed rather than answering not-found: %v", err)
	}
}

// The boundary the live-only probe draws, stated as a test rather than as a
// comment: a tool whose effect REMOVES its own evidence keeps the promise and
// loses the receipt.
//
// `archive_record` names exactly the record it archived, so the retry cannot
// prove the caller may still read it and refuses. Both halves are asserted,
// because only one of them is the promise: the effect must still have happened
// exactly once. Relaxing the probe to include archived rows would return the
// receipt and reopen a worse hole — Art. 17 erasure anonymizes in place and
// stamps archived_at, so the same relaxation replays pre-erasure PII.
func TestAnArchivesReceiptIsRefusedAndItsEffectStillHappensOnce(t *testing.T) {
	e := Setup(t)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)
	person := e.SeedPerson(t, "Archived Once", &e.Rep1)

	args, err := json.Marshal(map[string]any{
		"record_type": "person", "id": person.String(), "idempotency_key": "archive-k-1",
	})
	if err != nil {
		t.Fatalf("encoding the call: %v", err)
	}
	if _, err := registry.Invoke(ctx, "archive_record", args); err != nil {
		t.Fatalf("the first archive: %v", err)
	}
	archivedAt := e.WsCount(t, `SELECT count(*) FROM person WHERE id = $1 AND archived_at IS NOT NULL`, person)
	if archivedAt != 1 {
		t.Fatalf("the first call archived %d rows, want 1", archivedAt)
	}

	// The receipt is gone: its only evidence is the record it just removed.
	out, err := registry.Invoke(ctx, "archive_record", args)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("the archived record's receipt answered %v (out %s), want ErrNotFound", err, out)
	}
	// The effect happened once.
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_id = $1 AND action = 'archive'`, person); n != 1 {
		t.Fatalf("the record carries %d archive audit entries, want the 1 the first call wrote", n)
	}
	// And the refusal came from the REPLAY GATE rather than from a re-run that
	// happened to fail the same way. Neither of the two assertions above can
	// tell those apart — archiving an archived row answers not-found too, and
	// writes no second audit entry — but the claim row can: a re-run settles as
	// a FAILURE and overwrites the recorded success, so a surviving 200 means
	// the tool was never entered.
	if n := e.WsCount(t,
		`SELECT count(*) FROM idempotency_key WHERE key = $1 AND response_status = 200`, "archive-k-1"); n != 1 {
		t.Fatalf("the claim no longer carries its recorded success, so the retry reached the tool " +
			"and settled over it")
	}
}
