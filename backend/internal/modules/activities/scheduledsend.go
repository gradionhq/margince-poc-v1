// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// scheduleCeiling bounds how far ahead a message may be scheduled. A year-out
// send is not a plan, it is a row nobody will remember writing, whose consent
// and recipients will have moved on long before it fires.
const scheduleCeiling = 90 * 24 * time.Hour

// payloadVersionCurrent is the frozen-payload schema this build writes. Rows
// outlive the code that wrote them, so a reader checks this rather than
// assuming the struct it compiled against is the one on disk.
const payloadVersionCurrent = 1

// The scheduling fields, named once. They are a wire contract, an audit key and
// a refusal's field name at the same time, so a typo in any one spelling would
// answer the caller about a field they did not send.
// Exported because compose names the same two fields — the agent door's
// refusals and the replay discriminator — and a second spelling of a wire field
// is a second thing to get wrong.
const (
	FieldScheduledAt = "scheduled_at"
	FieldScheduledTZ = "scheduled_tz"
)

// ScheduleTimer wakes a scheduled send when it comes due. It is the seam
// between the decision to defer, which this module owns, and the job runner,
// which it must not reach into directly.
//
// ScheduleTx runs in the caller's transaction for the same reason DeliveryStager
// does: the row and its timer are one fact. A timer without a row fires at
// nothing; a row without a timer waits forever.
type ScheduleTimer interface {
	ScheduleTx(ctx context.Context, tx pgx.Tx, id ids.UUID, due time.Time) error
}

// HeldNotifier puts a stopped message in front of the human who scheduled it.
//
// A message the system refused to send is a decision waiting for a rep, and a
// decision they have to go looking for is one they make late or not at all. This
// is the seam to the approval inbox — the surface this product already uses for
// "something needs you" (ADR-0104 §5, DRAFT-AC-N-11a).
//
// Both halves run in the caller's transaction, for the reason DeliveryStager
// does: a hold and the item a rep acts on are one fact, and so are a rep's
// answer and the item disappearing.
//
// Nil is a surface with no inbox wired. A hold still happens — refusing to stop
// a message because nobody can be told would send mail a gate refused, which is
// far worse than a quiet stop.
type HeldNotifier interface {
	NotifyHeldInTx(ctx context.Context, tx pgx.Tx, in HeldNotice) error
	// ResolveHeldInTx clears the item once the rep has answered it. Rescheduling
	// or cancelling IS the answer, and an item outliving it asks the same
	// question twice.
	ResolveHeldInTx(ctx context.Context, tx pgx.Tx, scheduledSendID ids.UUID) error
}

// HeldNotice is one stopped message as the rep needs to see it: whose it is,
// what it said, and which gate refused.
type HeldNotice struct {
	ScheduledSendID ids.UUID
	ScheduledBy     ids.UUID
	Reason          string
	Subject         string
	ScheduledAt     time.Time
}

// SendSchedule is a caller's request to defer a send. At is absolute; TZ is the
// IANA zone the human picked it in, kept so the choice can be re-rendered and
// audited (ADR-0104 §7).
type SendSchedule struct {
	At time.Time
	TZ string
}

// scheduledPayload is the frozen message, versioned because these rows outlive
// the code that wrote them. Explicit JSON tags, never the internal struct's
// field names: a rename in a later refactor must not change what a pending
// message says.
//
// Attachments are IDS, not bytes. The fire path re-resolves them so a document
// archived or superseded between scheduling and sending is caught by the same
// gate an immediate send passes through.
type scheduledPayload struct {
	Recipients     []string `json:"recipients"`
	Cc             []string `json:"cc,omitempty"`
	Bcc            []string `json:"bcc,omitempty"`
	Subject        string   `json:"subject"`
	Body           string   `json:"body"`
	HTMLBody       string   `json:"html_body,omitempty"`
	AttachmentIDs  []string `json:"attachment_ids,omitempty"`
	ConsentPurpose string   `json:"consent_purpose"`
	DraftRef       string   `json:"draft_ref,omitempty"`
}

