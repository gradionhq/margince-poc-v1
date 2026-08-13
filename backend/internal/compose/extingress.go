// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The ingress port: what a unit hands the core when it pulls a record out of
// its provider, and everything the core decides about that record rather than
// letting the unit decide it.
//
// The shape to hold onto is that this adapter converts and REFUSES, and writes
// nothing itself. Every durable effect belongs to capture's Sink — the
// idempotent upsert, the counterparty ladder, the raw evidence, the audit row
// and the outbox event in one transaction — so there is no second write shape
// here to keep in step with the first. What this file owns is the authority the
// write runs under, the provenance it carries, and the bounds a remote party is
// held to before any of it opens a transaction.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// errIngressUnwired is a role that composed no capture pipeline. It refuses BY
// NAME rather than building a bare sink at the call, and that distinction is
// the whole reason this error exists: newCaptureSink attaches the merge stager,
// the file keeper and — the one that matters — the counterparty ensurer, so a
// sink assembled here from the pool alone would compile, run, land activities,
// and silently create no people. A refusal is loud; a half-wired pipeline is
// not.
var errIngressUnwired = errors.New("compose: this role composed no capture pipeline, so a unit cannot ingest through it")

// ingressPrincipalPrefix opens the connector identity every ingested record is
// stamped with. It is `connector:` because that is what capture's own sink
// requires of an acting principal, and `ext:` after it so a unit's records can
// never be mistaken in the ledger for a core connector's.
const ingressPrincipalPrefix = "connector:ext:"

// Ingest hands one record to the installation's capture pipeline.
//
// The order of the refusals is the order in which they can be answered without
// spending anything: what the unit declared, then what kind of invocation this
// is, then the record's own shape, and only then the two that cost a query —
// the member's consent and their live authority.
func (r *callRuntime) Ingest(ctx context.Context, on extension.UserID, rec extension.Record) (extension.Result, error) {
	// The declared source is resolved rather than read: what comes back is
	// unused today and that is a fact about the vocabulary, not an oversight.
	// A source declares which KINDS it lands, and exactly one kind is landable
	// (extension.KindActivity), which the declaration grammar already enforces
	// at generation and at boot — while a Record carries one Activity and has
	// no field a second kind could arrive in. So there is no call this side
	// could refuse for landing an undeclared kind. When a second kind is
	// published, the gate belongs here, against Lands.
	if _, err := r.declaredIngress(rec.System); err != nil {
		return extension.Result{}, err
	}
	// An invocation with a caller has two authorities in play — the caller's
	// and the member's — and the shape where those differ is the one a
	// low-privileged caller uses to have a unit act as somebody else and read
	// the answer back. Refused before anything is spent.
	if !r.unattended {
		return extension.Result{}, extension.ErrAttendedIngest
	}
	if r.insideTx() {
		return extension.Result{}, extension.ErrNestedIngest
	}
	if err := rec.Validate(); err != nil {
		return extension.Result{}, fmt.Errorf("%w: %s", extension.ErrInvalid, err.Error())
	}
	// Whether this ROLE can accept a record at all is answered before the
	// call's context is rebound: it costs nothing, it is the same answer for
	// every call, and a deployment fault should not read as a refusal about
	// this particular record.
	sink := r.deps.captureSink
	if sink == nil {
		return extension.Result{}, errIngressUnwired
	}
	ctx, err := r.scoped(ctx)
	if err != nil {
		return extension.Result{}, err
	}
	runCtx, err := r.ingressAuthority(ctx, on)
	if err != nil {
		return extension.Result{}, err
	}
	return r.landRecord(runCtx, sink, rec)
}

// landRecord performs the one write and maps its outcome onto the published
// dispositions.
//
// The skip arm is the load-bearing one. Capture drops a wholly-internal message
// on purpose, commits a breadcrumb saying so, and reports it as an
// ErrSkip-wrapped error — and its own contract is that a skip ADVANCES a
// connector's watermark. Reporting that to a unit as a failure would have the
// unit retry a deliberate drop on every poll, forever, so it is a success here
// with a disposition that says what happened.
func (r *callRuntime) landRecord(ctx context.Context, sink *capture.Sink, rec extension.Record) (extension.Result, error) {
	ref, err := sink.Upsert(ctx, r.normalized(rec))
	switch {
	case errors.Is(err, connector.ErrSkip):
		return extension.Result{Disposition: extension.DispositionSkipped}, nil
	case err != nil:
		return extension.Result{}, r.ingressRefusal(ctx, err)
	}
	return extension.Result{
		Ref:         extension.Ref{Type: string(ref.Type), ID: ref.ID.String()},
		Disposition: extension.DispositionAccepted,
	}, nil
}

