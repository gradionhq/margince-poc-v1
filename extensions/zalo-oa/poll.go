// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The scheduled pull: the only thing here that happens without a user, and the
// reason this unit exists.
//
// THE SHAPE TO HOLD ONTO is that a tick reads its work in one transaction,
// closes it, and only then ingests. Runtime.Ingest hands a record to the core's
// capture pipeline, which opens its own transaction — so calling it inside one
// of this unit's would take a second connection while holding one, which on a
// small pool does not fail, it hangs. The same rule binds the secret port, which
// is why the credential is unsealed before any transaction opens.
//
// The cursor moves AFTER the ingest and never before it, and a tick that fails
// part way writes NO cursor at all. That asymmetry is the whole safety argument:
// a cursor not advanced past a message that landed costs one deduplicated
// retry, because the natural key makes a replay a no-op, while a cursor advanced
// past a message that did not land costs the message — and for this provider it
// costs it permanently, because a message older than the retention window is
// gone from the API with no depth left to page to.
//
// POLL LIVENESS IS THEREFORE A DATA-INTEGRITY CONCERN HERE, not a freshness one.
// A connector down longer than the provider's retention loses conversations that
// no later run can recover, and there is no webhook to have caught them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// pollChats is the workspace tick: this installation's one connection, polled
// once.
func pollChats(ctx context.Context, rt extension.Runtime) error {
	return pollConnection(ctx, rt, newClient, newOAuthClient(), time.Now)
}

// clock is where a tick reads the time, injected so the credential's expiry
// arithmetic is provable without waiting a day for it.
type clock func() time.Time

// pollConnection runs one tick with every boundary injected.
func pollConnection(ctx context.Context, rt extension.Runtime, dial clientFactory,
	grants grantExchanger, now clock,
) error {
	conn, err := connectedInstallation(ctx, rt)
	if err != nil || conn == nil {
		return err
	}
	token, current, err := usableToken(ctx, rt, grants, *conn, now())
	if err != nil {
		return credentialOutcome(ctx, rt, *conn, err)
	}
	api := dial(token.AccessToken)
	// The account is re-read every tick rather than trusted from connect time,
	// and the tier evidence with it. A package EXPIRES: a connection that was
	// admitted in March starts being refused the day its term ends, and an
	// operator reading this screen needs to see the package and the date that
	// explain it rather than a poll failing in a log.
	label, err := api.profile(ctx)
	if err != nil {
		return noteFailure(ctx, rt, current, err)
	}
	// AND THE ACCOUNT IS RECONCILED, every tick, against the one the row claims.
	//
	// The connect path takes the id from the token rather than from the request
	// that carried it, which is what makes the two agree in the first place. This
	// is the same obligation from the other end: a credential that has come to
	// answer for a DIFFERENT account than the row names would key every message it
	// landed under a namespace belonging to somebody else's people, and every
	// reply would resolve a stranger holding the same number. Nothing about that
	// is visible afterwards, so it is refused before a single record is built.
	if label.OAID != current.OAID {
		if _, err := park(ctx, rt, current, "account_changed", statusReauth); err != nil {
			return err
		}
		return nil
	}
	at := current.cursor()
	// THE CEILING IS THE ROW'S. The account read above has already spent one
	// request against it, so what is left is what the walk may page. A first poll
	// ignores it downward and never upward: connecting an account brings what
	// arrives from now on, whatever ceiling the row carries.
	budget := pageBudget(current.PollRequestBudget)
	if at.firstPoll() {
		budget = firstPollPages
	}
	// THE NEWEST REGION FIRST, always. An installation whose backlog is still
	// being filled in should still see this morning's messages this morning,
	// which is the whole reason the cursor carries a separate `top`.
	forward, err := walkChats(ctx, api, walkSpec{stopBelow: at.forwardFrom(), budget: budget})
	if err != nil {
		return noteFailure(ctx, rt, current, err)
	}
	decidedTo, err := landAll(ctx, rt, forward.items, current)
	if err != nil {
		// Nothing is advanced, the connection records the class, and the next
		// tick walks the same region again — where every message that already
		// landed is a deduplicated no-op on its natural key.
		return noteFailure(ctx, rt, current, err)
	}
	at = afterForward(at, decidedTo, forward)

	// Whatever budget the forward walk left goes to the backlog. Nothing is spent
	// on it when there is none, and a first poll never has one.
	if spent := forward.pagesRead; at.unread() && spent < budget {
		at, err = fillGap(ctx, rt, api, current, at, len(forward.items), budget-spent)
		if err != nil {
			return noteFailure(ctx, rt, current, err)
		}
	}
	if err := saveCursor(ctx, rt, current, label, at); err != nil {
		return err
	}
	// The markers are swept on the tick because the only thing that makes one
	// worth keeping is a walk that might still read the message it names, and the
	// walk is here. A sweep that fails is not this tick's outcome: the records
	// landed, the cursor moved, and a marker kept too long suppresses nothing that
	// the provider can still serve.
	return forgetOldSentMarkers(ctx, rt)
}

