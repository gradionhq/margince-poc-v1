// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package contracts

// The asserted transitions: status, cancellation, renewal — and the typed
// errors the transport maps onto contract codes.
//
// Every transition here is something a human said. No date reaches these
// functions as a trigger, which is the invariant the whole chapter rests on.

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// terminalStatuses are the states an agreement does not come back out of. A
// correction to a term is a field edit under audit; reviving an expired or
// cancelled agreement would make the record a description of somebody's second
// thoughts rather than of what happened.
var terminalStatuses = []string{StatusExpired, StatusCancelled, StatusSuperseded}

// InvalidStatusTransitionError reports a transition the state machine refuses.
type InvalidStatusTransitionError struct {
	From string
	To   string
}

func (e *InvalidStatusTransitionError) Error() string {
	return fmt.Sprintf("a contract cannot move from %s to %s", e.From, e.To)
}

// ContractCheckError reports a database constraint that a human can fix by
// changing what they typed. It names the field rather than the constraint, so
// the message a caller sees is about their agreement and not about our schema.
type ContractCheckError struct {
	Field  string
	Reason string
}

func (e *ContractCheckError) Error() string { return e.Reason }

// contractCheckError maps a constraint name onto the field the human should
// look at. An unmapped constraint keeps its name out of the message: a client
// never learns our schema from an error.
func contractCheckError(constraint string) error {
	switch constraint {
	case "contract_value_pair":
		return &ContractCheckError{Field: "value_minor",
			Reason: "a contract value needs its currency, and a currency needs its value"}
	case "contract_fx_pair":
		return &ContractCheckError{Field: "fx_rate_to_base",
			Reason: "a frozen conversion rate needs the date it was frozen on"}
	case "contract_term_order":
		return &ContractCheckError{Field: "ends_on",
			Reason: "a term cannot end before it starts"}
	case "contract_cancellation_within_term":
		return &ContractCheckError{Field: "cancellation_effective_on",
			Reason: "a cancellation cannot take effect after the term already ends"}
	case "contract_cancellation_order":
		return &ContractCheckError{Field: "cancellation_effective_on",
			Reason: "a cancellation cannot take effect before notice was given"}
	case "contract_superseded_agrees":
		return &ContractCheckError{Field: "status",
			Reason: "a superseded contract names its successor, and only a superseded one may"}
	default:
		return &ContractCheckError{Field: "", Reason: "the contract's dates or amounts contradict each other"}
	}
}

// ChangeStatus asserts a new status.
func (s *Store) ChangeStatus(ctx context.Context, id ids.ContractID, to string, ifVersion *int64) (crmcontracts.Contract, error) {
	if err := auth.Require(ctx, contractObject, principal.ActionUpdate); err != nil {
		return crmcontracts.Contract{}, err
	}

	var out crmcontracts.Contract
	err := s.tx(ctx, func(tx pgx.Tx) error {
		existing, err := writableContract(ctx, tx, id, s.today())
		if err != nil {
			return err
		}
		if err := refuseInvalidTransition(statusOf(existing), to); err != nil {
			return err
		}
		out, err = applyStatusTx(ctx, tx, id, existing, to, nil, ifVersion, s.today())
		return err
	})
	return out, err
}

// refuseInvalidTransition holds the two rules the state machine has: a
// terminal state is terminal, and `superseded` is reached only by renewing
// (which writes the successor pointer in the same transaction, so asserting it
// directly would leave the pointer and the status disagreeing).
func refuseInvalidTransition(from, to string) error {
	if from == to {
		return nil
	}
	if slices.Contains(terminalStatuses, from) {
		return &InvalidStatusTransitionError{From: from, To: to}
	}
	if to == StatusSuperseded {
		return &InvalidStatusTransitionError{From: from, To: to}
	}
	return nil
}

// statusOf reads the asserted status as a plain string. The generated type
// carries it as an optional pointer; a contract row always has one.
func statusOf(c crmcontracts.Contract) string {
	if c.Status == nil {
		return ""
	}
	return string(*c.Status)
}

