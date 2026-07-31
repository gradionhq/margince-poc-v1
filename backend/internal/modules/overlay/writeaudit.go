// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The write-back path's governance half: the audit_log row and the outbox
// event every overlay Update/Archive commits, alongside the mirror refresh
// that same write produced.
//
// Why this file exists at all. The mirror is a derived cache, and its sync
// writes are deliberately audit-free (mirrorstore.go's Ingest doc) — a
// poller refresh is not a domain mutation and auditing it would retain
// incumbent PII in audit_log for no compliance purpose. A HUMAN or agent
// write-back is the opposite case: it IS a domain mutation, made by an
// identified principal, against the record the incumbent holds. It carries
// the same attributable audit trail every native module write carries, or
// the overlay path would be the one mutation surface in this build where
// "who changed this record" has no answer.
//
// The ordering this shape can and cannot promise. The incumbent commits
// FIRST (design.md §4.5: incumbent-first, so a refusal upstream never
// leaves a local row claiming a write HubSpot never took), so the canonical
// row and our audit row cannot share one transaction — the canonical row is
// not in our database. What DOES commit atomically is the entire local half:
// the mirror refresh, the audit_log row, and the event_outbox row land in
// ONE transaction or none of them do. A caller therefore never sees a
// refreshed mirror row with no audit trail, nor an announced event whose
// audit row rolled back.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// The audit actions a write-back records, spelled the same way the native
// module stores spell them (people/person.go, deals/deal.go) so one audit
// query answers "who changed this person" across both systems of record.
const (
	auditActionUpdate  = "update"
	auditActionArchive = "archive"
)

// auditWriteBack records one write-back as a governed mutation: the
// audit_log row naming the principal, and the record's own public event,
// both on tx.
//
// before/after carry the record's field images restricted to the fields
// this write actually touched — the same discipline the native update path
// keeps (people/lead_update.go's patch Before()/After()), and the reason a
// write-back audit row does not become a full copy of an incumbent record
// in audit_log. Both images are narrowed by minimizeAuditImage first.
// externalID travels as evidence rather than in those images: it is context
// ABOUT the mutation (which incumbent record it landed on), and folding it
// into the field images would make a field-history projection read it as a
// field change that never happened.
func auditWriteBack(ctx context.Context, tx pgx.Tx, action string, ref datasource.EntityRef,
	externalID string, before, after map[string]any,
) error {
	auditID, err := storekit.AuditWithEvidence(ctx, tx, action, string(ref.Type), ref.ID,
		minimizeAuditImage(ref.Type, before), minimizeAuditImage(ref.Type, after),
		map[string]any{"system_of_record": "incumbent", keyExternalID: externalID})
	if err != nil {
		return fmt.Errorf("overlay: auditing the %s write-back of %s %s: %w", action, ref.Type, externalID, err)
	}
	if err := emitWriteBack(ctx, tx, auditID, action, ref, after); err != nil {
		return fmt.Errorf("overlay: announcing the %s write-back of %s %s: %w", action, ref.Type, externalID, err)
	}
	return nil
}

// emitWriteBack stages the record's own public event for a write-back —
// the SAME event type the native path emits for the same verb, because a
// subscriber must not have to know which system of record served the write
// to recognize that a person changed.
//
// A verb/type pair with no case here is a programming error rather than a
// silent no-op: the write verbs refuse everything SupportsWrite does not
// declare (provider_writes.go), so an unhandled pair means the two have
// drifted, and an un-announced mutation is exactly the silence this file
// exists to prevent.
func emitWriteBack(ctx context.Context, tx pgx.Tx, auditID ids.UUID, action string,
	ref datasource.EntityRef, after map[string]any,
) error {
	switch action {
	case auditActionArchive:
		return emitWriteBackArchived(ctx, tx, auditID, ref)
	case auditActionUpdate:
		return emitWriteBackUpdated(ctx, tx, auditID, ref, after)
	default:
		return fmt.Errorf("no write-back event for action %q on %s", action, ref.Type)
	}
}

// emitWriteBackArchived stages the archived event for the three types
// Archive supports (archivableTypes).
func emitWriteBackArchived(ctx context.Context, tx pgx.Tx, auditID ids.UUID, ref datasource.EntityRef) error {
	switch ref.Type {
	case datasource.EntityPerson:
		return storekit.EmitEvent(ctx, tx, auditID, ref.ID, crmcontracts.PublicEventPersonArchived{})
	case datasource.EntityOrganization:
		return storekit.EmitEvent(ctx, tx, auditID, ref.ID, crmcontracts.PublicEventOrganizationArchived{})
	case datasource.EntityDeal:
		return storekit.EmitEvent(ctx, tx, auditID, ref.ID, crmcontracts.PublicEventDealArchived{})
	default:
		return fmt.Errorf("no archived event for %s", ref.Type)
	}
}

