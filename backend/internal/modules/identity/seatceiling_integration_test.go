// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// The licensed full-seat ceiling over a real migrated Postgres: what it
// refuses, what it admits, and which rows it counts.
//
// Every case runs the real writers — InviteUser, DeactivateUser,
// ReactivateUser — because the ceiling's whole job is to sit inside their
// transactions. A test that counted rows and asserted arithmetic would pass
// with the gate deleted.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// licensedSeats is a ceiling the composition root would inject from a verified
// license granting limit seats.
func licensedSeats(limit int) SeatCeiling { return func() (int, bool) { return limit, true } }

// licenseFor binds that ceiling the way the composition root does — through the
// handler seam, which is the only thing that binds one in production. Setting
// the field here instead would prove the gate works when a test sets it, which
// is not the claim.
func licenseFor(t *testing.T, e *revocationEnv, ceiling SeatCeiling) {
	t.Helper()
	NewHandlers(e.svc).WithSeatCeiling(ceiling)
}

// seatsInUse counts what the gate counts, through the gate's OWN statement —
// the same constant the meter on the entitlement screen runs, so a test cannot
// agree with a predicate the product does not have.
//
// The workspace predicate is the test's own, and the product needs none: an
// installation serves exactly one organization, so every full seat in the
// database is one of its seats. This suite seeds a workspace per environment
// into a database that holds every other suite's, which is the one place that
// assumption does not hold.
func seatsInUse(t *testing.T, e *revocationEnv) int {
	t.Helper()
	var used int
	if err := e.owner.QueryRow(context.Background(),
		fullSeatsInUseQuery).Scan(&used); err != nil {
		t.Fatalf("counting full seats in use: %v", err)
	}
	return used
}

// inviteOneMore invites a member nobody else in this suite will collide with.
func inviteOneMore(t *testing.T, e *revocationEnv, who string) error {
	t.Helper()
	_, _, err := e.svc.InviteUser(e.wsCtx(e.admin), e.admin, InviteUserInput{
		Email:       who + "-" + ids.NewV7().String()[24:] + "@seatceiling.test",
		DisplayName: who,
		Role:        "rep",
	})
	return err
}

func TestSeatCeilingRefusesAnInviteWhenEveryLicensedSeatIsInUse(t *testing.T) {
	e := setupRevocationEnv(t, "seat-ceiling-full")
	used := seatsInUse(t, e)
	licenseFor(t, e, licensedSeats(used))

	err := inviteOneMore(t, e, "overflow")
	if !errors.Is(err, apperrors.ErrSeatLimitReached) {
		t.Fatalf("invite at the ceiling: err = %v, want %v", err, apperrors.ErrSeatLimitReached)
	}
	// Both numbers reach the admin. "No seats left" on its own is a refusal
	// nobody can act on: which of the two ways out to take — free a seat or
	// license another — is a decision that needs the count to make.
	for _, want := range []string{fmt.Sprint(used), "deactivate", "licensed seat count"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not carry %q", err, want)
		}
	}
	// The refusal has to be the whole transaction's. A member created and then
	// refused would leave the installation one seat over its license with a row
	// nobody meant to write.
	if got := seatsInUse(t, e); got != used {
		t.Errorf("seats in use after the refused invite = %d, want %d", got, used)
	}
}

// The boundary is the licensed number itself, checked from both sides in one
// test: an off-by-one either refuses the seat the license paid for or hands out
// one it did not.
func TestSeatCeilingAdmitsTheLastLicensedSeatAndRefusesTheNext(t *testing.T) {
	e := setupRevocationEnv(t, "seat-ceiling-last")
	used := seatsInUse(t, e)
	licenseFor(t, e, licensedSeats(used+1))

	if err := inviteOneMore(t, e, "last-seat"); err != nil {
		t.Fatalf("invite into the last licensed seat: %v", err)
	}
	if got := seatsInUse(t, e); got != used+1 {
		t.Fatalf("seats in use = %d after taking the last one, want %d", got, used+1)
	}
	if err := inviteOneMore(t, e, "one-too-many"); !errors.Is(err, apperrors.ErrSeatLimitReached) {
		t.Errorf("invite past the ceiling: err = %v, want %v", err, apperrors.ErrSeatLimitReached)
	}
}