// fillGap walks the unread region under the newest messages.
//
// It stops at the FLOOR — the point everything below which has been decided
// about — rather than at the top, because the region it is filling is the one
// between them. Reaching it collapses the two numbers back into one.
func fillGap(ctx context.Context, rt extension.Runtime, api *client, conn connection,
	at cursor, arrivedSince, budget int,
) (cursor, error) {
	backfill, err := walkChats(ctx, api, walkSpec{
		stopBelow: at.floor,
		skipAbove: at.gap,
		startPage: resumePage(at, arrivedSince),
		budget:    budget,
	})
	if err != nil {
		return at, err
	}
	if _, err := landAll(ctx, rt, backfill.items, conn); err != nil {
		return at, err
	}
	return afterBackfill(at, backfill), nil
}

// connectedInstallation reads the connection this tick acts on, and CLOSES the
// transaction before anything else happens.
//
// A connection that is not `connected` is not an error and not a skip worth
// reporting: the two parked states already say what a human must do, and a tick
// that failed on them would fill a log with the fact that somebody has not
// repaired something yet.
func connectedInstallation(ctx context.Context, rt extension.Runtime) (*connection, error) {
	var found *connection
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		var err error
		found, err = currentConnection(ctx, tx)
		return err
	})
	if err != nil || found == nil || found.Status != statusConnected {
		return nil, err
	}
	return found, nil
}

// credentialOutcome decides what a tick does about a credential it could not
// use.
//
// The three cases are genuinely different and collapsing them would each cost
// something: another caller renewing is not a failure at all, a credential that
// is simply absent is a state a human must repair, and a rotation whose result
// was not kept has ALREADY parked itself and must not be parked twice under a
// class that would overwrite the one naming what happened.
func credentialOutcome(ctx context.Context, rt extension.Runtime, conn connection, cause error) error {
	switch {
	case errors.Is(cause, errRefreshInFlight):
		// Somebody else is renewing right now. The next tick finds a fresh
		// token; reporting this as a failed tick would make an ordinary race
		// look like an outage.
		return nil
	case errors.Is(cause, errCredentialGone):
		if _, err := park(ctx, rt, conn, "credential_missing", statusReauth); err != nil {
			return errors.Join(cause, err)
		}
		return nil
	case errors.Is(cause, errRotationLost):
		// Already parked, by the code that knows which way the rotation went.
		// It is returned rather than swallowed because a lost rotation is the
		// one failure in this unit that a human has to be told about.
		return cause
	default:
		return noteFailure(ctx, rt, conn, cause)
	}
}

// landAll ingests the messages of one walk, oldest first.
//
// It answers the newest timestamp it DECIDED ABOUT — which includes what the
// core deliberately skipped and what this unit could not represent, because a
// cursor that only moved past landed messages would re-page a feed of stickers
// forever.
//
// Oldest first, so that what is decided about is a contiguous run upward from
// the floor: a tick that stops half way leaves everything above it untouched and
// above the mark, where the next tick finds it again. That is also why the
// caller writes no cursor at all when this returns an error.
func landAll(ctx context.Context, rt extension.Runtime, msgs []chatMessage, conn connection) (int64, error) {
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].Time < msgs[j].Time })
	ours, err := sentByThisInstallation(ctx, rt, conn.OAID, outboundIDs(msgs))
	if err != nil {
		return 0, err
	}
	var decidedTo int64
	for _, msg := range msgs {
		if ours[msg.MessageID] {
			// A message this installation SENT. The core wrote it as an activity
			// when the rep staged it, so capturing it back would put one real
			// message on a timeline twice with nothing to say which row is the
			// duplicate. It is DECIDED ABOUT rather than skipped — the cursor
			// moves past it exactly as it moves past a landed record, or the walk
			// would meet it again forever.
			decidedTo = msg.Time
			continue
		}
		if err := landOne(ctx, rt, msg, conn); err != nil {
			return decidedTo, err
		}
		decidedTo = msg.Time
	}
	return decidedTo, nil
}