func freezePayload(in SendEmailInput) scheduledPayload {
	files := make([]string, 0, len(in.AttachmentIDs))
	for _, id := range in.AttachmentIDs {
		files = append(files, id.String())
	}
	return scheduledPayload{
		Recipients:     in.Recipients,
		Cc:             in.Cc,
		Bcc:            in.Bcc,
		Subject:        in.Subject,
		Body:           in.Body,
		HTMLBody:       in.HTMLBody,
		AttachmentIDs:  files,
		ConsentPurpose: in.ConsentPurpose,
		DraftRef:       in.DraftRef,
	}
}

func (p scheduledPayload) thaw() (SendEmailInput, error) {
	files := make([]ids.UUID, 0, len(p.AttachmentIDs))
	for _, raw := range p.AttachmentIDs {
		id, err := ids.Parse(raw)
		if err != nil {
			return SendEmailInput{}, fmt.Errorf("scheduled send: attachment id %q: %w", raw, err)
		}
		files = append(files, id)
	}
	return SendEmailInput{
		Recipients:     p.Recipients,
		Cc:             p.Cc,
		Bcc:            p.Bcc,
		Subject:        p.Subject,
		Body:           p.Body,
		HTMLBody:       p.HTMLBody,
		AttachmentIDs:  files,
		ConsentPurpose: p.ConsentPurpose,
		DraftRef:       p.DraftRef,
	}, nil
}

