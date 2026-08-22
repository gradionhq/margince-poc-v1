// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Handlers is the people module's transport surface: the contract
// operations over persons, organizations and leads (incl. merge and
// lead promotion). Wire concerns only — decode, validate, map store
// errors to the sentinel registry; the store owns the transactional
// write shape.

import (
	"context"
	"errors"
	"net/http"

	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

type Handlers struct {
	// stageMatches turns suggested LinkedIn matches into approvals. See
	// WithMatchStager for why it is injected.
	stageMatches func(context.Context) error

	store *Store
	// blob serves the organization logo's bytes. Nil is a role that stores no
	// objects: the logo endpoint then answers 501 rather than nil-derefing,
	// and no logo can have been resolved for it to serve anyway.
	blob blobstore.Store
	// uploadLimit is the deployment's ceiling for this module's upload route
	// (OPS-CFG-12), injected by WithUploadLimit. Zero refuses every upload,
	// which is the honest reading of "nobody has said" for a bound.
	uploadLimit int64
}

// NewHandlers builds the module's HTTP surface over a workspace-bound handle.
func NewHandlers(db *database.DB) Handlers {
	return Handlers{store: NewStore(db)}
}

// WithMatchStager wires the pass that turns this member's suggested LinkedIn
// matches into approvals.
//
// Injected rather than called directly because the approvals engine is a
// sibling module and this one never imports a sibling. Nil means a role that
// stages nothing — the import still runs and still matches; the suggestions
// simply wait for the hourly sweep, which is composed with the seam.
func (h Handlers) WithMatchStager(stage func(context.Context) error) Handlers {
	h.stageMatches = stage
	return h
}

// WithGeocodeEnqueue wires the coordinate lookup an address write queues, into
// the TRANSPORT's own store.
//
// It has to be said here as well as on the store, because the two are not the
// same object: compose builds the handlers from newPeopleHandlers and keeps a
// separate s.peopleStore for the services. Wiring only the latter left every
// address written over HTTP marked stale by the trigger with no job coming —
// the enqueue existed, was correct, and was reachable from nothing a rep
// touches.
func (h Handlers) WithGeocodeEnqueue(enqueue GeocodeEnqueue) Handlers {
	h.store = h.store.WithGeocodeEnqueue(enqueue)
	return h
}

// WithBlobstore wires the object store the organization-logo stream reads.
func (h Handlers) WithBlobstore(blob blobstore.Store) Handlers {
	h.blob = blob
	return h
}

// WithFieldCatalog wires the workspace custom-field catalog into the
// transport's store (see Store.WithFieldCatalog); compose injects
// modules/customfields' Service here.
func (h Handlers) WithFieldCatalog(catalog fieldcatalog.Reader) Handlers {
	h.store = h.store.WithFieldCatalog(catalog)
	return h
}

// WithSettings wires the installation settings store the lead-settings
// endpoints write through.
func (h Handlers) WithSettings(store *settings.Store) Handlers {
	h.store = h.store.WithSettings(store)
	return h
}

// WithDealOpener wires the deals-side seam a qualify call opens its deal on.
func (h Handlers) WithDealOpener(opener LeadDealOpener) Handlers {
	h.store = h.store.WithDealOpener(opener)
	return h
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
	var bothProjects *BothCompaniesCarryProjectsError
	if errors.As(err, &bothProjects) {
		// The names ride the body: "resolve your projects first" is only
		// actionable if the caller is told which ones.
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict,
			Code:   "both_companies_have_projects",
			Detail: bothProjects.Error(),
			Details: map[string]any{
				"source_projects": bothProjects.Source,
				"target_projects": bothProjects.Target,
			},
		})
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
	var dupLeadLinkedIn *DuplicateLeadLinkedInError
	if errors.As(err, &dupLeadLinkedIn) {
		httperr.Write(w, r, httperr.Duplicate("duplicate_linkedin_url", duplicateID(dupLeadLinkedIn.ExistingID.UUID)))
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
			e.Details = map[string]any{fieldKeyPromotedPerson: promoted.PersonID.String()}
		}
		httperr.Write(w, r, e)
		return
	}
	var notPromoted *NotPromotedError
	if errors.As(err, &notPromoted) {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict, Code: "not_promoted", Detail: notPromoted.Error(),
		})
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
			e.Details = map[string]any{auditKeyMergedInto: alreadyMerged.IntoID.String()}
		}
		httperr.Write(w, r, e)
		return
	}
	httperr.Write(w, r, err)
}
