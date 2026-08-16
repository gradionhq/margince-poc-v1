// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The two commands that NAME their records instead of anchoring on one
// (gradionhq/margince-poc-v1#928 task 7): an account-started email, which
// files a brand-new conversation under the records it belongs to, and a
// booking, which says who and what the meeting is about. Neither is handed a
// row, so both have to answer the same three questions about the list they
// were given — is it bounded, is it free of repeats, is every record one the
// caller may actually reach — which toollinks.go answers once for both.
//
// Where they part is the staged TARGET. A booking's first link becomes the
// target and supplies the pin, because a meeting is a commitment ON that
// record. An account-started send stages a CREATE — target type with no id
// and no pin — for a reason its own resolver states.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// namedLinks is the link list both resolvers below rest on, admitted and read
// ONCE for the two questions that share it.
//
// One reading, for the reason anchoredRecord (command.go) gives for an
// anchor: Guards refuses on what the read finds and Subject describes what it
// found, and a second reading is a second moment those two answers are free to
// disagree across — a booking would then pin a version taken after the
// authority judgment it is supposed to accompany. It also matters more here
// than for a single row: the read is one row-scoped provider round trip PER
// link, so asking twice doubles a bounded-but-not-small cost.
type namedLinks struct {
	records datasource.SystemOfRecordProvider
	// seen is the list the memo was filled for, so a resolver asked about a
	// second set of links reads THAT set — the same key archiveResolver's and
	// anchoredRecord's memos carry (command.go), compared elementwise because a
	// slice is not comparable and a bare "already read" flag would answer the
	// second question with the first question's records.
	seen   []RecordLink
	unique []RecordLink
	rows   []datasource.Record
	read   bool
}

// stageable de-duplicates the caller's links, bounds them, reads every one and
// refuses the staging if any cannot carry an approval. It answers the unique
// list and the rows in that same order, so a caller needing one of the rows —
// a booking pins its first link's version — reads it here rather than fetching
// it again.
func (n *namedLinks) stageable(ctx context.Context, links []RecordLink) ([]RecordLink, []datasource.Record, error) {
	if n.read && slices.Equal(n.seen, links) {
		return n.unique, n.rows, nil
	}
	unique, err := uniqueRecordLinks(links)
	if err != nil {
		return nil, nil, err
	}
	rows, err := readStageableLinks(ctx, n.records, unique)
	if err != nil {
		return nil, nil, err
	}
	n.seen, n.unique, n.rows, n.read = slices.Clone(links), unique, rows, true
	return unique, rows, nil
}

// SendAccountEmailCommand is one account-started email, whichever door asked
// for it: the reply's operands minus the anchor, plus the records the new
// conversation is filed under.
//
// It carries no body and no consent purpose, for the reason SendEmailCommand's
// own doc gives — nothing here reads either.
type SendAccountEmailCommand struct {
	To      []string
	Cc      []string
	Subject string
	Links   []RecordLink
}

// NewSendAccountEmailCall binds one account-started send to the resolver that
// answers for it, reading its named records through the record seam.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewSendAccountEmailCall(records datasource.SystemOfRecordProvider, cmd SendAccountEmailCommand) GovernedCall {
	return bind[SendAccountEmailCommand](&accountSendResolver{links: namedLinks{records: records}}, cmd)
}

type accountSendResolver struct {
	links namedLinks
}

// Subject stages a CREATE, and the shape says so: target type `activity` with
// no id and no version pin. This send answers no message, so there is no row
// its effect depends on — approvals.settledByShape reads that shape as
// decidable on the object-read floor plus the decision grants.
//
// A LINK CANNOT BE THE TARGET here, the way a booking's first link is. The pin
// is taken SERVER-SIDE from the target pair (approvals.resolveTargetVersion),
// so an organization target pins a version that an enrichment run bumps while
// an overnight proposal waits for someone's morning inbox — cancelling a send
// the record's own content never invalidated. The waiver that declines a pin
// is reserved for kinds whose effect approvals itself applies
// (TestEveryContextTargetKindIsAKindWeStage); this effect is performed by the
// agent's own approved retry.
//
// So the approver is bounded by read+create on `activity` and NOT by the row
// scope of the records the message is filed under, where the reply path binds
// its approver to the anchor. That difference is stated rather than closed:
// closing it takes a target derived from the body on BOTH doors, and this
// command is the half of that which now exists.
func (r *accountSendResolver) Subject(ctx context.Context, cmd SendAccountEmailCommand) (StageInfo, error) {
	links, _, err := r.links.stageable(ctx, cmd.Links)
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType: string(datasource.EntityActivity),
		Summary:    describeAccountSend(cmd, links),
	}, nil
}