// ScheduledSend is one message waiting for its moment.
type ScheduledSend struct {
	ID          ids.UUID
	Status      string
	ScheduledAt time.Time
	ScheduledTZ string
	OriginKind  string
	Anchor      ids.ActivityID
	Subject     string
	Recipients  []string
	Cc          []string
	Bcc         []string
	Body        string
	ScheduledBy ids.UUID
	ActivityID  ids.UUID
	HeldReason  string
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Scheduled-send states.
//
// 'released' is deliberately not 'sent' AT THAT MOMENT: the fire transaction has
// handed the message to the delivery machinery and the provider has not been
// called, so the delivery can still park or fail. It is a step, not an ending —
// when the provider confirms receipt the row reads 'sent' like any other message
// this system sent (ADR-0104 §5).
//
// 'sent' is never WRITTEN here. It is derived at read from the delivery's own
// status (scheduledSendColumns), because comms owns the receipt and a second
// writer would be a second place for the two to disagree about one message.
const (
	ScheduledStatusScheduled = "scheduled"
	ScheduledStatusReleased  = "released"
	ScheduledStatusSent      = "sent"
	ScheduledStatusCancelled = "cancelled"
	ScheduledStatusHeld      = "held"
)

// Hold reasons. Each names what a human has to decide about, because "held" on
// its own tells a rep nothing they can act on.
const (
	HeldConsentWithdrawn = "consent_withdrawn"
	HeldSenderInactive   = "sender_inactive"
	// HeldPassportRevoked is its own reason rather than sender_inactive: the
	// human is fine, and telling them their account is inactive would send them
	// to the wrong place. What stopped the message is the agent credential it
	// was scheduled under.
	HeldPassportRevoked = "passport_revoked"
	HeldMissedWindow    = "missed_window"
	HeldTimerExhausted  = "timer_exhausted"
	HeldSendRefused     = "send_refused"
)

// InvalidScheduleError refuses a due moment the server will not accept. It maps
// to 422 on every surface: a rep who picked a bad moment can pick another one.
type InvalidScheduleError struct {
	Field  string
	Reason string
}

func (e *InvalidScheduleError) Error() string {
	return fmt.Sprintf("%s %s", e.Field, e.Reason)
}

// FieldFault names the field the caller has to correct.
func (e *InvalidScheduleError) FieldFault() (field, code, message string) {
	return e.Field, "invalid_schedule", e.Error()
}

// SendOutcome is what a send returned: an activity when it went now, a
// scheduled record when it will go later. Exactly one is populated.
type SendOutcome struct {
	Activity  *crmcontracts.Activity
	Scheduled *ScheduledSend
}

// SendOrSchedule is the ONE branch between sending now and sending later.
//
// Every door — the reply handler, the account-started handler, and both MCP
// tools — calls this rather than choosing for itself, so "send later" cannot
// exist on one transport and not another, and neither can a gate that only the
// immediate path runs.
//
// A nil schedule, or one already due, sends immediately through the unchanged
// path. Anything else prepares the message exactly as an immediate send would —
// so a bad recipient, a withheld consent or an unreadable attachment refuses at
// the keyboard, where the rep can still fix it — and then freezes it.
func (s *Store) SendOrSchedule(
	ctx context.Context,
	origin SendOrigin,
	in SendEmailInput,
	sched *SendSchedule,
	gate ConsentGate,
	stager DeliveryStager,
	timer ScheduleTimer,
) (SendOutcome, error) {
	if sched == nil || !sched.At.After(s.now()) {
		sent, err := s.SendEmail(ctx, origin, in, gate, stager)
		if err != nil {
			return SendOutcome{}, err
		}
		return SendOutcome{Activity: &sent}, nil
	}
	scheduled, err := s.scheduleSend(ctx, origin, in, *sched, gate, stager, timer)
	if err != nil {
		return SendOutcome{}, err
	}
	return SendOutcome{Scheduled: &scheduled}, nil
}

// scheduleSend freezes a validated message for later.
//
// It runs the SAME preparation an immediate send runs, and throws the rendered
// result away. That looks wasteful and is the point: the rep learns now that a
// recipient is unreachable or a purpose unconsented, rather than discovering it
// tomorrow from a held message. What the row stores is the human's input, not
// the rendering — the sign-off, the footer and the attachment snapshots are all
// re-derived at fire, against the state that exists then.
func (s *Store) scheduleSend(
	ctx context.Context,
	origin SendOrigin,
	in SendEmailInput,
	sched SendSchedule,
	gate ConsentGate,
	stager DeliveryStager,
	timer ScheduleTimer,
) (ScheduledSend, error) {
	if timer == nil {
		return ScheduledSend{}, errNoScheduleTimer
	}
	if err := validateSchedule(sched, s.now()); err != nil {
		return ScheduledSend{}, err
	}
	if _, err := s.PrepareSend(ctx, origin, in, gate, stager); err != nil {
		return ScheduledSend{}, err
	}

	actor, err := storekit.Actor(ctx)
	if err != nil {
		return ScheduledSend{}, err
	}
	if actor.UserID == (ids.UUID{}) {
		return ScheduledSend{}, errNoSchedulingUser
	}

	payload, err := json.Marshal(freezePayload(in))
	if err != nil {
		return ScheduledSend{}, fmt.Errorf("scheduled send: freezing the message: %w", err)
	}
	originLinks, err := marshalOriginLinks(origin)
	if err != nil {
		return ScheduledSend{}, err
	}

	row := ScheduledSend{
		ID:          ids.NewV7(),
		Status:      ScheduledStatusScheduled,
		ScheduledAt: sched.At.UTC(),
		ScheduledTZ: sched.TZ,
		OriginKind:  originKind(origin),
		Anchor:      origin.anchor,
		Subject:     in.Subject,
		Recipients:  in.Recipients,
		Cc:          in.Cc,
		Bcc:         in.Bcc,
		Body:        in.Body,
		ScheduledBy: actor.UserID,
		Version:     1,
	}

	err = s.tx(ctx, func(tx pgx.Tx) error {
		// workspace_id comes from the transaction's own GUC, the one binding
		// every tenant write here uses — never from a caller-supplied value.
		prov := provenanceOf(actor)
		if _, err := tx.Exec(ctx, `
			INSERT INTO scheduled_send
			  (id, workspace_id, status, scheduled_at, scheduled_tz,
			   origin_kind, anchor_activity_id, origin_links,
			   payload, payload_version, scheduled_by, principal_kind,
			   agent_actor_id, agent_passport_id, agent_on_behalf_of)
			VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			row.ID, row.Status, row.ScheduledAt, row.ScheduledTZ,
			row.OriginKind, nullableAnchor(origin), originLinks,
			payload, payloadVersionCurrent, row.ScheduledBy, principalKind(actor),
			prov.ActorID, prov.PassportID, prov.OnBehalfOf,
		); err != nil {
			return fmt.Errorf("scheduled send: recording the intention: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "schedule", "scheduled_send", row.ID, nil, map[string]any{
			FieldScheduledAt: row.ScheduledAt,
			FieldScheduledTZ: row.ScheduledTZ,
			"subject":        row.Subject,
		}); err != nil {
			return err
		}
		return timer.ScheduleTx(ctx, tx, row.ID, row.ScheduledAt)
	})
	if err != nil {
		return ScheduledSend{}, err
	}
	return row, nil
}

// validateSchedule refuses a due moment the server will not honour.
func validateSchedule(sched SendSchedule, now time.Time) error {
	if sched.TZ == "" {
		return &InvalidScheduleError{Field: FieldScheduledTZ, Reason: "is required when scheduling a send"}
	}
	// A zone NAME, resolved against the IANA database — never a numeric offset,
	// which would be frozen against the DST rules of the day it was written
	// (AC-DS-TZ4).
	if _, err := time.LoadLocation(sched.TZ); err != nil {
		return &InvalidScheduleError{Field: FieldScheduledTZ, Reason: "is not an IANA time zone name"}
	}
	if sched.At.Sub(now) > scheduleCeiling {
		return &InvalidScheduleError{
			Field:  FieldScheduledAt,
			Reason: fmt.Sprintf("is further ahead than the %d-day scheduling limit", int(scheduleCeiling.Hours()/24)),
		}
	}
	return nil
}

var (
	// errNoScheduleTimer refuses a deferred send on a surface wired without a
	// timer. Like errNoDeliveryStager this is a composition defect rather than
	// a client-correctable condition, so it carries no sentinel: it must
	// surface as the 500 it is rather than borrow a refusal that would tell
	// the caller something untrue about their request.
	errNoScheduleTimer = errors.New("activities: send path has no scheduling machinery wired")
	// errNoSchedulingUser refuses to defer a send nobody can be re-derived from.
	// Fire rebuilds its authority from this id (ADR-0104 §4); a row without one
	// could only fire under an authority it invented.
	errNoSchedulingUser = errors.New("activities: a scheduled send needs a user to fire under")
)

func originKind(o SendOrigin) string {
	if o.isReply() {
		return "reply"
	}
	return "account"
}

// nullableAnchor is the anchor a reply continues, or nil for an account-started
// message — a typed pointer, which pgx writes as SQL NULL.
func nullableAnchor(o SendOrigin) *ids.UUID {
	if o.isReply() {
		anchor := o.anchor.UUID
		return &anchor
	}
	return nil
}

func marshalOriginLinks(o SendOrigin) ([]byte, error) {
	if o.isReply() {
		// A reply inherits its links from the anchor, so there are none to
		// freeze. A nil []byte is SQL NULL, which is what the origin-shape
		// CHECK requires of a reply row.
		return nil, nil
	}
	// An account origin always carries a list, never null: the schema's shape
	// check rejects null, and a nil Go slice would encode as exactly that.
	links := o.links
	if links == nil {
		links = []ActivityLinkInput{}
	}
	raw, err := json.Marshal(links)
	if err != nil {
		return nil, fmt.Errorf("scheduled send: freezing the record links: %w", err)
	}
	return raw, nil
}

// principalKind records WHAT will execute this send, not who authorized it.
// The send path withholds a human's sign-off and display name when an agent is
// the actor, so a message scheduled by an agent and fired under a rebuilt human
// principal would go out over a signature its immediate twin would never carry
// (ADR-0104 §4).
func principalKind(p principal.Principal) string {
	if p.Type == principal.PrincipalHuman {
		return "human"
	}
	return "agent"
}
