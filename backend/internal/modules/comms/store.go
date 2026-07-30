// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// Delivery status. There is no in-flight status on purpose: a crash mid-send
// would strand a row in it, and a guard keyed on that status would then turn
// River's redelivery into a silent skip — disabling the connector's
// retransmission check in exactly the crash it exists for. River serializes one
// job per delivery; terminal status plus that check is what makes a retry safe.
const (
	StatusPending = "pending"
	StatusSent    = "sent"
	StatusParked  = "parked"
)

// ErrTerminal marks a delivery that is already finished (or was never staged).
// A redelivered job hits this, and it is a normal at-least-once outcome rather
// than a failure.
var ErrTerminal = errors.New("comms: delivery is already terminal")

// ErrDuplicateMessage marks a second staging of a message identity this
// workspace already staged. It is the (workspace_id, message_id) idempotency
// key answering, phrased so the caller learns what to do without learning what
// the database is called: a wrapped pgx violation carries the constraint and
// table names, and a client is owed neither.
var ErrDuplicateMessage = fmt.Errorf(
	"comms: this message identity is already staged for delivery in this workspace: %w", apperrors.ErrConflict)

// ErrNoAddressee marks a delivery staged with nobody to reach. A message with
// neither a To nor a Cc address can only be refused later — the consent gate
// asks about an empty list and answers no — so it is refused here, where the
// caller is still in the transaction that would have written the row.
var ErrNoAddressee = errors.New("comms: a delivery needs at least one recipient or cc address")

// Store is the comms_outbound seam: staging, loading with attempt-counting,
// and the four terminal/retry transitions. It carries no RBAC gate of its
// own — see the internal/modules/comms waivers in backend/rbacgate_test.go
// for why.
type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
	// identity re-keys the timeline row of a message whose provider stamped
	// an identity other than the one this system minted. It is a REQUIRED
	// constructor parameter rather than an option, because there is no safe
	// default: a role that transmits without one files every sent message
	// under an identity that exists nowhere on the wire, and duplicates it on
	// the timeline, silently.
	identity MessageIdentityReconciler
}

// NewStore builds the store. The clock is injected so age arithmetic is asserted
// by advancing time, never by sleeping.
func NewStore(pool *pgxpool.Pool, now func() time.Time, identity MessageIdentityReconciler) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{pool: pool, now: now, identity: identity}
}

// StageInput is one message staged for transmission, written in the caller's
// transaction so the delivery and the activity it belongs to commit together.
//
// There is deliberately no UserID field: the sending identity — whose Gmail
// credential eventually transmits the message — is stamped by StageTx from
// the authenticated principal, never taken from a caller-supplied value.
// This matches storekit.CapturedBy's provenance rule ("stamped from the
// authenticated principal, never from the request body"): a caller that
// could name an arbitrary user_id could stage a delivery that later sends
// through someone else's mailbox.
type StageInput struct {
	ActivityID     ids.ActivityID
	Provider       string
	MessageID      string // unbracketed
	Recipients     []string
	Cc             []string
	Subject        string
	Body           string // unsubscribe footer already applied
	ConsentPurpose string
	InReplyTo      string   // unbracketed; empty starts a thread
	References     []string // unbracketed ancestry, oldest first
	// ThreadKey is the RFC822 conversation identity this message joins. It is
	// written and never loaded back — not here and nowhere else in the tree:
	// the wire carries threading in the In-Reply-To/References headers above,
	// so the dispatcher needs none of it. It is stored because the delivery
	// row is the send log's own record of which conversation the message
	// joined, held independently of the activity this delivery reports on.
	ThreadKey       string
	ListUnsubscribe string // the Post header is derived from this, never stored
}