// emitWriteBackUpdated stages the updated event for the five types Update
// supports, carrying the fields the write touched as changed_fields.
func emitWriteBackUpdated(ctx context.Context, tx pgx.Tx, auditID ids.UUID,
	ref datasource.EntityRef, after map[string]any,
) error {
	switch ref.Type {
	case datasource.EntityPerson:
		return storekit.EmitEvent(ctx, tx, auditID, ref.ID, crmcontracts.PublicEventPersonUpdated{ChangedFields: after})
	case datasource.EntityOrganization:
		return storekit.EmitEvent(ctx, tx, auditID, ref.ID, crmcontracts.PublicEventOrganizationUpdated{ChangedFields: after})
	case datasource.EntityDeal:
		return storekit.EmitEvent(ctx, tx, auditID, ref.ID, crmcontracts.PublicEventDealUpdated{ChangedFields: after})
	case datasource.EntityLead:
		return storekit.EmitEvent(ctx, tx, auditID, ref.ID, crmcontracts.PublicEventLeadUpdated{ChangedFields: after})
	case datasource.EntityActivity:
		changed, err := activityChangedFields(after)
		if err != nil {
			return err
		}
		return storekit.EmitEvent(ctx, tx, auditID, ref.ID, crmcontracts.PublicEventActivityUpdated{ChangedFields: changed})
	default:
		return fmt.Errorf("no updated event for %s", ref.Type)
	}
}

// activityChangedFields projects an activity patch onto activity.updated's
// BOUNDED delta. Unlike person/organization/deal/lead.updated — whose
// changed_fields is a genuinely open map — this event's key set is fixed and
// typed, so the patch has to be narrowed rather than passed through.
//
// The projection is a JSON round-trip rather than a field-by-field copy
// because both sides are generated from the SAME contract: the patch keys
// are UpdateActivityRequest's json tags, and the delta's keys are those same
// names by construction, so a hand-written mapping would only be a second
// place for them to drift. Keys outside the delta's schema drop out, which
// is what "bounded" means.
//
// body is the one deliberate exception: the delta carries a PRESENCE FLAG,
// never the content ("bodies can be large and are never echoed onto the
// wire"), so it is removed before the round-trip and re-stated as a bool.
func activityChangedFields(patch map[string]any) (crmcontracts.PublicEventActivityChangedFields, error) {
	narrowed := make(map[string]any, len(patch))
	for k, v := range patch {
		if k == "body" {
			continue
		}
		narrowed[k] = v
	}
	raw, err := json.Marshal(narrowed)
	if err != nil {
		return crmcontracts.PublicEventActivityChangedFields{}, fmt.Errorf("encoding the activity delta: %w", err)
	}
	var changed crmcontracts.PublicEventActivityChangedFields
	if err := json.Unmarshal(raw, &changed); err != nil {
		return crmcontracts.PublicEventActivityChangedFields{}, fmt.Errorf("projecting the activity delta: %w", err)
	}
	if _, touched := patch["body"]; touched {
		bodyTouched := true
		changed.Body = &bodyTouched
	}
	return changed, nil
}

// writeBackLocalTimeout bounds the local half of a write-back once it is
// detached from the caller's cancellation. Detached work without a deadline
// of its own would hang a request worker on a stalled database indefinitely
// — the same pairing the vault cleanup makes (connection.go).
const writeBackLocalTimeout = 10 * time.Second

// afterIncumbentCommit returns the context the local half of a write-back
// runs on: DETACHED from the caller's cancellation, under its own deadline.
//
// Past the incumbent's commit there is no longer a request to abandon. The
// canonical row has already changed in the customer's CRM, and everything
// still owed — the mirror refresh, the audit_log row, the outbox event, the
// echo-ledger entries — describes a change that HAS happened. Letting a
// closed browser tab cancel that work is what turns a completed archive into
// a record still listed and still readable, with no audit trail, until the
// next full reconcile sweep. So the caller can walk away; the bookkeeping
// cannot.
func afterIncumbentCommit(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), writeBackLocalTimeout)
}

// writePathError translates the fence's internal control signal into a
// sentinel a transport can answer with.
//
// ErrConnectionGone is deliberately not an apperrors sentinel — it is the
// sweep's clean STOP, and its own doc says it "never crosses an HTTP/MCP
// boundary" because the on-demand reconcile path collapses it first. The
// human write path is the second boundary that needs the same collapse: a
// disconnect racing an in-flight write leaves the fenced local half aborting
// with it, and an unmapped control signal reaching httperr is an opaque 500
// for what is really "this workspace no longer reads from an incumbent" —
// 404 mode_not_overlay, exactly as every other post-disconnect request
// answers. Any other error passes through untouched.
func writePathError(err error) error {
	if errors.Is(err, ErrConnectionGone) {
		return apperrors.ErrModeNotOverlay
	}
	// A pending flip holds the mirror still; the write-back path folds it
	// the same way the user-map service does, so a frozen workspace never
	// answers an opaque 500 on either surface.
	return foldMirrorFrozen(err)
}

