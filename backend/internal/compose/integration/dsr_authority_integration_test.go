// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Who may see the data-subject-request queue. A request row names the subject
// who exercised an Art. 15/17 right and records what was decided about them,
// so the queue is the admin's — and an unbounded row scope is not a stand-in
// for that authority. Three seeded roles hold scope `all`, so a gate that
// asked about scope handed the whole queue to read_only, the least-privileged
// role in the matrix.

import (
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestSubjectRequestQueueIsTheAdminsAlone(t *testing.T) {
	e := Setup(t)
	store := consent.NewStore(e.DB())

	if _, err := store.CreateDSR(e.Admin(), consent.CreateDSRInput{
		Kind:       "access",
		SubjectRef: "subject@queue.test",
		DueAt:      time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seeding a request the queue can hide: %v", err)
	}

	// Both hold row scope `all`, and ops carries the admin object grid, so
	// neither is refused for want of a grant — the role is what turns them
	// away, which is the whole point.
	for _, unbounded := range []principal.Permissions{OpsPerms, ReadOnlyPerms} {
		ctx := e.As(ids.NewV7(), []ids.UUID{e.Team1}, unbounded)
		if _, _, err := store.ListDSRs(ctx, nil, "", ""); !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Fatalf("%v lists the subject-request queue: err=%v, want permission denied", unbounded.RoleKeys, err)
		}
	}

	// A bounded rep is refused too, and for the older reason: it never held
	// the scope in the first place.
	repCtx := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	if _, _, err := store.ListDSRs(repCtx, nil, "", ""); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("bounded rep lists the subject-request queue: err=%v, want permission denied", err)
	}

	// The admin still reads the row that was seeded, so the narrowing refused
	// the roles rather than the surface.
	rows, _, err := store.ListDSRs(e.Admin(), nil, "", "")
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("admin sees an empty subject-request queue after one was filed")
	}
}