// outboundIDs are the ids worth asking about: only a message the ACCOUNT sent can
// be one this installation sent, so an inbound page costs no query at all.
func outboundIDs(msgs []chatMessage) []string {
	ids := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		if !msg.inbound() && msg.MessageID != "" {
			ids = append(ids, msg.MessageID)
		}
	}
	return ids
}

// landOne hands one message to the core, and separates the two failures that
// look alike.
//
// A record the core calls INVALID is one this unit built wrong or cannot build
// at all: retrying it on every tick would park the connection on a single
// malformed message. Every other refusal is about this unit's standing —
// authority, wiring, the pipeline's own transaction — and those must stop the
// tick rather than skip a message nobody has seen.
func landOne(ctx context.Context, rt extension.Runtime, msg chatMessage, conn connection) error {
	rec, err := recordFor(msg, conn.OAID)
	if err != nil {
		// Unrepresentable — no counterparty account, or no provider id — so
		// there is no way this message ever becomes a record. The cursor moves
		// past it and a ledger row says so: a provider format change that made
		// EVERY message unrepresentable would otherwise present exactly like an
		// account nobody writes to.
		return noteDrop(ctx, rt, conn, msg, "unrepresentable")
	}
	// The member is the ADMIN WHO AUTHORIZED this connection — whose sealed
	// credential produced the message, and whose live authority the core
	// resolves per record. What lands is bounded by what they may do right now
	// rather than by anything this unit asserts, so an admin demoted since they
	// authorized lands less from the next message onward.
	if _, err := rt.Ingest(ctx, extension.UserID(conn.AuthorizedBy), rec); err != nil {
		if errors.Is(err, extension.ErrInvalid) {
			return noteDrop(ctx, rt, conn, msg, "refused_by_the_core")
		}
		return err
	}
	return nil
}

// noteDrop records that one message will never land, and why.
//
// It writes the unit's own ledger row rather than a core one, because there is
// no core record to hang a drop on — that is the point of a drop. What it buys
// is that "this connector has been dropping every message since Tuesday" is a
// question somebody can answer.
func noteDrop(ctx context.Context, rt extension.Runtime, conn connection, msg chatMessage, class string) error {
	payload, err := json.Marshal(struct {
		MessageID string `json:"message_id"`
		Class     string `json:"class"`
	}{MessageID: msg.MessageID, Class: class})
	if err != nil {
		return err
	}
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		return tx.Record(ctx,
			extension.Change{
				Action: extension.AuditUpdate,
				Entity: connectionEntity,
				ID:     conn.ID,
				Detail: payload,
			},
			extension.Event{Verb: eventRecordDropped, Payload: payload})
	})
}