// commitUpdateWriteBack lands the LOCAL half of a completed create/update
// write-back in one transaction: the mirror refresh that carries the
// incumbent's post-write state, plus the audit_log and event_outbox rows
// that record who made it. See this file's header on what that atomicity
// does and does not promise.
//
// The store is bound to the LIVE incumbent (WithResolver) so the ingest's
// owner re-validation resolves against the real adapter rather than the
// read-path placeholder that always fails, and the disconnect fence is
// engaged (WithFence) so a write landing after a Disconnect cannot
// repopulate the purged mirror.
func (p *Provider) commitUpdateWriteBack(ctx context.Context, inc Incumbent, rec Record,
	ref datasource.EntityRef, before, after map[string]any,
) error {
	if p.ms == nil {
		return errNoMirrorStore()
	}
	ms := p.ms.WithResolver(inc).WithFence()
	var landed bool
	err := database.WithWorkspaceTx(ctx, ms.pool, func(tx pgx.Tx) error {
		var ingestErr error
		if landed, ingestErr = ms.ingestTx(ctx, tx, rec); ingestErr != nil {
			return ingestErr
		}
		return auditWriteBack(ctx, tx, auditActionUpdate, ref, rec.ExternalID, before, after)
	})
	if err == nil && landed {
		mirrorSyncedTotal.Add(1)
	}
	return err
}

// commitArchiveWriteBack is commitUpdateWriteBack for the archive verb: the
// mirror purge that stops the record being readable, plus the archive's own
// audit_log and event_outbox rows, in one transaction.
//
// No resolver binding here, unlike the update path: a purge writes no owner
// projection, so there is no owner email to re-validate.
func (p *Provider) commitArchiveWriteBack(ctx context.Context, del Deletion, ref datasource.EntityRef) error {
	if p.ms == nil {
		return errNoMirrorStore()
	}
	ms := p.ms.WithFence()
	var existed bool
	err := database.WithWorkspaceTx(ctx, ms.pool, func(tx pgx.Tx) error {
		var purgeErr error
		if existed, purgeErr = ms.purgeRecordTx(ctx, tx, del); purgeErr != nil {
			return purgeErr
		}
		// before/after stay nil, matching the native archive audit rows
		// (people/person.go, deals/deal.go): an archive changes no field
		// values, it retires the record.
		return auditWriteBack(ctx, tx, auditActionArchive, ref, del.ExternalID, nil, nil)
	})
	if err == nil && existed {
		mirrorDeletedTotal.Add(1)
	}
	return err
}

// contentFreeAuditFields names, per entity type, the fields whose VALUE never
// enters an audit image — only the fact that the write touched them.
//
// An activity body is the one such field today, and the rule is the native
// path's, not a new one: activities/lifecycle.go records `body: true` rather
// than the text, because bodies are large and are the message content itself.
// It binds harder on the overlay path than on the native one. A mirrored
// body is INCUMBENT-sourced — customer correspondence synced out of HubSpot —
// and audit_log is append-only, sits under the retention floor, and is served
// verbatim to unbounded compliance readers. Copying that text there would put
// incumbent message content in the one store neither Art. 17 erasure nor
// disconnect teardown reaches, which is the opposite of what this file's own
// header gives as the reason not to audit mirror sync.
var contentFreeAuditFields = map[datasource.EntityType]map[string]bool{
	datasource.EntityActivity: {"body": true},
}

// minimizeAuditImage replaces a content-free field's value with the presence
// flag `true`, leaving every other field as-is. A nil image stays nil: an
// archive audits no field values at all, and an empty object would read as
// "these fields changed to nothing".
//
// It is applied inside auditWriteBack rather than at each call site so every
// verb — and every verb added later — inherits it.
func minimizeAuditImage(et datasource.EntityType, image map[string]any) map[string]any {
	if image == nil {
		return nil
	}
	contentFree := contentFreeAuditFields[et]
	if len(contentFree) == 0 {
		return image
	}
	out := make(map[string]any, len(image))
	for k, v := range image {
		if contentFree[k] {
			out[k] = true
			continue
		}
		out[k] = v
	}
	return out
}

// beforeImage reads the pre-write values of exactly the fields a patch
// touches, out of the mirror row that supplied the write's drift baseline.
// A field the mirror never held reads as nil, which is the honest before
// value for a field the incumbent had not set.
func beforeImage(row Row, patch map[string]any) map[string]any {
	before := make(map[string]any, len(patch))
	for k := range patch {
		before[k] = row.Fields[k]
	}
	return before
}
