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

	"github.com/gradionhq/margince/backend/internal/modules/activities"
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
	// The declared source decides what this record may contribute to identity.
	//
	// Lands is still not gated here, and that is a fact about the vocabulary
	// rather than an oversight: exactly one kind is landable
	// (extension.KindActivity), the declaration grammar already enforces it at
	// generation and at boot, and a Record carries one Activity with no field a
	// second kind could arrive in. When a second kind is published, that gate
	// belongs here too.
	declared, err := r.declaredIngress(rec.System)
	if err != nil {
		return extension.Result{}, err
	}
	if err := refuseUndeclaredMergeKey(declared, rec.Counterparty); err != nil {
		return extension.Result{}, err
	}
	// An invocation with a caller has two authorities in play — the caller's
	// and the member's — and the shape where those differ is the one a
	// low-privileged caller uses to have a unit act as somebody else and read
	// the answer back. Refused before anything is spent.
	if !r.unattended {
		return extension.Result{}, extension.ErrAttendedIngest
	}
	// Claimed rather than merely checked: capture opens its transaction after
	// this returns, so a check that did not hold the slot would be true when
	// it was made and false when it mattered.
	if err := r.beginIngest(); err != nil {
		return extension.Result{}, err
	}
	defer r.endIngest()
	if err := rec.Validate(); err != nil {
		return extension.Result{}, fmt.Errorf("%w: %s", extension.ErrInvalid, err.Error())
	}
	if err := refuseUndeclaredTransport(r.unit, rec.Activity.Kind, rec.Activity.ChannelProvider); err != nil {
		return extension.Result{}, err
	}
	if err := refuseUnitIdentity(r.unit, rec.Counterparty.ChannelIdentity.Provider); err != nil {
		return extension.Result{}, err
	}
	// Whether this ROLE can accept a record at all is answered before the
	// call's context is rebound: it costs nothing, it is the same answer for
	// every call, and a deployment fault should not read as a refusal about
	// this particular record.
	sink := r.deps.captureSink
	if sink == nil {
		return extension.Result{}, errIngressUnwired
	}
	ctx, err = r.scoped(ctx)
	if err != nil {
		return extension.Result{}, err
	}
	runCtx, err := r.ingressAuthority(ctx, on)
	if err != nil {
		return extension.Result{}, err
	}
	return r.landRecord(runCtx, sink, rec, declared)
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
func (r *callRuntime) landRecord(ctx context.Context, sink *capture.Sink, rec extension.Record, declared extension.IngressSource) (extension.Result, error) {
	ref, err := sink.Upsert(ctx, r.normalized(rec, declared))
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

func (r *callRuntime) normalized(rec extension.Record, declared extension.IngressSource) connector.NormalizedRecord {
	return connector.NormalizedRecord{
		EntityType: datasource.EntityActivity,
		NaturalKey: r.naturalKey(rec),
		Fields: capture.ActivityFields{
			Kind: rec.Activity.Kind,
			// The transport the unit named, already held to what it DECLARED
			// (refuseUndeclaredTransport) — so this carries a provider the
			// installation registered rather than a string the unit chose.
			ChannelProvider: rec.Activity.ChannelProvider,
			Subject:         rec.Activity.Subject,
			Body:            rec.Activity.Body,
			OccurredAt:      rec.Activity.OccurredAt,
			Direction:       rec.Activity.Direction,
		},
		Source:       r.sourceSystem(rec.System),
		CapturedBy:   ingressPrincipalPrefix + r.unit,
		ThreadKey:    rec.ThreadKey,
		Addresses:    rec.Addresses,
		Raw:          rec.Raw,
		Counterparty: counterpartyOf(rec.Counterparty, declared),
	}
}

// counterpartyOf maps the published counterparty onto the core's and stamps what
// the SOURCE declared onto it.
//
// The declaration is stamped, never taken from the record — the rule Source and
// CapturedBy already follow, for the same reason: a unit that could state its own
// trust could widen it per record, which is the whole thing a declaration exists
// to bound. It is reduced to the one question the core asks (may this address
// corroborate?) rather than carried as the key list, which belongs to the
// manifest an operator reads.
func counterpartyOf(cp extension.Counterparty, declared extension.IngressSource) connector.Counterparty {
	return connector.Counterparty{
		Email:       cp.Email,
		DisplayName: cp.DisplayName,
		Domain:      cp.Domain,
		Direction:   cp.Direction,
		// The binding that makes the record repliable, held to the same
		// declared set the activity's own transport is (refuseUnitIdentity):
		// a unit that could bind an identity under a core connector's
		// provider would be writing into the table the workspace's own
		// reply path resolves its recipients from.
		//
		// DisplayName lands on Username, which is the core's display-only
		// field for exactly this: a handle nothing routes, authorizes or
		// deduplicates on. The two names differ because the core's is
		// Telegram-shaped and the published one is not, and renaming either
		// would be a change at a security-sensitive identity for a word.
		ChannelIdentity: connector.ChannelIdentity{
			Provider:      cp.ChannelIdentity.Provider,
			ChannelUserID: cp.ChannelIdentity.ChannelUserID,
			Username:      cp.ChannelIdentity.DisplayName,
		},
	}.WithDeclaredEmailMerge(declaresMergeKey(declared, extension.MergeKeyEmail))
}

// declaresMergeKey reports whether a source vouched for one identity key.
func declaresMergeKey(declared extension.IngressSource, key extension.MergeKey) bool {
	for _, got := range declared.Merges {
		if got == key {
			return true
		}
	}
	return false
}

// refuseUndeclaredMergeKey is the unit-facing half of the merge-key gate: a
// source may offer an address to MATCH on only if it vouched for one.
//
// It is the attributable refusal — a unit author reads their own grammar rather
// than an unattributable "the core could not land this record" — and it is not
// the invariant. Capture's own admitCounterpartyKeys holds that, for every
// caller of Upsert including the ones that never pass through here. Two layers,
// the way refuseUndeclaredTransport and refuseUnitIdentity are two layers, not
// two spellings of one rule.
//
// It asks only about an address CORROBORATING a human named by a channel
// identity. A mail-shaped record's address IS that record's identity, belongs to
// no declaration, and is untouched.
func refuseUndeclaredMergeKey(declared extension.IngressSource, cp extension.Counterparty) error {
	namedByChannel := cp.ChannelIdentity.Provider != "" && cp.ChannelIdentity.ChannelUserID != ""
	if !namedByChannel || cp.Email == "" {
		return nil
	}
	if declaresMergeKey(declared, extension.MergeKeyEmail) {
		return nil
	}
	return fmt.Errorf("%w: ingress source %q carries a counterparty address to match on but declares no %q merge key",
		extension.ErrInvalid, declared.System, string(extension.MergeKeyEmail))
}

// refuseUndeclaredTransport holds the kind/transport pairing on ANY of a unit's
// write doors, and it is deliberately one function rather than a check at each.
//
// It is a BOUNDED PERMISSION, and the bound is the point. A unit may file a
// message on a transport it DECLARED (extension.Channel) and on no other. What
// it may not do is name somebody else's: the core-write door carries
// channel_provider on the published request, so a unit naming `telegram` would
// mint a row that is a valid SEND ANCHOR for a conversation it does not own —
// a rep or an approved agent replying on it would transmit a real message from
// the workspace's bot to whoever the unit linked, the unit picking the target
// and the human supplying the authority. It would also inherit that transport's
// statutory retention floor, pinning unit-supplied text past the workspace's
// own policy.
//
// The other direction is refused too, and it is the quieter defect: a NON-message
// naming a transport is a record claiming to have travelled somewhere it did
// not. The send path reads the provider column and not the kind since
// ADR-0107/A158, so such a row would be repliable — a note that answers back.
func refuseUndeclaredTransport(unit, kind, provider string) error {
	if kind != activities.KindMessage {
		if provider == "" {
			return nil
		}
		return fmt.Errorf("%w: a %q activity names the transport %q — only a %q carries one, and a record claiming a transport it did not travel on is one the reply path would answer on",
			extension.ErrInvalid, kind, provider, activities.KindMessage)
	}
	if provider == "" {
		return fmt.Errorf("%w: a %q activity names no transport — a message that does not say what carried it cannot be replied to on anything",
			extension.ErrInvalid, activities.KindMessage)
	}
	for _, declared := range composedChannelsFor(unit) {
		if declared.Provider == provider {
			return nil
		}
	}
	return fmt.Errorf("%w: this unit does not supply the transport %q — a unit may file a message on a channel it declared and on no other",
		extension.ErrInvalid, provider)
}

// refuseUnitIdentity holds a record's counterparty BINDING to the same declared
// set its transport is held to.
//
// It is a second check rather than a reuse of the pairing above because it
// answers about a different row. The activity's provider decides where a reply
// would be SENT; this one writes person_channel_identity, which is where the
// core's reply path resolves WHO it is sent to. A unit able to bind an identity
// under `telegram` could attach an account it controls to somebody else's person
// record, and the next Telegram reply a rep writes on that person's conversation
// would go to the unit's account instead — no message the unit filed involved.
//
// An empty provider is a record that identifies its counterparty by address, and
// binds nothing.
func refuseUnitIdentity(unit, provider string) error {
	if provider == "" {
		return nil
	}
	for _, declared := range composedChannelsFor(unit) {
		if declared.Provider == provider {
			return nil
		}
	}
	return fmt.Errorf("%w: this unit does not supply the transport %q, so it cannot bind an account on it — the reply path resolves its recipients from those bindings",
		extension.ErrInvalid, provider)
}

// naturalKey is the idempotency key the database's unique index enforces. Its
// system half is core-derived, so two units — or one unit and a core connector
// — can never collide in it whatever they name their records.
func (r *callRuntime) naturalKey(rec extension.Record) connector.NaturalKey {
	return connector.NaturalKey{SourceSystem: r.sourceSystem(rec.System), SourceID: rec.Key}
}

func (r *callRuntime) sourceSystem(system string) string {
	return extSourceSystem(r.unit, system)
}

// extSourceSystem spells the provenance a unit's landed record carries. One
// spelling in product code; the integration lane pins the bytes with a literal
// of its own, which is the point of that copy.
//
// Two callers need the same string for opposite reasons: this file WRITES it
// onto every record (the natural key and activity.source), and the transport
// directory PUBLISHES it with a label so a reader of the capture trace has a
// name to see. A second spelling of the grammar would resolve labels for ids no
// record ever carries while the ones that do arrive stay raw — a mismatch
// nothing fails on, because a directory miss is a fallback rather than an error.
func extSourceSystem(unit, system string) string {
	return "ext:" + unit + ":" + system
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
