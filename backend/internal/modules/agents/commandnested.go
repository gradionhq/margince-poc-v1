// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The seven remaining bespoke commands (gradionhq/margince-poc-v1#928 task 6):
// a list member add, a tag apply, an offer line item add/update/remove, an
// offer created under a parent deal, and a partner upsert. All seven are 🟢
// auto_execute today, so Subject/Guards are unreachable on today's tiers —
// but a route with no command of its own still answers stagedTargetByRoute's
// GUESS the moment a tier floor (#982) tightens it, and for createOffer that
// guess is wrong on its face: the routed {id} is the DEAL the offer is
// created ON, not an offer id, so the walk would pair
// target_entity_type=offer with a deal's id — a target that resolves to no
// row, or to an unrelated offer that happens to share the id space
// (gradionhq/margince-poc-v1#1046, closed by this file's CreateOfferCommand).
//
// list, tag and offer are all outside the record seam's vocabulary
// (servedByTheRecordSeam, command.go), the same bound six of the twelve
// archivable types already stand on — so five of these seven resolvers'
// Guards stand down, reusing that check rather than a hand-restated opinion
// (gradionhq/margince-poc-v1#1021). deal and organization ARE served, so
// createOffer's and upsertPartner's Guards perform a real read.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

const (
	// listRecordType and tagRecordType name AddListMemberCommand's and
	// ApplyTagCommand's fixed targets — plain strings, not
	// datasource.EntityType, for the same reason customFieldRecordType
	// (commandaction.go) is: the record seam has never served either.
	listRecordType = "list"
	tagRecordType  = "tag"
	// offerRecordType names the three offer-line-item commands' fixed
	// target. An offer itself is one of the six archivable types the seam
	// does not serve either.
	offerRecordType = "offer"
)

// AddListMemberCommand is one static-list member addition, whichever door
// asked for it — the routed list id only. It does not carry the member
// being added: neither Guards nor Subject reads it, the same reason
// UpdateFactCommand's own doc (commandsidecar.go) gives for dropping a
// value nothing here reads.
type AddListMemberCommand struct {
	ID ids.UUID
}

// NewAddListMemberCall binds one member addition to the resolver that
// answers for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewAddListMemberCall(records datasource.SystemOfRecordProvider, cmd AddListMemberCommand) GovernedCall {
	return bind[AddListMemberCommand](addListMemberResolver{
		target: routedRecordTarget{records: records, recordType: listRecordType},
	}, cmd)
}

type addListMemberResolver struct {
	target routedRecordTarget
}

// Subject names the LIST the approval binds to — a membership row has no
// row of its own on the seam.
func (r addListMemberResolver) Subject(_ context.Context, cmd AddListMemberCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: listRecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Add a member to list %s", cmd.ID),
	}, nil
}

// Guards stands down: the seam has never served `list`, so there is
// nothing here to read and nothing to refuse.
func (r addListMemberResolver) Guards(ctx context.Context, cmd AddListMemberCommand) error {
	return r.target.refuse(ctx, cmd.ID)
}

// ApplyTagCommand is one tag application, whichever door asked for it — the
// routed tag id only. It does not carry which record the tag is applied to:
// neither Guards nor Subject reads it, the same reasoning AddListMemberCommand's
// own doc gives.
type ApplyTagCommand struct {
	ID ids.UUID
}

// NewApplyTagCall binds one tag application to the resolver that answers
// for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewApplyTagCall(records datasource.SystemOfRecordProvider, cmd ApplyTagCommand) GovernedCall {
	return bind[ApplyTagCommand](applyTagResolver{
		target: routedRecordTarget{records: records, recordType: tagRecordType},
	}, cmd)
}

type applyTagResolver struct {
	target routedRecordTarget
}

// Subject names the TAG the approval binds to.
func (r applyTagResolver) Subject(_ context.Context, cmd ApplyTagCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: tagRecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Apply tag %s", cmd.ID),
	}, nil
}

// Guards stands down: the seam has never served `tag`.
func (r applyTagResolver) Guards(ctx context.Context, cmd ApplyTagCommand) error {
	return r.target.refuse(ctx, cmd.ID)
}

// AddOfferLineItemCommand is one offer line-item addition, whichever door
// asked for it — the routed offer id only. It does not carry the line
// item's own fields: neither Guards nor Subject reads them, the same
// reasoning AddListMemberCommand's own doc gives, and Guards does not check
// whether the offer is still a draft (the state the handler itself refuses
// a line-item add against) — that read is the executor's, not this
// approval's.
type AddOfferLineItemCommand struct {
	ID ids.UUID
}