// Guards refuses, before anything is staged, a send that reaches nobody, one
// filed under no record, and one naming a record the caller cannot see or
// whose authority lives elsewhere.
//
// The links are read even though none of them is the staged target, because
// that is a question about the STAGER's reach rather than the approver's: the
// store refuses a link the caller cannot see, at execution — so without the
// same probe here, an agent naming a company it cannot read mints an approval
// a human reads, approves, and watches fail with the one-shot authority
// already spent.
//
// What this does not pre-empt, so neither reads as covered: the consent gate's
// per-purpose verdict, the workspace's mailbox send capability, and whether an
// address belongs to a person on file. All are refusals a human's yes cannot
// fix, and all need reads staging does not have.
func (r *accountSendResolver) Guards(ctx context.Context, cmd SendAccountEmailCommand) error {
	if err := requireAddressee(cmd.To); err != nil {
		return err
	}
	if err := requireAccountSendLinks(cmd.Links); err != nil {
		return err
	}
	_, _, err := r.links.stageable(ctx, cmd.Links)
	return err
}

// requireAccountSendLinks enforces what the schema and crm.yaml both state: a
// new conversation says which records it belongs to.
//
// A function rather than a line inside Guards because this verb has two doors
// past this rule — the staging path and the post-approval execute — and the
// MCP surface does not validate arguments against an InputSchema (that schema
// is documentation), so a rule enforced at one door is a rule a caller meets
// only sometimes.
func requireAccountSendLinks(links []RecordLink) error {
	if len(links) > 0 {
		return nil
	}
	return &BadArgsError{
		Cause: errors.New("`links` needs at least one entry: a message filed under no record " +
			"is one nobody finds again, and the store refuses it"),
		Guidance: "name the company, person or deal this conversation is about",
	}
}

// describeAccountSend is the one line the inbox shows: who it reaches, cc
// included, what it says it is about, and how many records it will land on.
//
// Every addressee, for the reason describeSend states — an unnamed recipient
// is a recipient nobody agreed to. The links are COUNTED rather than listed:
// their ids mean nothing to a human reading one line, and the staged row is
// decidable on the activity floor rather than on those records, so naming them
// would disclose more than the decision rests on.
func describeAccountSend(cmd SendAccountEmailCommand, links []RecordLink) string {
	summary := fmt.Sprintf("Start an email conversation with %s", strings.Join(cmd.To, ", "))
	if len(cmd.Cc) > 0 {
		summary += fmt.Sprintf(", cc %s", strings.Join(cmd.Cc, ", "))
	}
	return summary + fmt.Sprintf(", subject %q, filed under %d record(s)", cmd.Subject, len(links))
}

// BookMeetingCommand is one booking, whichever door asked for it. It carries
// every operand the two questions below read: the slot, whose calendar it
// lands on, what it is called, and what it attaches to.
type BookMeetingCommand struct {
	HostUserID *ids.UUID
	Start      time.Time
	End        time.Time
	Subject    string
	Links      []RecordLink
}

// NewBookMeetingCall binds one booking to the resolver that answers for it,
// reading its named records through the record seam.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewBookMeetingCall(records datasource.SystemOfRecordProvider, cmd BookMeetingCommand) GovernedCall {
	return bind[BookMeetingCommand](&bookMeetingResolver{links: namedLinks{records: records}}, cmd)
}

type bookMeetingResolver struct {
	links namedLinks
}

// Subject names the booking's FIRST link as the target and pins its version,
// which is what makes a booking's staged row bind to a record rather than
// float free: a meeting is a commitment ON that record, and the human deciding
// it is the one whose scope reaches it.
//
// The empty case is asked again rather than assumed away. Guards refuses it
// and StageSubject runs Guards first, but a Subject that indexes links[0] on
// the strength of a caller's ordering is one panic away from being wrong — and
// requireBookingLinks is ONE function asked at both points, not a second
// spelling of the rule that could come to say something else.
func (r *bookMeetingResolver) Subject(ctx context.Context, cmd BookMeetingCommand) (StageInfo, error) {
	if err := requireBookingLinks(cmd.Links); err != nil {
		return StageInfo{}, err
	}
	links, rows, err := r.links.stageable(ctx, cmd.Links)
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType:    links[0].EntityType,
		TargetID:      links[0].EntityID,
		TargetVersion: &rows[0].Version,
		Summary:       describeBooking(cmd, links),
	}, nil
}

