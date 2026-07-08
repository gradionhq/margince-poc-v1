// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Handlers is the people module's transport surface: the contract
// operations over persons, organizations and leads (incl. merge and
// lead promotion). Wire concerns only — decode, validate, map store
// errors to the sentinel registry; the store owns the transactional
// write shape.

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type Handlers struct {
	store *Store
}

func NewHandlers(pool *pgxpool.Pool) Handlers {
	return Handlers{store: NewStore(pool)}
}

// duplicateID renders a duplicate error's existing-row pointer for the
// wire. The dedupe pre-checks leave ExistingID zero when the row is not
// visible to the caller (or a race hid it); the response then omits
// existing_id entirely — a literal zero UUID is not an id, and clients
// must never be trained to special-case one.
func duplicateID(id ids.UUID) string {
	if id.IsZero() {
		return ""
	}
	return id.String()
}

// writeStoreErr maps this module's typed store errors onto the wire
// codes the contract names, then falls through to the sentinel registry.
func writeStoreErr(w http.ResponseWriter, r *http.Request, err error) {
	var missing *RequiredFieldError
	if errors.As(err, &missing) {
		httperr.Write(w, r, httperr.Validation(missing.Field, "required", missing.Error()))
		return
	}
	var dupEmail *DuplicateEmailError
	if errors.As(err, &dupEmail) {
		httperr.Write(w, r, httperr.Duplicate("duplicate_email", duplicateID(dupEmail.ExistingID.UUID)))
		return
	}
	var dupDomain *DuplicateDomainError
	if errors.As(err, &dupDomain) {
		httperr.Write(w, r, httperr.Duplicate("duplicate_domain", duplicateID(dupDomain.ExistingID.UUID)))
		return
	}
	var dupLead *DuplicateLeadError
	if errors.As(err, &dupLead) {
		httperr.Write(w, r, httperr.Duplicate("duplicate_email", duplicateID(dupLead.ExistingID.UUID)))
		return
	}
	var needsReason *ScoreOverrideReasonRequiredError
	if errors.As(err, &needsReason) {
		httperr.Write(w, r, httperr.Validation("score_override_reason", "required", needsReason.Error()))
		return
	}
	var promoted *AlreadyPromotedError
	if errors.As(err, &promoted) {
		e := &httperr.DetailedError{
			Status: http.StatusConflict, Code: "already_promoted", Detail: promoted.Error(),
		}
		// The outcome pointer sits on the lead row the caller just proved
		// they can read, so echoing it discloses nothing new.
		if !promoted.PersonID.IsZero() {
			e.Details = map[string]any{"promoted_person_id": promoted.PersonID.String()}
		}
		httperr.Write(w, r, e)
		return
	}
	var needsIdentity *PromoteNeedsIdentityError
	if errors.As(err, &needsIdentity) {
		httperr.Write(w, r, httperr.Validation("lead", "identity_required", needsIdentity.Error()))
		return
	}
	var mergeSelf *MergeSelfError
	if errors.As(err, &mergeSelf) {
		httperr.Write(w, r, httperr.Validation("target_id", "merge_self", mergeSelf.Error()))
		return
	}
	var alreadyMerged *AlreadyMergedError
	if errors.As(err, &alreadyMerged) {
		e := &httperr.DetailedError{
			Status: http.StatusConflict, Code: "already_merged", Detail: alreadyMerged.Error(),
		}
		// The redirect pointer lives on the source row the caller just proved
		// they can address, so echoing it discloses nothing new (the
		// AlreadyPromoted precedent).
		if !alreadyMerged.IntoID.IsZero() {
			e.Details = map[string]any{"merged_into_id": alreadyMerged.IntoID.String()}
		}
		httperr.Write(w, r, e)
		return
	}
	var mergedTarget *MergedTargetError
	if errors.As(err, &mergedTarget) {
		httperr.Write(w, r, httperr.Validation("target_id", "merged_target", mergedTarget.Error()))
		return
	}
	// Defense-in-depth net: a CHECK constraint is a business rule, so a
	// breach that slipped past the per-path validations still answers a
	// typed 422 naming the rule — never an opaque 500.
	if constraint, ok := storekit.CheckViolation(err); ok {
		httperr.Write(w, r, httperr.Validation(constraint, "constraint_violated",
			"the request violates the "+constraint+" business rule"))
		return
	}
	httperr.Write(w, r, err)
}
