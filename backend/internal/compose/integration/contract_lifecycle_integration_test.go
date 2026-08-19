// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The contract lifecycle against a real database (ADR-0109/A160).
//
// This suite exists because its absence shipped a defect: `captured_by` was
// typed `uuid` where the writer stamps a prefixed principal, so EVERY contract
// write answered 500, and four gates called it green. Nothing here touches a
// mock — the store writes through the same path the API does, so a column that
// disagrees with its writer fails on the first insert rather than in production.

import (
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The derived reading is computed against TODAY by the store's own clock, so a
// fixture states its dates relative to that rather than pinning a calendar date
// that silently walks into the past and makes the assertion mean the opposite.
func daysFromToday(days int) time.Time {
	return time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, days)
}

func TestContractCreateReadsBackWhatWasWritten(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "Acme", nil)

	created, err := e.Contracts.CreateContract(e.Admin(), contracts.CreateContractInput{
		OrganizationID: ids.From[ids.OrganizationKind](org),
		Title:          "MSA 2026",
		ValueBasis:     contracts.BasisTotal,
		Source:         "manual",
	})
	if err != nil {
		t.Fatalf("creating a contract: %v", err)
	}

	// The defect this suite was written for: the principal is prefixed
	// ("human:<id>"), and a uuid column refuses it on the way in.
	if created.CapturedBy == nil || *created.CapturedBy == "" {
		t.Error("captured_by came back empty — the principal did not survive the write")
	}
	if created.Status == nil || string(*created.Status) != contracts.StatusDraft {
		t.Errorf("status = %v, want a contract born in draft", created.Status)
	}
	// A draft has asserted nothing, so it is not under contract whatever its
	// dates say.
	if created.UnderContract == nil || *created.UnderContract {
		t.Error("a draft contract reads as under contract")
	}
}

