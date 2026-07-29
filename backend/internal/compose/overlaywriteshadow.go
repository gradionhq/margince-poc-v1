// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The overlay-mode human write surface (design.md §4.5, the write-back half
// of "Overlay does not fork the data API"): Server shadows the contract
// update/archive ops for the write verbs overlay.SupportsWrite reports true
// — update on all five mirror entity types, archive on person/organization/
// deal — routing them through the SAME Dispatcher the MCP/agent seam
// consumers ride, and delegating to the native module handler otherwise.
// The overlaywrite.go guard already refuses every write the provider cannot
// serve (create; archive on lead/activity) before it reaches a handler at
// all, so no shadow exists here for those — see that file's own doc.
//
// Every write here is incumbent-first (overlay.Provider.Update/Archive):
// HubSpot accepts the change before anything lands back in the mirror, so a
// refusal upstream never leaves a local row claiming a write the incumbent
// never took. The response is always the RE-MIRRORED row, assembled by the
// same overlayWire* mapper the read shadows use, so a write and a
// following GET answer one shape.

import (
	"context"
	"net/http"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// overlayWriteMode answers whether this MUTATING request dispatches to the
// mirror, resolved uncached (dispatcher.go's isOverlayUncached) rather than
// through the read path's TTL cache. A write shadow that took the cached
// answer could hand a mutation to the native module handler for the rest of
// the TTL after another process flipped the workspace into overlay — a
// commit to a native table no overlay read ever serves, which never reaches
// the incumbent. A mode-resolution failure is written to w (ok=false), the
// same fail-closed posture overlayReadMode takes.
func (s Server) overlayWriteMode(w http.ResponseWriter, r *http.Request) (overlayMode, ok bool) {
	ov, err := s.sorDispatch.isOverlayUncached(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return false, false
	}
	return ov, true
}

// restWriteSource is the provenance Source every human REST write carries
// into the seam. CapturedBy stays unset: overlay's write-back never reads it
// (the incumbent record carries no such column), and the write shape's own
// rule is that captured_by is stamped from the authenticated principal,
// never from the transport.
const restWriteSource = "api"

// overlayUpdate serves one update shadow: the native module handler off
// overlay mode, otherwise a decode into the contract's own request struct
// (the shape overlay.SupportsWrite's writeContractTarget validates against)
// and a dispatched seam Update, answered with the re-mirrored row.
//
// If-Match is NOT evaluated here, and the caller is not told: a mirror row
// carries no version (overlay/provider.go's recordFromRow), so a
// caller-supplied If-Match has nothing to compare against on this path, and
// the header is accepted and discarded. State the consequence plainly rather
// than only the cause — a client that sends If-Match believing it has
// optimistic concurrency has none here, and gets no signal saying so.
//
// What overlay does guard is a DIFFERENT clock: the provider's incumbent
// stored-baseline drift check (provider_writes.go's Update), applied
// unconditionally inside the seam call below. It closes the mirror↔incumbent
// gap, not the caller↔mirror gap the header is about, so it is not a
// substitute. Closing that gap needs a version (or baseline echo) the caller
// can pin, which is a contract question — RowVersion's own text says version
// semantics apply "not only overlay mode" — and is raised upstream rather
// than decided here.
//
// Go forbids type parameters on methods, so this is a plain function
// taking Server as its first argument rather than a method.
func overlayUpdate[Req any, Res any](s Server, w http.ResponseWriter, r *http.Request,
	et datasource.EntityType, id crmcontracts.Id, native func(),
	wire func(context.Context, datasource.Record) (Res, error),
) {
	ov, ok := s.overlayWriteMode(w, r)
	if !ok {
		return
	}
	if !ov {
		native()
		return
	}
	var req Req
	if !httperr.Decode(w, r, &req) {
		return
	}
	// ov is already the fresh answer overlayWriteMode just read, so dispatch
	// with it rather than making the Dispatcher re-read the same row.
	ref, err := s.sorDispatch.updateInMode(r.Context(), bool(ov), datasource.UpdateInput{
		Ref:    datasource.EntityRef{Type: et, ID: ids.UUID(id)},
		Patch:  &req,
		Source: restWriteSource,
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	respondWithMirroredRecord(s, w, r, ref, wire)
}

// respondWithMirroredRecord reads the just-written row back through the
// same mapper the read shadows use, so a write and a subsequent GET answer
// one shape. Anything that returns a record is a read: the read-back
// carries the row-scope gate (Provider.Read's own object-RBAC + visibility
// deny-join), so a write whose result the caller may not see answers 404
// rather than leaking it.
func respondWithMirroredRecord[Res any](s Server, w http.ResponseWriter, r *http.Request,
	ref datasource.EntityRef, wire func(context.Context, datasource.Record) (Res, error),
) {
	rec, err := s.sorDispatch.Read(r.Context(), ref)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	body, err := wire(r.Context(), rec)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, body)
}

// archiveWire pairs the two per-entity pieces an archive shadow needs: the
// mapper that assembles the contract body from a mirror record, and the
// setter that stamps the archive instant onto it. They travel together
// because neither is meaningful without the other — assembling a body and
// then forgetting to mark it archived is exactly the bug the stamp exists to
// fix.
type archiveWire[Res any] struct {
	assemble     func(context.Context, datasource.Record) (Res, error)
	markArchived func(*Res, time.Time)
}

// overlayArchive serves one archive shadow: the native module handler off
// overlay mode, otherwise a dispatched seam Archive answered with the
// archived row's last-known state — the contract's own archive response
// shape (200 with the full entity body; architecture/11 §8 rules out a bare
// 204 for a domain row, and every native ArchivePerson/ArchiveOrganization/
// ArchiveDeal handler answers exactly that). The mirror row is purged by the
// archive itself (provider_writes.go's Archive calls PurgeRecord), so
// unlike overlayUpdate there is no read-BACK to ride: the record is read
// once, BEFORE the purge, and that pre-archive snapshot is what wire
// assembles — still a read, still carrying the same row-scope/object gate
// (Provider.Read's own auth.Require), just ordered before rather than after
// the write.
//
// markArchived stamps the archive instant onto the assembled body, so the
// 200 describes the record as it is AFTER the call rather than as it was
// before. Without it the response carries archived_at: null about a record
// the incumbent has just archived — the contract defines the archive
// response as one that "now carries a non-null archived_at", and answering
// otherwise reports the write as not having happened.
//
// The instant is this server's own: the incumbent acks the archive without
// reporting when it applied it, so the honest available value is when we
// observed it, accurate to the width of the call.
//
// The contract's other archive promise — that the archived row stays
// fetchable by id — is NOT met here, and cannot be by this transport: the
// mirror row is purged, so the following GET answers 404. An incumbent
// archive stops serving the object at the source, so the mirror has no
// archived state to keep; honoring that promise needs an archived-row
// substrate and a settled answer to what archive MEANS when the system of
// record is an incumbent. Both are upstream contract questions against
// margince-foundation's overlay chapter; this shadow does not paper over the
// gap by pretending the record is still there, and the integration test
// asserts the 404 so the day that answer lands, it fails loudly.
//
// The body can also be stale by the width of the pre-read-then-archive
// window — an incumbent update landing in that gap is not reflected —
// whereas the native path reads and archives in one transaction.
func overlayArchive[Res any](s Server, w http.ResponseWriter, r *http.Request,
	et datasource.EntityType, id crmcontracts.Id, native func(), wire archiveWire[Res],
) {
	ov, ok := s.overlayWriteMode(w, r)
	if !ok {
		return
	}
	if !ov {
		native()
		return
	}
	ref := datasource.EntityRef{Type: et, ID: ids.UUID(id)}
	rec, err := s.sorDispatch.Read(r.Context(), ref)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if _, err := s.sorDispatch.archiveInMode(r.Context(), bool(ov), ref); err != nil {
		httperr.Write(w, r, err)
		return
	}
	body, err := wire.assemble(r.Context(), rec)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	wire.markArchived(&body, time.Now().UTC())
	httperr.WriteJSON(w, http.StatusOK, body)
}

// UpdatePerson shadows the person update.
func (s Server) UpdatePerson(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.UpdatePersonParams) {
	overlayUpdate[crmcontracts.UpdatePersonRequest](s, w, r, datasource.EntityPerson, id,
		func() { s.peopleHandlers.UpdatePerson(w, r, id, params) }, overlayWirePerson)
}

// ArchivePerson shadows the person archive.
func (s Server) ArchivePerson(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	overlayArchive(s, w, r, datasource.EntityPerson, id,
		func() { s.peopleHandlers.ArchivePerson(w, r, id) }, archiveWire[crmcontracts.Person]{
			assemble:     overlayWirePerson,
			markArchived: func(p *crmcontracts.Person, at time.Time) { p.ArchivedAt = &at },
		})
}

// UpdateOrganization shadows the organization update.
func (s Server) UpdateOrganization(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.UpdateOrganizationParams) {
	overlayUpdate[crmcontracts.UpdateOrganizationRequest](s, w, r, datasource.EntityOrganization, id,
		func() { s.peopleHandlers.UpdateOrganization(w, r, id, params) }, overlayWireOrganization)
}

// ArchiveOrganization shadows the organization archive.
func (s Server) ArchiveOrganization(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	overlayArchive(s, w, r, datasource.EntityOrganization, id,
		func() { s.peopleHandlers.ArchiveOrganization(w, r, id) }, archiveWire[crmcontracts.Organization]{
			assemble:     overlayWireOrganization,
			markArchived: func(o *crmcontracts.Organization, at time.Time) { o.ArchivedAt = &at },
		})
}

// UpdateDeal shadows the deal update.
func (s Server) UpdateDeal(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.UpdateDealParams) {
	overlayUpdate[crmcontracts.UpdateDealRequest](s, w, r, datasource.EntityDeal, id,
		func() { s.dealsHandlers.UpdateDeal(w, r, id, params) }, overlayWireDeal)
}

// ArchiveDeal shadows the deal archive.
func (s Server) ArchiveDeal(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	overlayArchive(s, w, r, datasource.EntityDeal, id,
		func() { s.dealsHandlers.ArchiveDeal(w, r, id) }, archiveWire[crmcontracts.Deal]{
			assemble:     overlayWireDeal,
			markArchived: func(d *crmcontracts.Deal, at time.Time) { d.ArchivedAt = &at },
		})
}

// UpdateLead shadows the lead update. Lead has no archive shadow: it is not
// in archivableTypes (overlay.SupportsWrite(WriteArchive, lead) is false),
// so the guard refuses its archive route before any handler is reached.
func (s Server) UpdateLead(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.UpdateLeadParams) {
	overlayUpdate[crmcontracts.UpdateLeadRequest](s, w, r, datasource.EntityLead, id,
		func() { s.peopleHandlers.UpdateLead(w, r, id, params) }, overlayWireLead)
}

// UpdateActivity shadows the activity update. Activity has no archive
// shadow for the same reason lead has none — see UpdateLead's doc.
func (s Server) UpdateActivity(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.UpdateActivityParams) {
	overlayUpdate[crmcontracts.UpdateActivityRequest](s, w, r, datasource.EntityActivity, id,
		func() { s.activitiesHandlers.UpdateActivity(w, r, id, params) }, overlayWireActivity)
}
