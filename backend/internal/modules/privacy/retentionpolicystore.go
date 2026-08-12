// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The retention-policy authoring store (UC-GDPR-09, GCS-WIRE-1..4). Until this
// file the table had exactly one writer — the workspace bootstrap — and no
// reader outside the nightly evaluator, which is what #695 named: the engine ran
// and nobody could configure it.
//
// Handlers→Store, the CRUD spine (ADR-0054 §3): the store owns the write shape
// and the RBAC gate at every entry point. Writes are audit-only, no outbox
// event — the closed catalog has no retention-policy stream (EVT-NOEVT-3), and
// `retention.applied` stays the ENGINE's event about a record it acted on, not
// a config change.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// retentionPolicyEntity is the audit_log entity_type for a policy row. A
// configuration row, not a first-class entity — which is also why the kernel
// mints no id kind for it and retentionPolicy.ID stays ids.UUID.
const retentionPolicyEntity = "retention_policy"

// PolicyStore is the authoring surface's persistence.
type PolicyStore struct {
	// db binds the installation's workspace itself (ADR-0091 §9 step 3).
	db *database.DB
}

// NewPolicyStore wires the store over the installation-bound pool.
func NewPolicyStore(db *database.DB) *PolicyStore { return &PolicyStore{db: db} }

// Policy is one authored or seeded retention rule, as the surface reads it.
type Policy struct {
	ID          ids.UUID
	Scope       RetentionScope
	RetainDays  int
	Action      string
	LawfulBasis *string
	Enabled     bool
	// SuppressedByPosture is derived, never stored: the retain-only posture
	// overriding this row right now. It travels with the row because an enabled
	// policy that is not acting needs to say why on the screen, not in a log.
	SuppressedByPosture bool
}

// PolicyInput is an authoring request, scope already resolved against the
// evaluator's selectors.
type PolicyInput struct {
	Scope       RetentionScope
	RetainDays  int
	Action      string
	LawfulBasis *string
	Enabled     bool
}

// PolicyPatch is a sparse edit. A nil field is unchanged; the scope is absent by
// design — re-targeting a row would silently re-attribute its audit history, so
// a different scope is a different policy.
type PolicyPatch struct {
	RetainDays  *int
	Action      *string
	LawfulBasis *string
	Enabled     *bool
}

// fieldRetainDays is the contract's name for the window field, spelled once: it
// appears in the refusal, in the audit image and in the patch, and a divergence
// between those would point an admin at a field their request never carried.
const fieldRetainDays = "retain_days"

// PolicyFieldError refuses one field of an authoring request. It implements
// apperrors.FieldFault, so the refusal classifies as a 422 naming the field on
// every surface that carries it rather than only on REST.
type PolicyFieldError struct {
	Field   string
	Code    string
	Message string
}

func (e PolicyFieldError) Error() string { return e.Field + ": " + e.Message }

// FieldFault names the offending field, its contract code and the sentence the
// caller can act on.
func (e PolicyFieldError) FieldFault() (field, code, message string) {
	return e.Field, e.Code, e.Message
}

// validateRetentionAction refuses an action the engine has no executor for. The
// contract enum already refuses this at the edge; this is the gate that holds for
// every caller, the tests and any future surface included.
func validateRetentionAction(action string) error {
	switch action {
	case actionArchive, actionAnonymize, actionErase:
		return nil
	default:
		return PolicyFieldError{
			Field: "action", Code: "invalid_retention_action",
			Message: "a retention action is one of archive, anonymize or erase — one action per policy row, ladders composing as separate rows",
		}
	}
}

// validateRetainDays refuses a window that would act on a record as soon as it
// exists. Zero is the dangerous value, not a harmless edge: a 0-day erase policy
// would empty the scope on the next pass.
func validateRetainDays(days int) error {
	if days < 1 {
		return PolicyFieldError{
			Field: fieldRetainDays, Code: "invalid_retain_days",
			Message: "retain_days must be at least 1 — a zero-day window would act on a record the moment it is created",
		}
	}
	return nil
}