// Delivery is one staged message as the dispatcher sees it, in EITHER shape:
// the row is mail-shaped or channel-shaped and never half of each
// (comms_outbound_shape, 0155).
//
// On a channel delivery the mail fields read as their ZERO VALUES rather than as
// pointers, because a channel genuinely has no subject and no RFC822 identity —
// there is no second fact for a nil to carry that an empty string does not
// already say. ChannelUserID is the one exception, and its own comment says why.
// IsChannel is the ONE spelling of "which shape is this".
type Delivery struct {
	ID         ids.UUID
	ActivityID ids.ActivityID
	UserID     ids.UserID
	Provider   string
	MessageID  string
	Recipients []string
	Cc         []string
	Subject    string
	Body       string
	// ChannelUserID is the recipient's account id at the provider for a channel
	// delivery, and NIL for a mail one.
	//
	// A pointer because NULL and empty are DIFFERENT facts on this column. NULL
	// is the row declaring itself mail-shaped; empty is a channel delivery whose
	// recipient a privacy scrub removed, emptying it exactly as it empties
	// mail's address lists (privacy/deliveries.go) — the column is also the
	// shape discriminator, so nulling it there would re-declare the row as mail
	// with every mail column missing. Collapsed into one string, a scrubbed
	// channel delivery would read as mail addressed to nobody.
	ChannelUserID  *string
	ConsentPurpose string
	// InReplyTo names the message this one replies to, in the vocabulary of its
	// own transport: an unbracketed RFC822 identity for mail, the provider's own
	// message id for a channel. Empty starts an unanchored message either way.
	InReplyTo       string
	References      []string
	ListUnsubscribe string
	// InFlightAt is when a PREVIOUS attempt handed this delivery to the provider
	// without recording what came back — nil when none did. It is set only on
	// the shapes whose retries cannot detect a prior send
	// (comms_outbound_inflight_is_channel, 0150), and reading it non-nil is what
	// makes the difference between an unsent message and a second copy of one
	// the customer already has.
	InFlightAt *time.Time
	Status     string
	Attempts   int
	CreatedAt  time.Time
}

// IsChannel reports whether this delivery leaves through a messaging channel
// rather than mail. It reads the row's shape discriminator, which the schema
// guarantees is present for exactly the channel-shaped rows.
func (d Delivery) IsChannel() bool { return d.ChannelUserID != nil }

// ChannelRecipient is the account id to deliver to: empty for a mail delivery,
// and empty for a channel delivery a privacy scrub has emptied — which no gate
// accepts as a recipient, so an erased subject cannot be messaged by a delivery
// that outlived them.
func (d Delivery) ChannelRecipient() string {
	if d.ChannelUserID == nil {
		return ""
	}
	return *d.ChannelUserID
}

// StageTx records one delivery inside the caller's transaction. user_id —
// whose mailbox eventually transmits the message — is derived from the
// authenticated principal on ctx, exactly as storekit.CapturedBy stamps
// captured_by everywhere else; no caller input can put a different user's
// id in that column. A principal with no app_user identity (system,
// connector) cannot stage a delivery at all: sending is a human act.
func (s *Store) StageTx(ctx context.Context, tx pgx.Tx, in StageInput) (ids.UUID, error) {
	userID, err := stagingUser(ctx)
	if err != nil {
		return ids.UUID{}, err
	}
	if len(in.Recipients) == 0 && len(in.Cc) == 0 {
		return ids.UUID{}, ErrNoAddressee
	}

	recipients, err := marshalList(in.Recipients)
	if err != nil {
		return ids.UUID{}, fmt.Errorf("comms: encoding recipients: %w", err)
	}
	cc, err := marshalList(in.Cc)
	if err != nil {
		return ids.UUID{}, fmt.Errorf("comms: encoding cc: %w", err)
	}
	refs, err := marshalList(in.References)
	if err != nil {
		return ids.UUID{}, fmt.Errorf("comms: encoding references: %w", err)
	}
	id := ids.NewV7()
	if _, err := tx.Exec(ctx, `
		INSERT INTO comms_outbound
		  (id, workspace_id, activity_id, user_id, provider, message_id,
		   recipients, cc, subject, body, consent_purpose, in_reply_to,
		   references_chain, thread_key, list_unsubscribe, status, created_at)
		VALUES ($1, current_setting('app.workspace_id')::uuid, $2, $3, $4, $5,
		        $6, $7, $8, $9, $10, NULLIF($11,''), $12, NULLIF($13,''),
		        NULLIF($14,''), 'pending', $15)`,
		id, in.ActivityID, userID, in.Provider, in.MessageID,
		recipients, cc, in.Subject, in.Body, in.ConsentPurpose,
		in.InReplyTo, refs, in.ThreadKey, in.ListUnsubscribe, s.now().UTC()); err != nil {
		// The idempotency key is an ANSWER, and it is mapped rather than
		// wrapped: a raw violation carries the constraint and table names, and
		// no caller is owed the schema behind a refusal it can act on.
		if storekit.IsUniqueViolation(err) {
			return ids.UUID{}, ErrDuplicateMessage
		}
		return ids.UUID{}, fmt.Errorf("comms: staging delivery: %w", err)
	}
	return id, nil
}