// Guards refuses, before anything is staged, a booking whose end does not
// follow its start, one attached to nothing, one attached to more records than
// a request may name, one naming a record the caller cannot see or whose
// authority lives elsewhere, and one onto another host's calendar by anyone
// but an admin.
func (r *bookMeetingResolver) Guards(ctx context.Context, cmd BookMeetingCommand) error {
	if err := requireBookingWindow(cmd.Start, cmd.End); err != nil {
		return err
	}
	if err := requireOwnCalendarOrAdmin(ctx, cmd.HostUserID); err != nil {
		return err
	}
	if err := requireBookingLinks(cmd.Links); err != nil {
		return err
	}
	_, _, err := r.links.stageable(ctx, cmd.Links)
	return err
}

// requireOwnCalendarOrAdmin mirrors the store's rule for booking on behalf of
// another host (activities.BookMeeting): the admin ROLE, not an unbounded row
// scope. Asked here too because the store's refusal comes after the human's
// one-shot approval is consumed, and a management or ops passport would
// otherwise stage a booking that can only ever be refused.
func requireOwnCalendarOrAdmin(ctx context.Context, host *ids.UUID) error {
	if host == nil {
		return nil
	}
	actor, ok := principal.Actor(ctx)
	if !ok {
		return apperrors.ErrPermissionDenied
	}
	if *host == actor.UserID {
		return nil
	}
	return auth.RequireAdmin(ctx)
}

// requireBookingWindow refuses a booking that ends before it starts.
//
// The store refuses it too (errBookingEndNotAfterStart), which is why this is
// not a correctness hole — but reaching THAT refusal costs the human's
// approval on the way past, since redemption is consumed before the handler
// runs. So both doors ask first, the same rule the link checks follow.
func requireBookingWindow(start, end time.Time) error {
	if end.After(start) {
		return nil
	}
	return &BadArgsError{Cause: fmt.Errorf(
		"`end` (%s) does not follow `start` (%s); a booking with no duration would be refused after approval",
		end.Format(time.RFC3339), start.Format(time.RFC3339))}
}

// requireBookingLinks refuses a booking attached to nothing, because the
// APPROVAL cannot exist without one: Subject binds to the first link and pins
// its version, so a link-less booking could only stage against a zero id — an
// authority object the approvals surface can scope to nobody in particular,
// which is the defect this seam fixes rather than one it may reintroduce.
//
// It does NOT restate the contract, and the difference is worth being exact
// about. `/bookings` declares `links` a required KEY with `maxItems: 25` and
// no `minItems`, so `"links": []` is contract-legal, and neither the handler
// nor activities.Store.BookMeeting refuses one. This rule is the gate's own,
// and an agent doing what the schema permits now meets it where it previously
// staged and executed. That disagreement is filed rather than settled here,
// because closing it the right way is a contract change:
// gradionhq/margince-poc-v1#1065.
//
// requireAccountSendLinks' identical-looking claim IS backed —
// SendAccountEmailRequest.links carries minItems: 1 — which is why that one
// cites the contract and this one cannot.
//
// A function for the reason requireAccountSendLinks is one: this verb has two
// doors past the rule, and the MCP surface does not validate arguments against
// an InputSchema.
func requireBookingLinks(links []RecordLink) error {
	if len(links) > 0 {
		return nil
	}
	return &BadArgsError{Cause: errors.New(
		"`links` needs at least one entry: a booking names who and what it is about, " +
			"and one attached to nothing cannot be approved against a record")}
}

// describeBooking is the one line the inbox shows.
//
// It names every operand that changes what gets released — the slot, the
// subject, WHOSE calendar it lands on, and how many records it attaches to. A
// human approving from the inbox row sees only this string, so an operand
// missing from it is an effect nobody agreed to.
//
// The subject is the agent's own text, so it is quoted rather than run into
// the sentence; the approvals engine sanitizes every summary at the single
// staging path regardless.
func describeBooking(cmd BookMeetingCommand, links []RecordLink) string {
	subject := cmd.Subject
	if strings.TrimSpace(subject) == "" {
		subject = "(no subject)"
	}
	summary := fmt.Sprintf("Book %q from %s to %s",
		subject, cmd.Start.Format(time.RFC3339), cmd.End.Format(time.RFC3339))
	if cmd.HostUserID != nil {
		summary += fmt.Sprintf(" on %s's calendar", cmd.HostUserID)
	}
	if len(links) > 0 {
		summary += fmt.Sprintf(", attached to %d record(s)", len(links))
	}
	return summary
}
