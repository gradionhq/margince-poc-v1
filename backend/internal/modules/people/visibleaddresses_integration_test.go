// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Resolving an address to a contact is a READ of that contact, so it carries
// both halves of the gate: the object grant answers whether this caller may
// read people at all, and the row scope answers which ones. An account-started
// send asks this question about addresses a human typed, which is why the
// object half matters — a seat with no person grant must not be able to
// confirm that an address is on somebody's record.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// seedAddress puts one address on one person, the way an import or a capture
// leaves it behind.
func (e *privacyEnv) seedAddress(t *testing.T, person ids.PersonID, email string) {
	t.Helper()
	ctx := e.as(e.owner, principal.RowScopeAll)
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO person_email (workspace_id, person_id, email, source, captured_by)
			VALUES ($1, $2, $3, 'gmail:seed', 'connector:gmail')`,
			e.ws, person, email)
		return err
	}); err != nil {
		t.Fatalf("seeding the address: %v", err)
	}
}

// withoutPersonGrant binds a caller whose row scope reaches everything and
// whose object grants do NOT include person — the seat that would otherwise
// learn an address is on file without being allowed to read contacts.
func (e *privacyEnv) withoutPersonGrant() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.owner.String(), UserID: e.owner,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects: map[string]principal.ObjectGrant{
				"organization": {Read: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
}

func TestVisibleAddressesResolvesAnAddressOnAContactTheCallerCanRead(t *testing.T) {
	e := setupCapturePrivacy(t)
	person := e.capturePerson(t, "workspace")
	e.seedAddress(t, person, "buyer@resolve.test")

	ctx := e.as(e.owner, principal.RowScopeAll)
	var found map[string]bool
	if err := e.store.tx(ctx, func(tx pgx.Tx) (err error) {
		found, err = VisibleAddresses(ctx, tx, []string{"Buyer@Resolve.Test"})
		return err
	}); err != nil {
		t.Fatalf("VisibleAddresses: %v", err)
	}

	// Asked in mixed case on purpose: person_email stores lowercase, and a
	// human types whatever they like.
	if !found["buyer@resolve.test"] {
		t.Fatalf("address not resolved (%v); a contact the caller can read carries it", found)
	}
}

func TestVisibleAddressesRefusesACallerWithNoPersonGrant(t *testing.T) {
	e := setupCapturePrivacy(t)
	person := e.capturePerson(t, "workspace")
	e.seedAddress(t, person, "buyer@resolve.test")

	ctx := e.withoutPersonGrant()
	err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, resolveErr := VisibleAddresses(ctx, tx, []string{"buyer@resolve.test"})
		return resolveErr
	})

	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("resolving addresses without the person grant = %v, want ErrPermissionDenied", err)
	}
}

// An address on a contact this caller's row scope excludes answers exactly
// like an address nobody has. Telling the two apart would disclose the
// existence of a record through a composer field.
func TestVisibleAddressesHidesAnAddressOnAnUnreachableContact(t *testing.T) {
	e := setupCapturePrivacy(t)
	// Owner-visible and owned by e.owner, so capture privacy hides it from
	// everyone else — even the admin, who is not on the owner's team.
	person := e.capturePerson(t, "owner")
	e.seedAddress(t, person, "private@resolve.test")

	ctx := e.as(e.admin, principal.RowScopeAll)
	var found map[string]bool
	if err := e.store.tx(ctx, func(tx pgx.Tx) (err error) {
		found, err = VisibleAddresses(ctx, tx, []string{"private@resolve.test"})
		return err
	}); err != nil {
		t.Fatalf("VisibleAddresses: %v", err)
	}

	if found["private@resolve.test"] {
		t.Fatal("an owner-private contact's address resolved for a caller who cannot read that contact")
	}
}