// List returns every policy with its live suppression state.
func (s *PolicyStore) List(ctx context.Context) ([]Policy, error) {
	if err := auth.Require(ctx, retentionPolicyObject, principal.ActionRead); err != nil {
		return nil, err
	}
	var out []Policy
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		retainOnly, err := settings.GetTx(ctx, tx, RetainOnly)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, object_type, category, retain_days, action, lawful_basis, enabled
			FROM retention_policy ORDER BY object_type, category NULLS FIRST, retain_days`)
		if err != nil {
			return err
		}
		out, err = collectPolicies(rows, retainOnly)
		return err
	})
	return out, err
}

// collectPolicies scans the read and stamps the posture onto each row.
func collectPolicies(rows pgx.Rows, retainOnly bool) ([]Policy, error) {
	defer rows.Close()
	out := []Policy{}
	for rows.Next() {
		var (
			policy     Policy
			objectType string
			category   *string
		)
		if err := rows.Scan(&policy.ID, &objectType, &category, &policy.RetainDays,
			&policy.Action, &policy.LawfulBasis, &policy.Enabled); err != nil {
			return nil, err
		}
		policy.Scope = ScopeOf(objectType, category)
		policy.SuppressedByPosture = retainOnly && isDestructive(policy.Action)
		out = append(out, policy)
	}
	return out, rows.Err()
}

// Create authors one policy. The scope's uniqueness is the DATABASE's answer
// (retention_policy_unique, core 0217), not a read-then-write here: two admins
// authoring the same scope concurrently would both pass a pre-check and one
// would silently win.
func (s *PolicyStore) Create(ctx context.Context, in PolicyInput) (Policy, error) {
	if err := auth.Require(ctx, retentionPolicyObject, principal.ActionCreate); err != nil {
		return Policy{}, err
	}
	if err := validateRetentionAction(in.Action); err != nil {
		return Policy{}, err
	}
	if err := validateRetainDays(in.RetainDays); err != nil {
		return Policy{}, err
	}
	var out Policy
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		// The response reports suppressed_by_posture, so a create reads the
		// posture — which takes RetainOnly's own READ grant on this same object.
		// A custom role granted create-without-read would therefore be refused
		// here rather than at the entry point. Left as is deliberately: the
		// alternative is an ungated settings read, and that gate is the only
		// control on a table with no RLS.
		retainOnly, err := settings.GetTx(ctx, tx, RetainOnly)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO retention_policy
			  (workspace_id, object_type, category, retain_days, action, lawful_basis, enabled)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, $5, $6)
			RETURNING id`,
			in.Scope.ObjectType, in.Scope.CategoryPtr(), in.RetainDays,
			in.Action, in.LawfulBasis, in.Enabled).Scan(&out.ID)
		if constraint, ok := storekit.UniqueViolation(err); ok && constraint == "retention_policy_unique" {
			return fmt.Errorf("retention policy for scope %q already exists: %w",
				in.Scope, apperrors.ErrConflict)
		}
		if err != nil {
			return err
		}
		out = Policy{
			ID: out.ID, Scope: in.Scope, RetainDays: in.RetainDays, Action: in.Action,
			LawfulBasis: in.LawfulBasis, Enabled: in.Enabled,
			SuppressedByPosture: retainOnly && isDestructive(in.Action),
		}
		_, err = storekit.Audit(ctx, tx, "create", retentionPolicyEntity, out.ID, nil, auditImage(out))
		return err
	})
	return out, err
}

// Update applies a sparse patch. It reads the row inside the write transaction
// so the audit's before-image is the state the change actually replaced.
func (s *PolicyStore) Update(ctx context.Context, id ids.UUID, patch PolicyPatch) (Policy, error) {
	if err := auth.Require(ctx, retentionPolicyObject, principal.ActionUpdate); err != nil {
		return Policy{}, err
	}
	if patch.Action != nil {
		if err := validateRetentionAction(*patch.Action); err != nil {
			return Policy{}, err
		}
	}
	if patch.RetainDays != nil {
		if err := validateRetainDays(*patch.RetainDays); err != nil {
			return Policy{}, err
		}
	}
	var out Policy
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		retainOnly, err := settings.GetTx(ctx, tx, RetainOnly)
		if err != nil {
			return err
		}
		before, err := readPolicyTx(ctx, tx, id, retainOnly)
		if err != nil {
			return err
		}
		out = applyPatch(before, patch)
		if _, err := tx.Exec(ctx, `
			UPDATE retention_policy
			SET retain_days = $2, action = $3, lawful_basis = $4, enabled = $5
			WHERE id = $1`,
			id, out.RetainDays, out.Action, out.LawfulBasis, out.Enabled); err != nil {
			return err
		}
		out.SuppressedByPosture = retainOnly && isDestructive(out.Action)
		_, err = storekit.Audit(ctx, tx, "update", retentionPolicyEntity, id,
			auditImage(before), auditImage(out))
		return err
	})
	return out, err
}