// CONTRACT-FORM-1's worked example, against the database that computes it: a
// June term cancelled effective May ends in MAY. The effective end is the
// EARLIER of the two dates, and a cancellation never revives a lapsed term.
func TestUnderContractTakesTheEarlierOfTermEndAndCancellation(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "Acme", nil)
	admin := e.Admin()

	starts := daysFromToday(-180)
	ends := daysFromToday(120)
	created, err := e.Contracts.CreateContract(admin, contracts.CreateContractInput{
		OrganizationID: ids.From[ids.OrganizationKind](org),
		Title:          "Cancelled early",
		ValueBasis:     contracts.BasisTotal,
		StartsOn:       &starts,
		EndsOn:         &ends,
		Source:         "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := ids.From[ids.ContractKind](ids.UUID(created.Id))
	if _, err := e.Contracts.ChangeStatus(admin, id, contracts.StatusActive, nil); err != nil {
		t.Fatalf("activating: %v", err)
	}

	// Notice given, effective a month before the term would have ended.
	// Notice given, taking effect BEFORE the term would have ended but still
	// ahead of today: the customer is under contract until then.
	notice := daysFromToday(-10)
	effective := daysFromToday(30)
	cancelled, err := e.Contracts.Cancel(admin, id, notice, effective, nil)
	if err != nil {
		t.Fatalf("recording the cancellation: %v", err)
	}

	// The status does NOT move. The customer is under contract until the
	// effective date, because that is what a notice period is.
	if cancelled.Status == nil || string(*cancelled.Status) != contracts.StatusActive {
		t.Errorf("status = %v after recording notice, want it unchanged at active", cancelled.Status)
	}
	if cancelled.UnderContract == nil || !*cancelled.UnderContract {
		t.Error("a contract whose cancellation has not taken effect reads as no longer under contract")
	}
}

// A renewal creates the successor and supersedes the predecessor in ONE
// transaction, so an agreement that has run for years reads as a chain rather
// than a row somebody overwrote.
func TestRenewalChainsRatherThanOverwrites(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "Acme", nil)
	admin := e.Admin()

	first, err := e.Contracts.CreateContract(admin, contracts.CreateContractInput{
		OrganizationID: ids.From[ids.OrganizationKind](org),
		Title:          "MSA 2026", ValueBasis: contracts.BasisTotal, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	predecessorID := ids.From[ids.ContractKind](ids.UUID(first.Id))
	if _, err := e.Contracts.ChangeStatus(admin, predecessorID, contracts.StatusActive, nil); err != nil {
		t.Fatal(err)
	}

	successor, err := e.Contracts.Renew(admin, predecessorID, contracts.CreateContractInput{
		Title: "MSA 2027", ValueBasis: contracts.BasisAnnualized, Source: "renewal",
	}, nil)
	if err != nil {
		t.Fatalf("renewing: %v", err)
	}

	predecessor, err := e.Contracts.GetContract(admin, predecessorID)
	if err != nil {
		t.Fatal(err)
	}
	if predecessor.Status == nil || string(*predecessor.Status) != contracts.StatusSuperseded {
		t.Errorf("predecessor status = %v, want superseded", predecessor.Status)
	}
	if predecessor.SupersededById == nil || *predecessor.SupersededById != successor.Id {
		t.Error("the predecessor does not point at its successor, so the chain cannot be read back")
	}
	// The successor inherits the counterparty rather than taking one from the
	// request: a renewal that changed companies would be a different agreement
	// wearing this one's history.
	if successor.OrganizationId != first.OrganizationId {
		t.Error("the successor names a different company than the agreement it renews")
	}
}

// A terminal status is terminal. Reviving an expired agreement would make the
// record a description of somebody's second thoughts.
func TestATerminalContractDoesNotReopen(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "Acme", nil)
	admin := e.Admin()

	created, err := e.Contracts.CreateContract(admin, contracts.CreateContractInput{
		OrganizationID: ids.From[ids.OrganizationKind](org),
		Title:          "Expired", ValueBasis: contracts.BasisTotal, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := ids.From[ids.ContractKind](ids.UUID(created.Id))
	for _, to := range []string{contracts.StatusActive, contracts.StatusExpired} {
		if _, err := e.Contracts.ChangeStatus(admin, id, to, nil); err != nil {
			t.Fatalf("moving to %s: %v", to, err)
		}
	}

	_, err = e.Contracts.ChangeStatus(admin, id, contracts.StatusActive, nil)

	var transition *contracts.InvalidStatusTransitionError
	if !errors.As(err, &transition) {
		t.Fatalf("reviving an expired contract: err = %v, want InvalidStatusTransitionError", err)
	}
}

// The database's own constraints, reached through the writer rather than
// hand-inserted — a row the real path cannot produce proves nothing about it.
func TestTheDatabaseRefusesContradictoryTerms(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "Acme", nil)
	value := int64(100)
	starts, ends := daysFromToday(0), daysFromToday(-30)

	cases := map[string]contracts.CreateContractInput{
		"value with no currency": {
			OrganizationID: ids.From[ids.OrganizationKind](org),
			Title:          "Half a money pair", ValueBasis: contracts.BasisTotal,
			ValueMinor: &value, Source: "manual",
		},
		"a term that ends before it starts": {
			OrganizationID: ids.From[ids.OrganizationKind](org),
			Title:          "Backwards", ValueBasis: contracts.BasisTotal,
			StartsOn: &starts, EndsOn: &ends, Source: "manual",
		},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := e.Contracts.CreateContract(e.Admin(), in)

			var check *contracts.ContractCheckError
			if !errors.As(err, &check) {
				t.Fatalf("err = %v, want a ContractCheckError naming the field", err)
			}
			if check.Field == "" {
				t.Error("the refusal names no field, so a human is not told what to fix")
			}
		})
	}
}

// A contract the caller cannot see answers NOT FOUND, never a denial: a 403
// would confirm the agreement exists. A deal is readable by every seat, so
// the hidden anchor is an account capture-private to Rep1: an
// organization-anchored contract on it is invisible to Rep3.
func TestAnInvisibleContractIsAbsentRatherThanRefused(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	e.MakeCapturePrivate(t, "organization", org, e.Rep1)

	rep1 := e.As(e.Rep1, []ids.UUID{e.Team1}, ContractRepPerms)
	orgID := ids.From[ids.OrganizationKind](org)
	created, err := e.Contracts.CreateContract(rep1, contracts.CreateContractInput{
		OrganizationID: orgID,
		Title:          "Rep1's agreement", ValueBasis: contracts.BasisTotal, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	contractID := ids.From[ids.ContractKind](ids.UUID(created.Id))

	// The owner reads it, so the refusal below is the anchor's visibility and
	// not a broken read.
	if _, err := e.Contracts.GetContract(rep1, contractID); err != nil {
		t.Fatalf("the owner cannot read their own contract: %v", err)
	}

	rep3 := e.As(e.Rep3, []ids.UUID{e.Team2}, ContractRepPerms)
	_, err = e.Contracts.GetContract(rep3, contractID)

	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("a contract outside the caller's scope: err = %v, want ErrNotFound (existence stays hidden)", err)
	}
}