// saveCursor writes what the tick decided, in a transaction of its own — opened
// after every ingest has returned, which is the rule this file exists to keep.
//
// The account label and the tier evidence are refreshed here rather than at
// connect, because they are what the provider says NOW: an OA renamed at
// oa.zalo.me, or a package renewed for another year, should show on this screen
// without anybody re-authorizing.
func saveCursor(ctx context.Context, rt extension.Runtime, conn connection, label oaProfile, at cursor) error {
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		updated, err := scanConnection(tx.QueryRow(ctx,
			`UPDATE `+connectionTable+`
			    SET high_water_mark = $2,
			        backfill_before = NULLIF($3::bigint, 0),
			        pending_high_water_mark = NULLIF($4::bigint, 0),
			        backfill_offset = $5,
			        account_label = $6,
			        package_name = $7,
			        package_valid_through = $8,
			        last_polled_at = now(),
			        last_error_class = NULL,
			        version = version + 1,
			        updated_at = now()
			  WHERE id = $1::uuid AND version = $9
			 RETURNING `+connectionColumns,
			conn.ID, at.floor, at.gap, at.top, at.offset,
			label.Name, label.PackageName, label.PackageValidThroughDate, conn.Version).Scan)
		if err != nil {
			if isNoRows(err) {
				// EITHER the connection was removed while this tick was reading,
				// OR it was re-authorized and the row moved on without this poll.
				// Both are the same answer: what this tick learned is about a
				// connection that no longer exists in the state it was read in,
				// and writing it would undo whatever the admin just did. The
				// messages it landed are landed and stay.
				return nil
			}
			return err
		}
		if updated.cursor() == conn.cursor() {
			// A tick that moved no cursor is a poll that found nothing, and
			// recording it would write one ledger row per cadence forever to say
			// that a schedule ran. The touched columns are the timestamp and the
			// label; neither is a fact anybody will later ask who changed.
			return nil
		}
		return recordConnection(ctx, tx, extension.AuditUpdate, eventPolled, &conn, &updated)
	})
}

// noteFailure records on the row what went wrong, so the screen can say it.
//
// The class is this unit's, never the provider's message. Two of the outcomes
// PARK the connection rather than retrying it, and which one they park as is the
// distinction the whole error catalog is built around: a credential the provider
// rejects is fixed by re-authorizing, and a PACKAGE the provider says is too low
// is fixed by paying for an upgrade. Sending an operator to do one when they need
// the other is a wasted afternoon in another company.
func noteFailure(ctx context.Context, rt extension.Runtime, conn connection, cause error) error {
	class, status := failureClass(cause), statusConnected
	switch {
	case errors.Is(cause, errUnauthorized):
		status = statusReauth
	case errors.Is(cause, errTierTooLow), errors.Is(cause, errAPINotRegistered):
		status = statusTierLapse
	}
	if status != statusConnected {
		if _, err := park(ctx, rt, conn, class, status); err != nil {
			return errors.Join(cause, err)
		}
		return nil
	}
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		// On the version this poll READ, so a failure from a tick that started
		// before an admin re-authorized cannot mark the connection they just
		// repaired.
		updated, err := scanConnection(tx.QueryRow(ctx,
			`UPDATE `+connectionTable+`
			    SET last_error_class = $2, last_polled_at = now(),
			        version = version + 1, updated_at = now()
			  WHERE id = $1::uuid AND version = $3
			 RETURNING `+connectionColumns, conn.ID, class, conn.Version).Scan)
		if err != nil {
			if isNoRows(err) {
				// The row moved on without this tick. What it learned is about a
				// connection in a state that no longer exists, and the tick that
				// moved it will report its own outcome.
				return nil
			}
			return err
		}
		// RECORDED, like every other state change on this row. The unit's ledger
		// header names exactly one exemption — the poll's last_polled_at touch on
		// an otherwise unchanged row — and this is not it: what is written here is
		// the class a screen renders, and "when did this start failing" is the
		// question a human brings to it.
		return recordConnection(ctx, tx, extension.AuditUpdate, eventPolled, &conn, &updated)
	})
	if err != nil {
		return errors.Join(cause, err)
	}
	// The failure is on the row for the screen to render; the tick's own outcome
	// is the failure, because this installation has exactly one connection and a
	// tick that could not poll it did not do its job.
	return fmt.Errorf("zalo-oa: the poll failed (%s): %w", class, cause)
}

// failureClass names what went wrong in this unit's own vocabulary. The
// provider's text is deliberately not carried: it is rendered on a screen, and a
// remote party's prose is not this installation's to display.
func failureClass(cause error) string {
	switch {
	case errors.Is(cause, errUnauthorized):
		return "token_rejected"
	case errors.Is(cause, errTierTooLow):
		return "package_too_low"
	case errors.Is(cause, errAPINotRegistered):
		return "api_not_registered"
	case errors.Is(cause, errTransient):
		return "provider_unavailable"
	case errors.Is(cause, errProvider):
		return "provider_answer_unusable"
	case errors.Is(cause, extension.ErrForbidden):
		return "member_not_permitted"
	case errors.Is(cause, extension.ErrInvalid):
		return "connection_unusable"
	default:
		return "poll_failed"
	}
}