// applyPatch folds a sparse patch onto the stored row. Kept apart from the
// transaction so the merge is unit-testable without a database, and so "an
// omitted field is unchanged" is one readable place rather than four.
func applyPatch(current Policy, patch PolicyPatch) Policy {
	out := current
	if patch.RetainDays != nil {
		out.RetainDays = *patch.RetainDays
	}
	if patch.Action != nil {
		out.Action = *patch.Action
	}
	if patch.LawfulBasis != nil {
		out.LawfulBasis = patch.LawfulBasis
	}
	if patch.Enabled != nil {
		out.Enabled = *patch.Enabled
	}
	return out
}

// Delete removes the configuration row. The records it governed are untouched —
// a deleted policy stops future actions and never undoes past ones.
//
// Audited under `archive` rather than a delete verb: `delete` is not in the
// closed audit vocabulary, and `archive` is what this repo spells for "this
// configuration is gone" everywhere else.
func (s *PolicyStore) Delete(ctx context.Context, id ids.UUID) error {
	if err := auth.Require(ctx, retentionPolicyObject, principal.ActionDelete); err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		// The row is read before it goes so the audit entry says WHAT was
		// deleted. An audit row naming only an id, for a row that no longer
		// exists, would record that something was removed and nothing about what.
		before, err := readPolicyTx(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM retention_policy WHERE id = $1`, id); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, actionArchive, retentionPolicyEntity, id,
			auditImage(before), nil)
		return err
	})
}

// readPolicyTx reads one row FOR UPDATE, or reports it absent. Anything that
// returns a record is a read (review-loop rule 3), and the object gate is
// already held by every caller's entry point.
//
// The lock is load-bearing, not defensive. Update writes all four mutable
// columns from the image merged onto this read, so two concurrent sparse patches
// without it would interleave: the second reads before the first commits, then
// writes its stale merge back — silently reverting a colleague's change and
// audit-logging a before-image that was never the state it replaced. Same
// failure platform/settings takes an advisory lock for, on a table whose subject
// is what the installation destroys.
func readPolicyTx(ctx context.Context, tx pgx.Tx, id ids.UUID, retainOnly bool) (Policy, error) {
	var (
		out        Policy
		objectType string
		category   *string
	)
	err := tx.QueryRow(ctx, `
		SELECT id, object_type, category, retain_days, action, lawful_basis, enabled
		FROM retention_policy WHERE id = $1 FOR UPDATE`, id).
		Scan(&out.ID, &objectType, &category, &out.RetainDays, &out.Action,
			&out.LawfulBasis, &out.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return Policy{}, fmt.Errorf("retention policy %s: %w", id, apperrors.ErrNotFound)
	}
	if err != nil {
		return Policy{}, err
	}
	out.Scope = ScopeOf(objectType, category)
	out.SuppressedByPosture = retainOnly && isDestructive(out.Action)
	return out, nil
}

// auditImage is the row's own field image for audit_log before/after. It carries
// the STORED fields only: SuppressedByPosture is a live reading of a setting, and
// folding it in would make the field-history projection report a policy edit
// every time the posture moved.
func auditImage(p Policy) map[string]any {
	return map[string]any{
		"scope":         p.Scope.String(),
		fieldRetainDays: p.RetainDays,
		"action":        p.Action,
		"lawful_basis":  p.LawfulBasis,
		"enabled":       p.Enabled,
	}
}