// NewAddOfferLineItemCall binds one line-item addition to the resolver
// that answers for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewAddOfferLineItemCall(records datasource.SystemOfRecordProvider, cmd AddOfferLineItemCommand) GovernedCall {
	return bind[AddOfferLineItemCommand](addOfferLineItemResolver{
		target: routedRecordTarget{records: records, recordType: offerRecordType},
	}, cmd)
}

type addOfferLineItemResolver struct {
	target routedRecordTarget
}

// Subject names the OFFER the approval binds to.
func (r addOfferLineItemResolver) Subject(_ context.Context, cmd AddOfferLineItemCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: offerRecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Add a line item to offer %s", cmd.ID),
	}, nil
}

// Guards stands down: the seam has never served `offer`.
func (r addOfferLineItemResolver) Guards(ctx context.Context, cmd AddOfferLineItemCommand) error {
	return r.target.refuse(ctx, cmd.ID)
}

// UpdateOfferLineItemCommand is one offer line-item edit, whichever door
// asked for it — the routed offer id plus LineItemID, the SECOND path
// parameter naming which line changes. It does not carry the edited
// fields, the same reasoning AddOfferLineItemCommand's own doc gives, and
// Guards does not check that LineItemID names a line actually on this
// offer — that existence read is the handler's, and duplicating it here
// would put a second spelling of the executor's own rule in staging.
type UpdateOfferLineItemCommand struct {
	ID         ids.UUID
	LineItemID ids.UUID
}

// NewUpdateOfferLineItemCall binds one line-item edit to the resolver that
// answers for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewUpdateOfferLineItemCall(records datasource.SystemOfRecordProvider, cmd UpdateOfferLineItemCommand) GovernedCall {
	return bind[UpdateOfferLineItemCommand](updateOfferLineItemResolver{
		target: routedRecordTarget{records: records, recordType: offerRecordType},
	}, cmd)
}

type updateOfferLineItemResolver struct {
	target routedRecordTarget
}

// Subject names the OFFER the approval binds to, with the line item being
// edited carried into the summary — the door-agnostic line
// GovernedCall.Subject owes this operation, distinct per line item even
// though no door renders it today (confirmFactResolver's own doc,
// commandsidecar.go, says why).
func (r updateOfferLineItemResolver) Subject(_ context.Context, cmd UpdateOfferLineItemCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: offerRecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Update line item %s on offer %s", cmd.LineItemID, cmd.ID),
	}, nil
}

// Guards stands down: the seam has never served `offer`.
func (r updateOfferLineItemResolver) Guards(ctx context.Context, cmd UpdateOfferLineItemCommand) error {
	return r.target.refuse(ctx, cmd.ID)
}

// RemoveOfferLineItemCommand is one offer line-item removal, whichever
// door asked for it — the same two fields UpdateOfferLineItemCommand
// carries, for the same reason.
type RemoveOfferLineItemCommand struct {
	ID         ids.UUID
	LineItemID ids.UUID
}

// NewRemoveOfferLineItemCall binds one line-item removal to the resolver
// that answers for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewRemoveOfferLineItemCall(records datasource.SystemOfRecordProvider, cmd RemoveOfferLineItemCommand) GovernedCall {
	return bind[RemoveOfferLineItemCommand](removeOfferLineItemResolver{
		target: routedRecordTarget{records: records, recordType: offerRecordType},
	}, cmd)
}

type removeOfferLineItemResolver struct {
	target routedRecordTarget
}

// Subject: the same shape as updateOfferLineItemResolver's.
func (r removeOfferLineItemResolver) Subject(_ context.Context, cmd RemoveOfferLineItemCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: offerRecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Remove line item %s from offer %s", cmd.LineItemID, cmd.ID),
	}, nil
}

// Guards stands down: the seam has never served `offer`.
func (r removeOfferLineItemResolver) Guards(ctx context.Context, cmd RemoveOfferLineItemCommand) error {
	return r.target.refuse(ctx, cmd.ID)
}

// CreateOfferCommand is one offer creation under a parent deal, whichever
// door asked for it. DealID is the ROUTED id — POST /v1/deals/{id}/offers
// names the parent, not an offer, because the offer does not exist yet
// (gradionhq/margince-poc-v1#1046). Fields is the create body, carried the
// same way CreateCommand's own is, so Subject can name which fields it sets.
type CreateOfferCommand struct {
	DealID ids.UUID
	Fields json.RawMessage
}

