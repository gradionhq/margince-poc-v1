// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

type UpdateLeadInput struct {
	FullName        *string
	Email           *string
	Title           *string
	CompanyName     *string
	CandidateOrgKey *string
	Status          *string // only new ↔ working here; terminal states have their own paths
	Score           *int
	// ScoreOverrideReason is the written reason for a score override; nil
	// means the field was absent (no override change). The explicit CLEAR
	// gesture is ClearScoreOverride, not an empty string — an empty reason
	// is invalid input (contract minLength 1).
	ScoreOverrideReason *string
	// ClearScoreOverride is the wire's explicit JSON null on score or
	// score_override_reason: drop the override and resume recompute.
	// encoding/json erases null-vs-absent on pointer fields, so the
	// transports carry the distinction here.
	ClearScoreOverride bool
	OwnerID            *ids.UserID
	IfVersion          *int64
	// CustomFields carries the request body's extra top-level keys
	// (additionalProperties); only active cf_* catalog columns land,
	// drop-on-mismatch (customfields.go).
	CustomFields map[string]any
}

// ScoreOverrideReasonRequiredError rejects a human score with no written
// reason — the Commercial Judgement rule (formulas §3.1, AC-S1): an
// override is auditable or it does not happen.
type ScoreOverrideReasonRequiredError struct{}

func (e *ScoreOverrideReasonRequiredError) Error() string {
	return "a score override requires a non-empty score_override_reason"
}

// ScoreOverrideReasonEmptyError rejects an empty-string reason: the
// contract's clear gesture is JSON null (minLength 1 on the field), so a
// blank reason is neither a written justification nor a clear — it is
// invalid input.
type ScoreOverrideReasonEmptyError struct{}

func (e *ScoreOverrideReasonEmptyError) Error() string {
	return "score_override_reason must not be empty; pass null to clear the override"
}

// ScoreOverrideClearConflictError rejects a null score arriving together
// with a written reason: null says "drop the override", the reason says
// "keep one" — honoring either would silently discard the other half of
// the request.
type ScoreOverrideClearConflictError struct{}

func (e *ScoreOverrideClearConflictError) Error() string {
	return "a null score clears the override; it cannot be combined with a score_override_reason"
}

func (s *Store) UpdateLead(ctx context.Context, id ids.LeadID, in UpdateLeadInput) (crmcontracts.Lead, error) {
	if err := auth.Require(ctx, "lead", principal.ActionUpdate); err != nil {
		return crmcontracts.Lead{}, err
	}
	active, err := s.activeColumns(ctx, "lead")
	if err != nil {
		return crmcontracts.Lead{}, err
	}
	var out crmcontracts.Lead
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = s.updateLeadTx(ctx, tx, id, in, active)
		return err
	})
	return out, err
}

// updateLeadTx runs the visibility gate, the sparse-patch fold, the write
// shape, and the cleared-override recompute for one lead update inside the
// caller's transaction. active names the workspace's custom-field columns
// (fetched before the tx opened).
func (s *Store) updateLeadTx(ctx context.Context, tx pgx.Tx, id ids.LeadID, in UpdateLeadInput, active []fieldcatalog.Column) (crmcontracts.Lead, error) {
	if err := auth.EnsureVisible(ctx, tx, "lead", id.UUID); err != nil {
		return crmcontracts.Lead{}, err
	}
	current, err := readLead(ctx, tx, id, storekit.LiveOnly, active)
	if err != nil {
		return crmcontracts.Lead{}, err
	}
	p, resumeRecompute, err := buildLeadPatch(current, in)
	if err != nil {
		return crmcontracts.Lead{}, err
	}
	storekit.SetCustomFieldPatch(p, active, in.CustomFields, current.AdditionalProperties)
	if p.Empty() {
		return current, nil
	}
	if err := p.ApplyGuarded(ctx, tx, "lead", id.UUID, in.IfVersion); err != nil {
		if mapped, ok := leadUniqueViolation(err, in.Email); ok {
			return crmcontracts.Lead{}, mapped
		}
		return crmcontracts.Lead{}, err
	}
	auditID, err := storekit.Audit(ctx, tx, "update", "lead", id.UUID, p.Before(), p.After())
	if err != nil {
		return crmcontracts.Lead{}, err
	}
	if err := storekit.Emit(ctx, tx, auditID, "lead.updated", "lead", id.UUID, p.After()); err != nil {
		return crmcontracts.Lead{}, err
	}
	// Clearing an override immediately recomputes from current signals
	// (formulas §3.1): score no longer lags behind the machine value.
	if resumeRecompute {
		if err := recomputeLeadScoreTx(ctx, tx, id, time.Now().UTC()); err != nil {
			return crmcontracts.Lead{}, err
		}
	}
	return readLead(ctx, tx, id, storekit.LiveOnly, active)
}