// Withdrawn access frees a seat. An admin who suspends somebody to make room —
// which is what an admin at the ceiling will do — has to actually get the room.
func TestSeatCeilingCountsNeitherASuspendedNorADeactivatedSeat(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withdraw func(t *testing.T, e *revocationEnv)
	}{
		{
			name: "deactivated through the real writer",
			withdraw: func(t *testing.T, e *revocationEnv) {
				if err := e.svc.DeactivateUser(e.wsCtx(e.admin), e.admin,
					DeactivateUserInput{UserID: e.member.UserID}); err != nil {
					t.Fatalf("deactivate: %v", err)
				}
			},
		},
		{
			// Suspension has no writer on this surface today — it is set by the
			// lockout path — so the status is written where the schema holds it.
			name: "suspended",
			withdraw: func(t *testing.T, e *revocationEnv) {
				if _, err := e.owner.Exec(context.Background(),
					`UPDATE app_user SET status = 'suspended' WHERE id = $1`, e.member.UserID); err != nil {
					t.Fatalf("suspend: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setupRevocationEnv(t, "seat-ceiling-withdrawn")
			atTheCeiling := seatsInUse(t, e)
			licenseFor(t, e, licensedSeats(atTheCeiling))
			if err := inviteOneMore(t, e, "before"); !errors.Is(err, apperrors.ErrSeatLimitReached) {
				t.Fatalf("the installation is not at its ceiling to begin with: err = %v", err)
			}

			tc.withdraw(t, e)

			if got := seatsInUse(t, e); got != atTheCeiling-1 {
				t.Fatalf("seats in use after withdrawing one = %d, want %d", got, atTheCeiling-1)
			}
			if err := inviteOneMore(t, e, "after"); err != nil {
				t.Errorf("invite into the freed seat: %v", err)
			}
		})
	}
}

// Reactivation is a seat coming back into use, so it answers to the same
// ceiling an invite does. Nothing here demotes or evicts the member: they stay
// deactivated until the installation has a seat for them (P7).
func TestSeatCeilingRefusesReactivatingAFullSeatIntoAFullInstallation(t *testing.T) {
	e := setupRevocationEnv(t, "seat-ceiling-reactivate")
	if err := e.svc.DeactivateUser(e.wsCtx(e.admin), e.admin,
		DeactivateUserInput{UserID: e.member.UserID}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	withoutThem := seatsInUse(t, e)
	licenseFor(t, e, licensedSeats(withoutThem))

	err := e.svc.ReactivateUser(e.wsCtx(e.admin), e.admin, e.member.UserID)
	if !errors.Is(err, apperrors.ErrSeatLimitReached) {
		t.Fatalf("reactivate at the ceiling: err = %v, want %v", err, apperrors.ErrSeatLimitReached)
	}
	var status string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT status FROM app_user WHERE id = $1`, e.member.UserID).Scan(&status); err != nil {
		t.Fatalf("reading the member's status: %v", err)
	}
	if status != "deactivated" {
		t.Errorf("the refused reactivation left status %q, want deactivated", status)
	}

	licenseFor(t, e, licensedSeats(withoutThem+1))
	if err := e.svc.ReactivateUser(e.wsCtx(e.admin), e.admin, e.member.UserID); err != nil {
		t.Errorf("reactivate with a seat licensed for them: %v", err)
	}
}

// A read seat is unlimited and never metered (A62/ADR-0047), so returning one
// to active asks the full-seat ceiling nothing.
func TestSeatCeilingDoesNotHoldAReadSeatToTheFullSeatGrant(t *testing.T) {
	e := setupRevocationEnv(t, "seat-ceiling-read")
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE app_user SET seat_type = 'read' WHERE id = $1`, e.member.UserID); err != nil {
		t.Fatalf("make the member a read seat: %v", err)
	}
	if err := e.svc.DeactivateUser(e.wsCtx(e.admin), e.admin,
		DeactivateUserInput{UserID: e.member.UserID}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	// Every full seat the installation has is licensed, and no more.
	licenseFor(t, e, licensedSeats(seatsInUse(t, e)))

	if err := e.svc.ReactivateUser(e.wsCtx(e.admin), e.admin, e.member.UserID); err != nil {
		t.Errorf("reactivating a read seat against a full full-seat grant: %v", err)
	}
}

// What an unlicensed installation and an uncapped license have in common: no
// number to hold anybody to. Both reach identity as the same answer, and a
// service nobody wired is the third spelling of it.
func TestSeatCeilingAdmitsEverySeatWhenNothingCapsThem(t *testing.T) {
	for name, ceiling := range map[string]SeatCeiling{
		"a license that caps no seats": func() (int, bool) { return 0, false },
		"no license posture wired":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			e := setupRevocationEnv(t, "seat-ceiling-uncapped")
			licenseFor(t, e, ceiling)
			for _, who := range []string{"first", "second", "third"} {
				if err := inviteOneMore(t, e, who); err != nil {
					t.Fatalf("invite %s under %s: %v", who, name, err)
				}
			}
		})
	}
}
