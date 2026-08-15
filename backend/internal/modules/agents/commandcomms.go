// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The two anchored sends: a mail reply and a channel reply
// (gradionhq/margince-poc-v1#928 task 7). Both answer a conversation that
// already exists, so both name an ANCHOR activity — the row the effect hangs
// off, whose version the approval pins and whose authority decides whether an
// approval could ever be released at all.
//
// These are the first commands BOTH doors reach for the same operation: the
// tool decodes send_email/send_message arguments into them, and the REST door
// decodes POST /v1/activities/{id}/send-email and .../send-message into the
// same commands. Every refusal below therefore stops being a rule the tool door
// makes and the REST door skips, which is what #928 is about.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// ChannelKinds answers whether an activity kind is a messaging-channel
// conversation a reply may transmit through.
//
// It is the Comms seam's own question narrowed to the two methods this
// resolver needs (Comms embeds them below), because the REST door reaches the
// resolver without the send machinery around it: that door has no reason to
// hold a seam that can send mail, book meetings and read calendars in order to
// ask whether an anchor is a channel. Both answers come from the activities
// package either way, so the two DOORS cannot come to disagree.
//
// They are a PRE-STAGING guard, not a restatement of the store's own refusal.
// The store refuses a send it cannot make by asking whether the workspace has a
// bot bound (channelsend.go's canSend); this asks whether the installation
// composed the transport at all, which is knowable earlier and is the question
// worth answering before an approval is spent.
//
// It asks TWO questions since ADR-0107/A158, because one no longer implies the
// other: whether the anchor is a channel conversation at all, and whether this
// installation composed a transport that can carry a reply on the provider it
// names. Before the split, a kind that was a channel WAS a provider this
// installation had, so a single test answered both.
type ChannelKinds interface {
	IsChannelKind(kind string) bool
	CanSendOnProvider(provider string) bool
}

// SendEmailCommand is one mail reply, whichever door asked for it.
//
// It carries the addressees and the subject because both questions below read
// them: Guards refuses a send with no addressee, and Subject names every
// recipient in the line a human approves. It carries neither the BODY nor the
// consent purpose — nothing here reads either (the body travels inside the
// staged arguments, covered by the diff_hash, and the consent verdict is the
// gate's own read at execution) — the same call UpdateFactCommand's own doc
// (commandsidecar.go) makes for dropping a value with no reader.
type SendEmailCommand struct {
	ActivityID ids.UUID
	To         []string
	Cc         []string
	Subject    string
}

// NewSendEmailCall binds one mail reply to the resolver that answers for it,
// reading the anchor through the record seam.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewSendEmailCall(records datasource.SystemOfRecordProvider, cmd SendEmailCommand) GovernedCall {
	return bind[SendEmailCommand](&sendEmailResolver{
		anchor: anchoredRecord{records: records, entityType: datasource.EntityActivity},
	}, cmd)
}

type sendEmailResolver struct {
	anchor anchoredRecord
}

// Subject names the ANCHOR the approval binds to: the thread being replied to
// is the row the effect hangs off, so it is what the redemption version pin
// re-checks.
//
// The summary names every addressee, cc included. A human approving from the
// inbox row reads only this line, so an unnamed recipient is a recipient
// nobody agreed to — and the diff_hash binds cc faithfully either way, which
// is precisely what makes omitting it from the display a problem rather than a
// harmless abbreviation.
func (r *sendEmailResolver) Subject(ctx context.Context, cmd SendEmailCommand) (StageInfo, error) {
	rec, err := r.anchor.row(ctx, cmd.ActivityID)
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType:    string(datasource.EntityActivity),
		TargetID:      cmd.ActivityID,
		TargetVersion: &rec.Version,
		Summary:       describeSend(cmd),
	}, nil
}

// Guards refuses, before anything is staged, a send that reaches nobody, and an
// anchor the caller cannot see or whose authority lives in another system of
// record. Reaching any of those refusals from the executor instead would cost
// the human's one-shot approval on the way past: staging mints an approval, a
// human reads a send with no addressee and says yes, the approved retry
// consumes that authority, and only THEN does the store refuse.
//
// The addressee check runs before the read for the same reason the read is
// worth avoiding: a call this malformed names no correspondent, and answering
// it costs a round trip through the record seam to say so.
//
// What this does not pre-empt, so neither reads as covered: the consent gate's
// per-purpose verdict, the workspace's mailbox send capability, and whether an
// address is syntactically deliverable. All are refusals a human's yes cannot
// fix, and the first two need reads this call does not have — staging fetches
// the anchor for the version pin and nothing else.
func (r *sendEmailResolver) Guards(ctx context.Context, cmd SendEmailCommand) error {
	if err := requireAddressee(cmd.To); err != nil {
		return err
	}
	return r.anchor.refuse(ctx, cmd.ActivityID)
}

