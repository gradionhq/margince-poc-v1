// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Claiming a customer record: putting one's own name on a person, company or
// lead. Customer identity is workspace-readable and an ownerless row is
// nobody's to change, so the claim is the step between finding a record and
// working it. It is one row update under the same write shape as every other
// mutation — the row, its audit row (`assign`) and its updated event — and it
// never takes a record away from somebody: a row owned by another seat is
// claimable only by someone who could already change it.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// RecordClaim is what a claim answers: the row, and who owns it now.
type RecordClaim struct {
	RecordType string
	RecordID   ids.UUID
	OwnerID    ids.UUID
	Version    int64
}

// claimableTables are the record types this module owns that a seat may
// claim. A deal is claimed through the deals module, which owns its table.
var claimableTables = map[string]bool{entityPerson: true, entityOrganization: true, entityLead: true}

// ClaimRecord makes the calling human the owner of one person, organization
// or lead. Human-only: owning a customer record is a person's accountability,
// not an agent's. ifVersion, when given, is the If-Match compare.
func (s *Store) ClaimRecord(ctx context.Context, recordType string, id ids.UUID, ifVersion *int64) (RecordClaim, error) {
	if !claimableTables[recordType] {
		return RecordClaim{}, &InvalidRecordTypeError{RecordType: recordType}
	}
	if err := auth.RequireHuman(ctx); err != nil {
		return RecordClaim{}, err
	}
	if err := auth.Require(ctx, recordType, principal.ActionUpdate); err != nil {
		return RecordClaim{}, err
	}
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return RecordClaim{}, err
	}
	out := RecordClaim{RecordType: recordType, RecordID: id, OwnerID: actor.UserID}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		claim, auditID, err := storekit.ClaimOwnership(ctx, tx, recordType, id, actor.UserID, ifVersion)
		if err != nil {
			return err
		}
		out.Version = claim.Version
		if !claim.Changed {
			return nil
		}
		return emitOwnerChanged(ctx, tx, auditID, recordType, id, actor.UserID)
	})
	return out, err
}

// emitOwnerChanged ships the record's own updated event with the owner delta,
// the shape every consumer of <record>.updated already reads.
func emitOwnerChanged(ctx context.Context, tx pgx.Tx, auditID ids.UUID, recordType string, id, me ids.UUID) error {
	changed := map[string]any{"owner_id": me}
	switch recordType {
	case entityPerson:
		return storekit.EmitEvent(ctx, tx, auditID, id, crmcontracts.PublicEventPersonUpdated{ChangedFields: changed})
	case entityOrganization:
		return storekit.EmitEvent(ctx, tx, auditID, id, crmcontracts.PublicEventOrganizationUpdated{ChangedFields: changed})
	case entityLead:
		return storekit.EmitEvent(ctx, tx, auditID, id, crmcontracts.PublicEventLeadUpdated{ChangedFields: changed})
	}
	return fmt.Errorf("people: no updated event for record type %q", recordType)
}

// InvalidRecordTypeError refuses a claim on a record type nobody can own.
type InvalidRecordTypeError struct{ RecordType string }

func (e *InvalidRecordTypeError) Error() string {
	return "record type " + e.RecordType + " cannot be claimed"
}

// FieldFault answers the refusal as a 422 on record_type.
func (e *InvalidRecordTypeError) FieldFault() (field, code, message string) {
	return "record_type", "invalid_record_type", e.Error()
}