// NewCreateOfferCall binds one nested offer creation to the resolver that
// answers for it, reading through the record seam the deal itself writes
// through.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewCreateOfferCall(records datasource.SystemOfRecordProvider, cmd CreateOfferCommand) GovernedCall {
	return bind[CreateOfferCommand](createOfferResolver{
		parent: routedRecordTarget{records: records, recordType: string(datasource.EntityDeal)},
	}, cmd)
}

type createOfferResolver struct {
	// parent, not target: what this resolver reads and refuses is the DEAL
	// the offer nests under, never an offer — the offer has no row yet.
	parent routedRecordTarget
}

// Subject names the record TYPE the approval binds to, with NO id — exactly
// the shape createResolver stages for every other create (command.go),
// because an offer does not exist yet either. This is the fix for
// gradionhq/margince-poc-v1#1046: the routed {id} is the deal, and pairing
// target_entity_type=offer with the deal's id (stagedTargetByRoute's guess)
// names a target that resolves to no row, or to an unrelated offer that
// happens to share the id space. Naming no id at all is the only staged
// target this create could honestly carry.
func (r createOfferResolver) Subject(_ context.Context, cmd CreateOfferCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: offerRecordType,
		Summary:    fmt.Sprintf("%s on deal %s", describeGenericWrite("Create", offerRecordType, cmd.Fields), cmd.DealID),
	}, nil
}

// Guards refuses, before anything is staged, the DEAL this offer would
// nest under — unreadable (the row-scope miss) or held in another system
// of record — the same two refusals patchResolver.Guards makes for its own
// target. It does not validate `fields`: offer is not a create_record
// createShapes entry and never will be (the type is created by its own
// module, never through create_record's own write path), so there is
// nothing here for rejectUnknownFields to be asked about — the same
// door-dependent reasoning createResolver.Guards' own comment gives for
// leaving that question to the door that owns the answer.
func (r createOfferResolver) Guards(ctx context.Context, cmd CreateOfferCommand) error {
	return r.parent.refuse(ctx, cmd.DealID)
}

// UpsertPartnerCommand is one partner-extension upsert, whichever door
// asked for it — the routed organization id only. It does not carry the
// partner fields (cert_status, margin_tier, …): neither Guards nor Subject
// reads them, the same reasoning AddListMemberCommand's own doc gives.
//
// The route's own record_type annotation is `partner`, but there is no
// `partner` id in the path and no row for it on the seam — {id} names the
// ORGANIZATION, and the upsert IS a field patch on that organization's
// partner extension, so the approval binds to the organization exactly as
// commandsidecar.go's four resolvers do (organizationSidecarRecordType).
// This is also why upsertPartner is deliberately EXCLUDED from
// agentsplit.go's actionShapedUpdateOps: that set is for operations whose
// body is NOT a field patch on the routed record, and this one's body is
// exactly that, so it must take the full §2.1 human-edit-precedence split
// rather than run unconditionally 🟢.
type UpsertPartnerCommand struct {
	ID ids.UUID
}

// NewUpsertPartnerCall binds one partner upsert to the resolver that
// answers for it, reading through the record seam the organization itself
// writes through.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewUpsertPartnerCall(records datasource.SystemOfRecordProvider, cmd UpsertPartnerCommand) GovernedCall {
	return bind[UpsertPartnerCommand](upsertPartnerResolver{
		target: routedRecordTarget{records: records, recordType: organizationSidecarRecordType},
	}, cmd)
}

type upsertPartnerResolver struct {
	target routedRecordTarget
}

// Subject names the ORGANIZATION the approval binds to (this type's own doc
// says why), not the partner row this route's record_type annotation names.
func (r upsertPartnerResolver) Subject(_ context.Context, cmd UpsertPartnerCommand) (StageInfo, error) {
	return StageInfo{
		TargetType: organizationSidecarRecordType,
		TargetID:   cmd.ID,
		Summary:    fmt.Sprintf("Upsert the partner extension on organization %s", cmd.ID),
	}, nil
}

// Guards refuses the same two ways patchResolver.Guards refuses its own
// target: an organization the caller cannot see, or one whose authority
// lives in another system of record.
func (r upsertPartnerResolver) Guards(ctx context.Context, cmd UpsertPartnerCommand) error {
	return r.target.refuse(ctx, cmd.ID)
}