// describeSend is the one line the inbox shows for a mail send: who it
// reaches, cc included, and what it says it is about.
func describeSend(cmd SendEmailCommand) string {
	summary := fmt.Sprintf("Send an email to %s", strings.Join(cmd.To, ", "))
	if len(cmd.Cc) > 0 {
		summary += fmt.Sprintf(", cc %s", strings.Join(cmd.Cc, ", "))
	}
	return summary + fmt.Sprintf(", subject %q", cmd.Subject)
}

// SendMessageCommand is one channel reply, whichever door asked for it.
//
// It carries the message BODY where its mail twin does not, and that asymmetry
// is the transport's rather than an omission: a channel reply names no
// addressee — the recipient is the person the anchor conversation is with,
// resolved server-side — so the text IS the whole of what a human is asked to
// release, and Guards refuses an empty one.
type SendMessageCommand struct {
	ActivityID ids.UUID
	Body       string
}

// NewSendMessageCall binds one channel reply to the resolver that answers for
// it. channels is the kind test the anchor is judged by — see
// sendMessageResolver.Guards for what it refuses and why the seam carries it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewSendMessageCall(records datasource.SystemOfRecordProvider, channels ChannelKinds, cmd SendMessageCommand) GovernedCall {
	return bind[SendMessageCommand](&sendMessageResolver{
		anchor:   anchoredRecord{records: records, entityType: datasource.EntityActivity},
		channels: channels,
	}, cmd)
}

type sendMessageResolver struct {
	anchor   anchoredRecord
	channels ChannelKinds
}

// Subject names the ANCHOR the approval binds to, exactly as the mail twin's
// does.
//
// The inbox shows the human what they are releasing. The message text is the
// thing being approved, so it belongs in the summary; the recipient does not,
// because nobody named one — the conversation did.
func (r *sendMessageResolver) Subject(ctx context.Context, cmd SendMessageCommand) (StageInfo, error) {
	rec, err := r.anchor.row(ctx, cmd.ActivityID)
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType:    string(datasource.EntityActivity),
		TargetID:      cmd.ActivityID,
		TargetVersion: &rec.Version,
		Summary:       fmt.Sprintf("Reply on a captured conversation: %q", cmd.Body),
	}, nil
}

// Guards refuses, before anything is staged, an anchor the caller cannot see or
// whose authority lives elsewhere, a text-less message (a channel provider
// rejects one), and an anchor that is not a messaging-channel conversation at
// all. The store refuses the last two too — errEmptyMessageBody and
// NotAChannelConversationError — and reaching them from there costs the human's
// one-shot approval on the way past.
//
// SendMessage has two more permanent refusals this does not guard:
// ChannelRecipientError (the conversation reaches nobody, or more than one
// person) and ChannelNotSendCapableError (the workspace has no bot bound for
// the provider). Both are the same "yes with no path to actually happening"
// shape as the two guarded here, but closing them needs a reachability read
// this call does not have: the record read below returns the anchor's fields,
// not who the conversation resolves to or whether a bot is bound, and answering
// either question would mean a new datasource seam method plus a database read
// at staging time.
func (r *sendMessageResolver) Guards(ctx context.Context, cmd SendMessageCommand) error {
	rec, err := r.anchor.row(ctx, cmd.ActivityID)
	if err != nil {
		return err
	}
	if err := refuseStagingElsewhere(rec); err != nil {
		return err
	}
	if strings.TrimSpace(cmd.Body) == "" {
		return &BadArgsError{Cause: fmt.Errorf("body is empty or whitespace-only; a channel provider rejects a text-less message")}
	}
	var anchor struct {
		Kind            string `json:"kind"`
		ChannelProvider string `json:"channel_provider"`
	}
	if err := json.Unmarshal(rec.Fields, &anchor); err != nil {
		return fmt.Errorf("crmagents: activity %s read back with unreadable fields: %w", cmd.ActivityID, err)
	}
	if !r.channels.IsChannelKind(anchor.Kind) {
		return &BadArgsError{
			Cause: fmt.Errorf("activity %s is a %q activity, not a messaging-channel conversation",
				cmd.ActivityID, anchor.Kind),
			Guidance: "reply on the channel the conversation was held on",
		}
	}
	// Refused HERE rather than at execution, and the timing is the point: this
	// runs before staging, and staging costs the human the one-shot approval
	// they only get to spend once. A message on a transport this installation
	// never composed would otherwise be approved and then fail — a yes with no
	// path to happening, which is exactly what these guards exist to prevent.
	if !r.channels.CanSendOnProvider(anchor.ChannelProvider) {
		return &BadArgsError{
			Cause: fmt.Errorf("activity %s was carried by %q, which this installation has no connector for",
				cmd.ActivityID, anchor.ChannelProvider),
			Guidance: "no reply can be sent on this transport; answer the person another way",
		}
	}
	return nil
}
