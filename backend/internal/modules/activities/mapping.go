// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Contract request → store input mappings, in ONE place: the HTTP
// handlers and the SoR provider (the MCP surface's door) both decode the
// same crm.yaml shapes, and a defaulting rule that lived in only one of
// them would make the two surfaces silently disagree.

import (
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

// RequiredFieldError maps to 422 on both surfaces.
type RequiredFieldError struct{ Field string }

func (e *RequiredFieldError) Error() string { return e.Field + " is required" }

// FieldFault names the missing required field, on every surface.
func (e *RequiredFieldError) FieldFault() (field, code, message string) {
	return e.Field, "required", e.Error()
}

// fieldBody names the activity body field in a FieldFault, the one spelling
// every 422 that refuses something about it shares (mirrors activity.go's
// fieldKind).
const fieldBody = "body"

// transcriptSourceSystem is the ADR-0058 marker: a `logActivity` call
// carrying it identifies its body as pasted/uploaded transcript text rather
// than ordinary meeting notes, which is what routes it through
// normalizeTranscript and what the activity/transcript retention selector
// (privacy/retentionselectors.go) keys its sweep on.
const transcriptSourceSystem = "transcript"

// faultInvalid is the RFC 7807 code every field refusal in this module carries:
// the value is well-formed enough to have reached us and is still not one this
// field accepts.
const faultInvalid = "invalid"

// TranscriptKindError maps to 422: a transcript is a recording of a
// conversation, so it only makes sense on the two activity kinds that ARE
// one. The message never echoes the caller's kind — this fires before the
// kind ever reaches the DB CHECK that would otherwise be the only
// validation of it, so an unbounded value has no other floor here.
type TranscriptKindError struct{ Kind string }

func (e *TranscriptKindError) Error() string {
	return "only a call or meeting activity may carry a transcript"
}

// FieldFault names the offending field; the caller's value is left to the
// wire's own field pointer, not interpolated into the message.
func (e *TranscriptKindError) FieldFault() (field, code, message string) {
	return "kind", faultInvalid, e.Error()
}

// pathID asserts a contract path id as entity K's id — the widening
// point between the wire and the typed store surface (the route already
// names the entity, so the assertion lives here, not in the store).
func pathID[K ids.EntityKind](id crmcontracts.Id) ids.ID[K] {
	return ids.From[K](ids.UUID(id))
}

// idArg asserts an optional wire UUID (body field or query parameter)
// as entity K's id; nil stays nil.
func idArg[K ids.EntityKind](u *openapi_types.UUID) *ids.ID[K] {
	if u == nil {
		return nil
	}
	v := ids.From[K](ids.UUID(*u))
	return &v
}

// activityUpdateInput maps the contract patch onto the store's input. Both
// transports go through it — the HTTP handler and the datasource seam's
// Update — so an activity patch cannot mean one thing over REST and another
// over the tool surface, which is the whole point of the seam.
func activityUpdateInput(req crmcontracts.UpdateActivityRequest, ifVersion *int64) UpdateActivityInput {
	return UpdateActivityInput{
		Subject:    req.Subject,
		Body:       req.Body,
		OccurredAt: req.OccurredAt,
		DueAt:      req.DueAt,
		RemindAt:   req.RemindAt,
		AssigneeID: idArg[ids.UserKind](req.AssigneeId),
		IsDone:     req.IsDone,
		IfVersion:  ifVersion,
	}
}

// LogActivityInputFrom maps the contract's create request onto the store's
// input. It is exported for the composition layer, which hands an extension's
// core write to LogActivityTx and must map it the way the HTTP handler does —
// a second mapping written beside this one would be a second set of rules
// about the reserved import namespace, and the two would drift.
func LogActivityInputFrom(req crmcontracts.CreateActivityRequest) (LogActivityInput, error) {
	if req.Kind == "" {
		return LogActivityInput{}, &RequiredFieldError{Field: "kind"}
	}
	// The importer's namespace is not a client's to write: this store
	// keys its idempotent replay on (source_system, source_id), so a
	// caller who could spell the reserved prefix could pre-plant a row
	// under an incumbent record id and have a later import hand it back
	// as already existing (provenance.ReservedSourceSystemPrefix).
	if req.SourceSystem != nil {
		if err := provenance.Refuse("source_system", *req.SourceSystem); err != nil {
			return LogActivityInput{}, err
		}
	}
	if err := provenance.Refuse("source", req.Source); err != nil {
		return LogActivityInput{}, err
	}
	in := LogActivityInput{
		Kind: string(req.Kind),
		// A caller naming a kind that IS a registered transport is naming the
		// transport, and the row records it. The contract has no provider field
		// yet, so this is the only place that intent can be read — and without it
		// a hand-logged or agent-logged channel activity would store no transport,
		// which would make it unrepliable while an identical row written before the
		// column existed stayed repliable, because the migration backfilled that
		// one. Same data, different behaviour decided by write date.
		//
		// This is a translation of a legacy input shape, not a rule: it holds only
		// while kind still carries provider names, and it goes away when the
		// contract gains a provider of its own and kind narrows to the interaction
		// vocabulary. It is NOT the derivation the send path used to do — that one
		// read a stored kind back as a provider at reply time, long after any
		// caller could say what they meant.
		ChannelProvider: ChannelProviderForKind(string(req.Kind)),
		Subject:         req.Subject,
		Body:            req.Body,
		OccurredAt:      req.OccurredAt,
		DueAt:           req.DueAt,
		RemindAt:        req.RemindAt,
		SourceSystem:    req.SourceSystem,
		SourceID:        req.SourceId,
		Source:          req.Source,
		AssigneeID:      idArg[ids.UserKind](req.AssigneeId),
	}
	if req.Direction != nil {
		d := string(*req.Direction)
		in.Direction = &d
	}
	if req.Links != nil {
		for _, link := range *req.Links {
			in.Links = append(in.Links, ActivityLinkInput{
				EntityType: string(link.EntityType),
				EntityID:   ids.UUID(link.EntityId),
			})
		}
	}
	if in.SourceSystem != nil && *in.SourceSystem == transcriptSourceSystem {
		if in.Kind != string(crmcontracts.ActivityKindCall) && in.Kind != string(crmcontracts.ActivityKindMeeting) {
			return LogActivityInput{}, &TranscriptKindError{Kind: in.Kind}
		}
		var body string
		if in.Body != nil {
			body = *in.Body
		}
		normalized, err := normalizeTranscript(body)
		if err != nil {
			return LogActivityInput{}, err
		}
		in.Body = &normalized
	}
	return in, nil
}