// buildLeadPatch folds the caller's sparse update onto the current lead
// as a field patch, and reports whether the caller must resume recompute
// (a cleared score override). The Commercial Judgement score override
// (formulas §3.1, A68/ADR-0053) is sticky: setting a score demands a
// written reason and retains the machine value; clearing the reason
// resumes recompute.
func buildLeadPatch(current crmcontracts.Lead, in UpdateLeadInput) (*storekit.Patch, bool, error) {
	p := storekit.NewPatch()
	if in.FullName != nil {
		p.Set("full_name", current.FullName, *in.FullName)
	}
	if in.Email != nil {
		parsed, err := values.ParseEmail(*in.Email)
		if err != nil {
			return nil, false, err
		}
		p.Set("email", current.Email, parsed.String())
	}
	if in.Title != nil {
		p.Set("title", current.Title, *in.Title)
	}
	if in.CompanyName != nil {
		p.Set("company_name", current.CompanyName, *in.CompanyName)
	}
	if in.CandidateOrgKey != nil {
		p.Set("candidate_org_key", current.CandidateOrgKey, *in.CandidateOrgKey)
	}
	if in.Status != nil {
		status, err := ParseLeadStatus(*in.Status)
		if err != nil {
			return nil, false, err
		}
		p.Set("status", current.Status, string(status))
	}
	resumeRecompute, err := applyScoreOverride(p, current, in)
	if err != nil {
		return nil, false, err
	}
	if in.OwnerID != nil {
		p.Set("owner_id", current.OwnerId, *in.OwnerID)
	}
	return p, resumeRecompute, nil
}

// applyScoreOverride folds the §3.1 sticky-override rules into the patch
// and reports whether the caller must resume recompute (an override was
// cleared). Setting `score` establishes/refreshes an override — it
// requires a non-empty reason and captures the machine value into
// score_computed the first time. An explicit JSON null on score or the
// reason clears the override. A non-empty reason with no score amends
// the note on an override already in force; an empty-string reason is
// invalid input (the clear gesture is null, not "").
func applyScoreOverride(p *storekit.Patch, current crmcontracts.Lead, in UpdateLeadInput) (resumeRecompute bool, err error) {
	overrideInForce := current.ScoreOverrideReason != nil

	switch {
	case in.Score != nil:
		reason := ""
		if in.ScoreOverrideReason != nil {
			reason = strings.TrimSpace(*in.ScoreOverrideReason)
		}
		if reason == "" {
			return false, &ScoreOverrideReasonRequiredError{}
		}
		p.Set("score", current.Score, *in.Score)
		p.Set("score_override_reason", current.ScoreOverrideReason, reason)
		// Retain the last machine value the first time an override takes
		// hold; if one is already in force, score_computed already holds it
		// and the recompute keeps it fresh — don't clobber it with a human
		// number.
		if !overrideInForce {
			p.Set("score_computed", current.ScoreComputed, current.Score)
		}
		return false, nil

	case in.ClearScoreOverride:
		if in.ScoreOverrideReason != nil {
			return false, &ScoreOverrideClearConflictError{}
		}
		if !overrideInForce {
			return false, nil // no override to clear — a no-op
		}
		p.Set("score_override_reason", current.ScoreOverrideReason, nil)
		// Resume: score tracks the retained machine value, then recompute
		// refines it from current signals.
		if current.ScoreComputed != nil {
			p.Set("score", current.Score, *current.ScoreComputed)
		}
		p.Set("score_computed", current.ScoreComputed, nil)
		return true, nil

	case in.ScoreOverrideReason != nil:
		if strings.TrimSpace(*in.ScoreOverrideReason) == "" {
			return false, &ScoreOverrideReasonEmptyError{}
		}
		if !overrideInForce {
			return false, &ScoreOverrideReasonRequiredError{} // a reason without a score sets nothing
		}
		p.Set("score_override_reason", current.ScoreOverrideReason, strings.TrimSpace(*in.ScoreOverrideReason))
		return false, nil
	}
	return false, nil
}
