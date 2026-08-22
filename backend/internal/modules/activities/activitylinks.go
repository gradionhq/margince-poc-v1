// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The timeline's polymorphic link rows: what an activity is filed under, and
// the rules every writer of one meets — the vocabulary it may name, the bound
// on how many, and the row-scope check on each. Split from activity.go because
// a link is its own concept: a send, a booking, a logged note and a captured
// message all reach this and share little else.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ActivityLinkInput ties one activity to one record. Which record types are
// admitted is linkTargets' answer, not a list restated here.
type ActivityLinkInput struct {
	EntityType string // one of linkTargets (activity_link_entity_type_check)
	// note: the link target is polymorphic (activity_link is the canonical
	// (entity_type, entity_id) seam), so the id stays untyped (rule 6).
	EntityID ids.UUID
}

// fieldLinks is the request field every link refusal attributes itself to, so
// a caller reading a 422 is told which array to change. One spelling, because
// three different refusals name it and a fourth that spelled it differently
// would send the caller looking for a field the request does not have.
const fieldLinks = "links"

// maxActivityLinks bounds how many records one activity may be filed under.
//
// It is a REQUEST BOUND rather than a modelling opinion: every entry costs its
// own row-scoped probe and its own insert, and the array is chosen freely by
// the caller. Both request schemas that carry a link list declare the same 25,
// and the agent surface refuses the count earlier so no approval is minted for
// a call the store would reject — this is the bound that holds for every
// transport, a human's own send included. insertActivityLinks applies it at
// the write itself, which is where a booking meets it.
const maxActivityLinks = 25

// TooManyLinksError refuses an activity filed under more records than the
// timeline will carry for one entry.
type TooManyLinksError struct{ Count int }

func (e *TooManyLinksError) Error() string {
	return fmt.Sprintf("an activity may be filed under at most %d records; this one names %d",
		maxActivityLinks, e.Count)
}

// FieldFault names the field the caller can shorten, so the refusal is a 422
// against `links` rather than an unattributed rejection.
func (e *TooManyLinksError) FieldFault() (field, code, message string) {
	return fieldLinks, "too_many_links", e.Error()
}

// insertActivityLinks writes the polymorphic link rows. The last_activity_at
// clocks on deal, person and organization move with them, but not from here:
// migration 1787032690 keeps them on the activity_link row itself (a trigger
// recomputing from the timeline), because this is one of several writers of
// that row — capture, ensure, relink and message identity insert links too —
// and the reach set also moves with no link written at all (an employment
// starts or ends, a deal moves account, an activity is archived or re-dated).
//
// The FK alone is not enough: it is
// checked as the table owner, bypassing RLS, so it would accept a
// guessed cross-tenant or out-of-scope UUID as a link target — every
// target passes the row-scope link check first.
//
// A repeated (type, id) is written ONCE. uq_activity_link already says a link
// is one row per record, so a caller that named the same company twice was
// answered with a unique violation — a 500 at the end of a send or a booking,
// and for an agent's approved retry a 500 that consumed the human's one-shot
// approval on a message that then never left. Deduplicating here rather than in
// each caller is what makes that true of every transport at once.
//
// The count is BOUNDED here for the same reason: the list is the caller's to
// choose, every entry costs its own row-scope probe and its own insert, and
// this is the one statement every writer passes through — the timeline's link
// vocabulary describes what a message or meeting is ABOUT, and a record set
// larger than this is about nothing.
//
// A PROJECT link additionally requires activity.UPDATE, which the create grant
// alone does not confer. Filing under a project classifies the correspondence
// as commercial (D5), and that classification is write-once in the database and
// is not lifted by unfiling — so it is a heavier act than creating a row, and
// the capture ladder already refuses it on exactly these terms
// (capture/sinkproject.go: "a principal that may create captured mail but not
// change it attributes nothing"). Two doors onto one act must not disagree
// about who may perform it; before this the create door was the cheaper way in.
func insertActivityLinks(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, links []ActivityLinkInput) error {
	if len(links) > maxActivityLinks {
		return &TooManyLinksError{Count: len(links)}
	}
	seen := make(map[ActivityLinkInput]struct{}, len(links))
	for _, link := range links {
		if _, duplicate := seen[link]; duplicate {
			continue
		}
		seen[link] = struct{}{}
		column := linkColumn(link.EntityType)
		if column == "" {
			return &InvalidLinkTypeError{EntityType: link.EntityType}
		}
		// The project grant is checked BEFORE the insert it gates, not after.
		// A refusal returned later is undone only because every caller runs
		// this inside a transaction it aborts — correct today, and the wrong
		// thing to hang an authorization check on. The sibling writer asks
		// first (capture/sinkproject.go), and two doors onto one act must not
		// disagree about WHEN they ask either.
		if link.EntityType == linkEntityProject {
			if err := auth.Require(ctx, "activity", principal.ActionUpdate); err != nil {
				return err
			}
		}
		if err := auth.EnsureLinkTarget(ctx, tx, link.EntityType, link.EntityID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			sprintf(`INSERT INTO activity_link (activity_id, entity_type, %s) VALUES ($1, $2, $3)`, column),
			activityID, link.EntityType, link.EntityID); err != nil {
			return err
		}
		if link.EntityType == linkEntityProject {
			if err := StampCorrespondenceForProject(ctx, tx, activityID, link.EntityID); err != nil {
				return err
			}
		}
	}
	return nil
}

// InvalidLinkTypeError maps to 422.
type InvalidLinkTypeError struct{ EntityType string }

func (e *InvalidLinkTypeError) Error() string {
	return "activity link entity_type " + e.EntityType + " is not " + linkVocabulary()
}

// FieldFault refuses a link to an entity type the timeline does not carry.
func (e *InvalidLinkTypeError) FieldFault() (field, code, message string) {
	return fieldLinks, "invalid_entity_type", e.Error()
}