// ingressAuthority builds the principal one ingest runs as: the connector
// identity, wearing the LIVE permissions of the member whose credential
// produced the record.
//
// Two facts are established before any of that, and neither is the unit's to
// assert. The member must currently hold one of this unit's user-scoped
// secrets, because depositing a credential with a unit is the act that says
// "act for me here" — without it a unit could name any colleague and land
// records on their authority. And their authority is resolved fresh, so a
// member demoted since they connected narrows what their connection can land
// from this call onward, exactly as a passport narrows.
func (r *callRuntime) ingressAuthority(ctx context.Context, on extension.UserID) (context.Context, error) {
	member, err := ids.Parse(string(on))
	if err != nil {
		return nil, fmt.Errorf("%w: the member id is not a canonical UUID", extension.ErrInvalid)
	}
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return nil, errExtensionRuntimeUnwired
	}
	consented, err := extensionMemberConsented(ctx, r.deps.pool, r.unit, member)
	if err != nil {
		return nil, r.ingressRefusal(ctx, err)
	}
	if !consented {
		// Deliberately ErrForbidden and not ErrNotFound: this says nothing
		// about whether the member exists, only that they have not asked THIS
		// unit to act for them.
		return nil, fmt.Errorf("%w: that member has deposited no credential with this unit", extension.ErrForbidden)
	}
	rbac, seat, err := liveMemberAuthority(ctx, r.deps.pool, ws, member)
	if err != nil {
		return nil, r.ingressRefusal(ctx, err)
	}
	acting := principal.Principal{
		Type:        principal.PrincipalConnector,
		ID:          ingressPrincipalPrefix + r.unit,
		UserID:      member,
		OnBehalfOf:  member,
		TeamIDs:     rbac.TeamIDs,
		SeatType:    seat,
		Permissions: rbac.Permissions,
	}
	runCtx := principal.WithActor(ctx, acting)
	return principal.WithCorrelationID(runCtx, ids.NewV7()), nil
}

// normalized converts the published record into the core's own, stamping the
// two fields a unit does not carry.
//
// Source and CapturedBy are derived from the invoking unit and the source it
// DECLARED, never from the record. CapturedBy is also the acting principal's
// id, which is what makes the sink's own "a connector cannot claim to be
// another one" check pass by construction rather than by the unit getting it
// right.
func (r *callRuntime) normalized(rec extension.Record) connector.NormalizedRecord {
	return connector.NormalizedRecord{
		EntityType: datasource.EntityActivity,
		NaturalKey: r.naturalKey(rec),
		Fields: capture.ActivityFields{
			Kind:       rec.Activity.Kind,
			Subject:    rec.Activity.Subject,
			Body:       rec.Activity.Body,
			OccurredAt: rec.Activity.OccurredAt,
			Direction:  rec.Activity.Direction,
		},
		Source:     r.sourceSystem(rec.System),
		CapturedBy: ingressPrincipalPrefix + r.unit,
		ThreadKey:  rec.ThreadKey,
		Addresses:  rec.Addresses,
		Raw:        rec.Raw,
		Counterparty: connector.Counterparty{
			Email:       rec.Counterparty.Email,
			DisplayName: rec.Counterparty.DisplayName,
			Domain:      rec.Counterparty.Domain,
			Direction:   rec.Counterparty.Direction,
		},
	}
}

// naturalKey is the idempotency key the database's unique index enforces. Its
// system half is core-derived, so two units — or one unit and a core connector
// — can never collide in it whatever they name their records.
func (r *callRuntime) naturalKey(rec extension.Record) connector.NaturalKey {
	return connector.NaturalKey{SourceSystem: r.sourceSystem(rec.System), SourceID: rec.Key}
}

func (r *callRuntime) sourceSystem(system string) string {
	return "ext:" + r.unit + ":" + system
}

// declaredIngress resolves the source the record names against what the
// invoking unit actually declared.
//
// This is what makes the manifest a contract rather than a description: an
// operator reading manifest.generated.json sees every provider a unit reaches
// core capture from, and a unit cannot land a record under a name that is not
// on that list — so a typo is a refusal at the call rather than a second
// provenance namespace nobody knows exists.
func (r *callRuntime) declaredIngress(system string) (extension.IngressSource, error) {
	for _, declared := range composedIngressFor(r.unit) {
		if declared.System == system {
			return declared, nil
		}
	}
	return extension.IngressSource{}, fmt.Errorf("%w: %q", extension.ErrIngressNotDeclared, system)
}

// ingressRefusal maps a core error onto the published classes, logging the
// original where it belongs.
//
// It maps rather than wraps for the reason the core port's equivalent does: the
// sink's errors carry table names, constraint names and SQL state, and a unit
// is other people's code. What survives is the class, which is the only part a
// unit can act on.
func (r *callRuntime) ingressRefusal(ctx context.Context, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, apperrors.ErrPermissionDenied):
		return extension.ErrForbidden
	case errors.Is(err, apperrors.ErrNotFound):
		return extension.ErrNotFound
	case errors.Is(err, apperrors.ErrConflict), errors.Is(err, apperrors.ErrVersionSkew):
		return extension.ErrConflict
	}
	var fault apperrors.FieldFault
	if errors.As(err, &fault) {
		return extension.ErrInvalid
	}
	slog.ErrorContext(ctx, "compose: an extension ingest failed", "err", err, "unit", r.unit)
	return errors.New("extension: the core could not land this record")
}