// applyStatusTx writes one status transition with its audit and event.
func applyStatusTx(ctx context.Context, tx pgx.Tx, id ids.ContractID, existing crmcontracts.Contract,
	to string, supersededBy *ids.ContractID, ifVersion *int64, asOf time.Time,
) (crmcontracts.Contract, error) {
	patch := storekit.NewPatch()
	patch.Set("status", statusOf(existing), to)
	if supersededBy != nil {
		patch.Set("superseded_by_id", existing.SupersededById, supersededBy.UUID)
	}
	if err := patch.ApplyGuarded(ctx, tx, "contract", id.UUID, ifVersion); err != nil {
		if constraint, ok := storekit.CheckViolation(err); ok {
			return crmcontracts.Contract{}, contractCheckError(constraint)
		}
		return crmcontracts.Contract{}, fmt.Errorf("apply contract status: %w", err)
	}

	auditID, err := storekit.Audit(ctx, tx, "update", contractObject, id.UUID, patch.Before(), patch.After())
	if err != nil {
		return crmcontracts.Contract{}, fmt.Errorf("audit contract status change: %w", err)
	}
	changed := crmcontracts.PublicEventContractStatusChanged{
		FromStatus:     statusOf(existing),
		ToStatus:       to,
		OrganizationId: &existing.OrganizationId,
	}
	if supersededBy != nil {
		successor := openapi_types.UUID(supersededBy.UUID)
		changed.SupersededById = &successor
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, changed); err != nil {
		return crmcontracts.Contract{}, fmt.Errorf("emit contract.status_changed: %w", err)
	}
	return readContract(ctx, tx, id, asOf)
}

// Cancel records notice of cancellation and when it takes effect, and changes
// NOTHING else. The customer stays under contract until the effective date,
// because that is what a notice period is — the status moves later, when the
// date arrives and a human or a proposal says so.
func (s *Store) Cancel(ctx context.Context, id ids.ContractID, noticeOn, effectiveOn time.Time, ifVersion *int64) (crmcontracts.Contract, error) {
	if err := auth.Require(ctx, contractObject, principal.ActionUpdate); err != nil {
		return crmcontracts.Contract{}, err
	}

	var out crmcontracts.Contract
	err := s.tx(ctx, func(tx pgx.Tx) error {
		existing, err := writableContract(ctx, tx, id, s.today())
		if err != nil {
			return err
		}
		patch := storekit.NewPatch()
		patch.Set("cancellation_notice_on", existing.CancellationNoticeOn, noticeOn)
		patch.Set("cancellation_effective_on", existing.CancellationEffectiveOn, effectiveOn)
		if err := patch.ApplyGuarded(ctx, tx, "contract", id.UUID, ifVersion); err != nil {
			if constraint, ok := storekit.CheckViolation(err); ok {
				return contractCheckError(constraint)
			}
			return fmt.Errorf("record contract cancellation: %w", err)
		}
		auditID, err := storekit.Audit(ctx, tx, "update", contractObject, id.UUID, patch.Before(), patch.After())
		if err != nil {
			return fmt.Errorf("audit contract cancellation: %w", err)
		}
		// A cancellation is a column patch, not a transition, so it rides
		// contract.updated. A consumer watching for the state change gets it
		// when the status actually moves, which is the honest moment.
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID,
			crmcontracts.PublicEventContractUpdated{ChangedFields: patch.After()}); err != nil {
			return fmt.Errorf("emit contract.updated: %w", err)
		}
		out, err = readContract(ctx, tx, id, s.today())
		return err
	})
	return out, err
}

// Renew creates the successor agreement and supersedes its predecessor, in ONE
// transaction. A renewal never mutates the agreement it replaces: an agreement
// that has run for six years reads as a chain rather than a row somebody
// overwrote five times.
func (s *Store) Renew(ctx context.Context, id ids.ContractID, successor CreateContractInput, ifVersion *int64) (crmcontracts.Contract, error) {
	if err := auth.Require(ctx, contractObject, principal.ActionCreate); err != nil {
		return crmcontracts.Contract{}, err
	}
	if err := auth.Require(ctx, contractObject, principal.ActionUpdate); err != nil {
		return crmcontracts.Contract{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Contract{}, err
	}

	var out crmcontracts.Contract
	err = s.tx(ctx, func(tx pgx.Tx) error {
		predecessor, err := writableContract(ctx, tx, id, s.today())
		if err != nil {
			return err
		}
		if err := refuseRenewalOfTerminal(statusOf(predecessor)); err != nil {
			return err
		}
		// The successor inherits the predecessor's counterparty rather than
		// taking one from the request: a renewal that changed companies would
		// be a different agreement wearing this one's history.
		successor.OrganizationID = ids.OrganizationID{UUID: ids.UUID(predecessor.OrganizationId)}

		created, err := createContractTx(ctx, tx, successor, by, s.today())
		if err != nil {
			return err
		}
		successorID := ids.ContractID{UUID: ids.UUID(created.Id)}
		if _, err := applyStatusTx(ctx, tx, id, predecessor, StatusSuperseded, &successorID, ifVersion, s.today()); err != nil {
			return err
		}
		out = created
		return nil
	})
	return out, err
}

// refuseRenewalOfTerminal keeps a renewal chain single-headed: renewing an
// agreement that is already superseded would give one predecessor two
// successors and make the chain a tree nobody can read back.
func refuseRenewalOfTerminal(status string) error {
	if status == StatusSuperseded {
		return &InvalidStatusTransitionError{From: status, To: StatusSuperseded}
	}
	return nil
}