// stagingUser derives the sending identity from the authenticated principal on
// ctx, exactly as storekit.CapturedBy stamps captured_by everywhere else. It is
// shared by both staging shapes so neither can grow a caller-suppliable
// override: a caller that could name an arbitrary user_id could stage a
// delivery that later transmits through someone else's connection.
//
// A principal with no app_user identity (system, connector) cannot stage a
// delivery at all: sending is a human act.
func stagingUser(ctx context.Context) (ids.UserID, error) {
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return ids.UserID{}, err
	}
	if actor.UserID.IsZero() {
		return ids.UserID{}, fmt.Errorf(
			"comms: staging a delivery requires an authenticated app_user identity, got principal type %q", actor.Type)
	}
	return ids.From[ids.UserKind](actor.UserID), nil
}

// marshalList encodes one address or reference list as a JSON ARRAY, never
// null. A nil Go slice marshals to null, and the column's shape constraint
// refuses it for a reason worth restating here: null and [] decode to the same
// nil slice, so a delivery whose list was never written would be
// indistinguishable from one addressed to nobody.
func marshalList(values []string) ([]byte, error) {
	if values == nil {
		values = []string{}
	}
	return json.Marshal(values)
}

// Load reads one delivery and counts the attempt about to be made — durably,
// before anything can reach the provider, so a crash mid-send can never leave
// the retry looking like a first send. A dispatch that turns out to transmit
// nothing usually keeps the rung anyway: the restore is the PACING deferral's
// alone (RecordDeferral), because only there did one of this installation's
// own rules hold the message with no provider ever asked. A park, and a fault
// raised before the send call, both spend theirs — so the count errs HIGH,
// which is the conservative direction: an early park, never a retry that skips
// its prior-send lookup and mails a real recipient twice.
//
// It returns ErrTerminal for a delivery that already finished — or was never
// staged in this workspace — which is how a redelivered job stops without
// transmitting, rather than dereferencing a row that is not there.
//
// Every mail column is COALESCED because a channel-shaped row carries none of
// them (comms_outbound_shape, 0155): the identity, the subject and the three
// address/reference lists are all NULL there. Without the coalesces the very
// first channel delivery fails at load time — a NULL into a Go string, and a
// NULL jsonb into a decode — which is a delivery that can never leave and a
// fault that names the scan rather than the shape. channel_user_id and
// inflight_at are the two columns NOT coalesced: one is the shape discriminator
// and the other says whether a prior attempt reached the provider, and for both
// of them NULL is the answer this scan needs rather than a value to substitute.
func (s *Store) Load(ctx context.Context, id ids.UUID) (Delivery, error) {
	var d Delivery
	var recipients, cc, refs []byte
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			UPDATE comms_outbound
			   SET attempts = attempts + 1
			 WHERE id = $1 AND status = 'pending'
			RETURNING id, activity_id, user_id, provider, coalesce(message_id, ''),
			          coalesce(recipients, '[]'::jsonb), coalesce(cc, '[]'::jsonb),
			          coalesce(subject, ''), body, channel_user_id, consent_purpose,
			          coalesce(in_reply_to, ''), coalesce(references_chain, '[]'::jsonb),
			          coalesce(list_unsubscribe, ''), inflight_at, status, attempts, created_at`,
			id).Scan(&d.ID, &d.ActivityID, &d.UserID, &d.Provider, &d.MessageID,
			&recipients, &cc, &d.Subject, &d.Body, &d.ChannelUserID, &d.ConsentPurpose,
			&d.InReplyTo, &refs, &d.ListUnsubscribe, &d.InFlightAt, &d.Status, &d.Attempts, &d.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrTerminal
	}
	if err != nil {
		return Delivery{}, fmt.Errorf("comms: loading delivery: %w", err)
	}
	if err := json.Unmarshal(recipients, &d.Recipients); err != nil {
		return Delivery{}, fmt.Errorf("comms: decoding recipients: %w", err)
	}
	if err := json.Unmarshal(cc, &d.Cc); err != nil {
		return Delivery{}, fmt.Errorf("comms: decoding cc: %w", err)
	}
	if err := json.Unmarshal(refs, &d.References); err != nil {
		return Delivery{}, fmt.Errorf("comms: decoding references: %w", err)
	}
	return d, nil
}

// RecordSent closes a delivery against the provider's receipt, and — only once
// that receipt has COMMITTED — re-keys the message onto the identity the
// provider actually stamped.
//
// TWO transactions, and which fact is in which is the safety property rather
// than an implementation detail. By the time this is called the provider has
// accepted the message, so an obligation exists that nothing here may revoke:
// leaving the delivery pending sends it back to River, and the connector's
// prior-send lookup cannot see an identity the provider discarded — it finds
// nothing and transmits again. The receipt therefore goes in first, alone, in a
// transaction that carries no bookkeeping it could fail with. Everything after
// it is best effort and reports nothing: a reconcile fault degrades to "receipt
// recorded, one duplicate timeline row", which is exactly the behaviour that
// existed before the re-key did.
//
// One transaction with the re-key under a savepoint is NOT the same guarantee,
// which is why it is not what this does. A savepoint isolates a refused
// statement; it does not isolate a failed RELEASE, a rollback that cannot be
// issued, a dropped connection, or a panic raised outside the guarded call. Any
// of those leaves the receipt as an uncommitted UPDATE in a transaction that
// then fails to commit — the error reaches the dispatcher, and the double-send
// is back. Committed first, the receipt cannot be taken back by anything the
// reconcile does.
//
// Both run under a context DETACHED from the caller's (detachedWrite): the job
// deadline expiring, or the worker being cancelled, mid-send is not permission
// to forget that a real message left the building.
//
// Guarded on status = 'pending': a stale attempt (network partition, GC pause)
// can lose a race against a newer attempt that already closed the same row.
// Rather than clobber a 'sent' or 'parked' row — a real receipt overwritten,
// or worse, un-sent by a stale park — a delivery that is no longer pending
// reports ErrTerminal. That is a benign no-op, the same fact Load already
// reports the same way: the dispatcher must treat it as "already handled,"
// never as retryable. Zero rows also means no re-key: there is no receipt here
// to correct the identity of.
//
// It reads the staged identity back from the receipt's own UPDATE rather than
// with a second query: the row is already being written, and a separate read
// would be a second chance for the two to disagree.
func (s *Store) RecordSent(ctx context.Context, id ids.UUID, receipt connector.SendReceipt) error {
	activityID, staged, err := s.commitReceipt(ctx, id, receipt)
	if err != nil {
		return err
	}
	s.reconcileIdentity(ctx, id, activityID, staged, receipt.RFC822MessageID)
	return nil
}

// commitReceipt writes the receipt and nothing else, and returns only once it
// is durable. It reports ErrTerminal for a delivery a newer attempt already
// closed, and a wrapped error for the one failure that IS the dispatcher's to
// act on: a receipt that did not land at all.
//
// message_id is COALESCED for Load's reason: it is NULL on a channel row, and a
// NULL into a Go string fails the scan — which would report a receipt as
// unrecorded for a message the provider has already accepted, and send the
// delivery back to a ladder that cannot tell it went. The staged identity that
// comes back is then empty, which the reconcile reads as "nothing to re-key" —
// the correct answer for a transport with no mail identity at all.
func (s *Store) commitReceipt(ctx context.Context, id ids.UUID, receipt connector.SendReceipt) (ids.ActivityID, string, error) {
	ctx, cancel := detachedWrite(ctx)
	defer cancel()

	var activityID ids.ActivityID
	var staged string
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// inflight_at is cleared with the receipt: the outcome is now KNOWN, and
		// a sent row still carrying the marker would read as a transmission
		// nobody could account for. It is already NULL on every mail row, so this
		// is the one write that closes the marker rather than a shape-specific
		// branch.
		return tx.QueryRow(ctx, `
			UPDATE comms_outbound
			   SET status = 'sent', provider_message_id = $2, sent_at = $3, reason = NULL,
			       inflight_at = NULL
			 WHERE id = $1 AND status = 'pending'
			RETURNING activity_id, coalesce(message_id, '')`,
			id, receipt.ProviderMessageID, s.now().UTC()).Scan(&activityID, &staged)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.ActivityID{}, "", ErrTerminal
	}
	if err != nil {
		return ids.ActivityID{}, "", fmt.Errorf("comms: recording the send receipt: %w", err)
	}
	return activityID, staged, nil
}

// detachedWriteTimeout bounds a write the caller's cancellation no longer
// governs. It is generous next to the statements it covers — one guarded UPDATE
// and, for the reconcile, a handful more — and small next to the job timeout
// that would otherwise be reached, because the point of the bound is that a
// database that has stopped answering cannot hold a worker's shutdown open
// indefinitely. Without a deadline of its own, detaching from cancellation
// would trade one hazard for another.
const detachedWriteTimeout = 30 * time.Second

// detachedWrite carries a write past the death of the job that triggered it,
// with a deadline of its own. It is for the writes that record something the outside
// world has ALREADY been told — here, that a provider accepted a message.
// Cancelling the job cannot un-send that mail, so it must not be able to
// un-record it either: the dispatcher would see a failed receipt, retry, and
// mail the recipient a second time.
func detachedWrite(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), detachedWriteTimeout)
}

// Park ends a delivery that no retry repairs, recording why in words an
// operator can act on. Guarded on status = 'pending' for the same reason as
// RecordSent — a stale attempt losing a race against a newer one that
// already closed the row reports ErrTerminal, a benign no-op the dispatcher
// must not treat as retryable.
func (s *Store) Park(ctx context.Context, id ids.UUID, reason string) error {
	return s.update(ctx, `UPDATE comms_outbound SET status = 'parked', reason = $2 WHERE id = $1 AND status = 'pending'`, id, reason)
}

// RecordFailure notes a transient fault and leaves the delivery pending for
// something else to bring it back. WHAT brings it back differs by caller, and
// the difference is in the RUNNER's ladder, never in this row: a retry returns
// the fault and spends a rung of it, while a provider throttle returns a
// snooze, which the runner honours by restoring the job attempt instead. This
// row's own `attempts` is kept either way — only RecordDeferral gives that one
// back. Same race as RecordSent/Park: a delivery a newer attempt already closed
// reports ErrTerminal rather than being silently reopened or dropped.
func (s *Store) RecordFailure(ctx context.Context, id ids.UUID, reason string) error {
	return s.update(ctx, `UPDATE comms_outbound SET reason = $2 WHERE id = $1 AND status = 'pending'`, id, reason)
}

// RecordDeferral notes why a delivery is being held back AND gives back the
// attempt Load counted, in one statement. It is for the PACING deferral only —
// the case where one of this installation's own policies held the message back
// and nothing was handed to a provider. A provider throttle is a different
// fact: the message reached the provider, so it keeps its rung and takes
// RecordFailure.
//
// attempts means TRANSMISSION attempts — both readers depend on it meaning
// that. The exhaustion guard parks a delivery whose ladder is spent, and the
// connector's prior-send lookup fires on a non-zero count precisely because a
// previous attempt may already have put the message on the wire. A pacing
// deferral put nothing on the wire: it never reached a provider, so it must
// consume no rung. Leaving the increment in place would park a paced delivery
// as "ladder exhausted" after N windows without it ever having tried to send,
// and would make the maximum-age bound unreachable.
//
// The restore is deliberately the ONLY way the counter goes down, and it is
// safe in the crash direction: Load's increment is already durable before
// anything can reach the provider, so a crash between the two leaves the count
// one too HIGH — an early park, never a retry that skips its prior-send lookup
// and mails a real recipient twice.
func (s *Store) RecordDeferral(ctx context.Context, id ids.UUID, reason string) error {
	return s.update(ctx, `
		UPDATE comms_outbound
		   SET reason = $2, attempts = greatest(attempts - 1, 0)
		 WHERE id = $1 AND status = 'pending'`, id, reason)
}

// update runs a status-guarded transition and reports ErrTerminal — never
// apperrors.ErrNotFound — when it touches zero rows. Every caller's SQL
// already scopes to `status = 'pending'`, so zero rows means either the
// delivery does not exist in this workspace or it is already closed; Load
// answers both the same way, and these transitions do too.
func (s *Store) update(ctx context.Context, sql string, args ...any) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("comms: updating delivery: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrTerminal
		}
		return nil
	})
}
