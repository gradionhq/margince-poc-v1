// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// The scheduled poll: the only thing here that happens without a user, and the
// reason this unit exists.
//
// THE SHAPE TO HOLD ONTO is that a tick reads its work in one transaction,
// closes it, and only then ingests. Runtime.Ingest hands a record to the core's
// capture pipeline, which opens its own transaction — so calling it inside one
// of this unit's would take a second connection while holding one, which on a
// small pool does not fail, it hangs. The core refuses that (ErrNestedIngest)
// rather than letting it happen, and this file is what obeying the rule looks
// like: read, close, ingest, then open a second transaction to move the cursor.
//
// The cursor moves AFTER the ingest and never before it — that asymmetry is the
// whole safety argument. A cursor not advanced past a record that landed costs
// one deduplicated retry, because the natural key makes a replay a no-op; a
// cursor advanced past a record that did not land costs the record.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// pollInbox is the workspace tick: every connected member of this workspace,
// polled once.
//
// One member's failure does not stop the others. Their connection records the
// class and the next tick tries again — a token that was revoked this morning
// must not be the reason nobody else's messages arrive.
func pollInbox(ctx context.Context, rt extension.Runtime) error {
	return pollFleet(ctx, rt, newClient)
}

// clientFactory is how a poll reaches a provider.
//
// It is a parameter rather than a call to newClient inside pollConnection
// because the provider is this unit's ONE true boundary — the HTTP the
// production constructor wraps in the egress guard, which refuses loopback by
// design and therefore refuses a test's own listener. Everything above it (the
// fleet loop, the per-member budget, the cursor write, the failure note) is
// this unit's own logic and is driven end to end through this seam.
type clientFactory func(base, token string) (*client, error)

// pollFleet is the tick's whole behaviour, with the provider boundary injected.
func pollFleet(ctx context.Context, rt extension.Runtime, dial clientFactory) error {
	connections, err := connectedMembers(ctx, rt)
	if err != nil {
		return err
	}
	// The failed members' classes, not just how many failed. A tick that reports
	// only a count cannot say whether one outage took everybody down or six
	// unrelated things did, and those are the two situations an operator has to
	// tell apart before deciding whether there is anything to chase at all.
	var failed []extension.FailureClass
	for _, conn := range connections {
		// One member's slow provider must not spend the whole tick: the
		// deadline below is per CONNECTION, so what a stall costs is that
		// member's turn rather than everybody after them in the list.
		memberCtx, done := context.WithTimeout(ctx, perConnectionBudget)
		pollErr := pollConnection(memberCtx, rt, conn, dial)
		done()
		if pollErr == nil {
			continue
		}
		failed = append(failed, failureClass(pollErr))
		// The failure is recorded on the row rather than returned, so the
		// screen shows which connection is broken and the tick's own outcome
		// stays about the fleet.
		//
		// On the TICK's context, not the member's: the member's is exactly
		// what may have just expired, and a note written on a cancelled
		// context is the one write that must not be lost — it is what stops
		// the next tick starting at the same member with no record of why.
		if noted := noteFailure(ctx, rt, conn, pollErr); noted != nil {
			return noted
		}
	}
	if len(failed) > 0 && len(failed) == len(connections) {
		// Every connection failing is not one member's problem: it is this
		// installation's egress, or the provider being down, and a tick that
		// answered success would leave a fleet-wide outage with no signal
		// anywhere but the rows.
		return fleetFailure(ctx, failed)
	}
	return nil
}

// perConnectionBudget bounds one member's turn. The job's own wall clock
// (api/jobs.yaml) bounds the whole tick; this is what keeps the first slow
// provider in the list from spending it.
const perConnectionBudget = 60 * time.Second

// connectedMembers reads this workspace's connections and CLOSES the
// transaction before anything is ingested.
//
// The whole set is read at once rather than one row at a time: holding a cursor
// open across the provider I/O below would be the nested-transaction defect
// wearing a different hat, and a workspace's connected members are a handful.
//
// LEAST RECENTLY POLLED FIRST, which is fairness rather than tidiness: a fixed
// order plus a bounded tick means the members at the end of a stable list are
// the ones a busy installation never reaches, tick after tick. Ordering by when
// each was last read rotates whoever waited longest to the front, and a
// connection that has never polled sorts first.
func connectedMembers(ctx context.Context, rt extension.Runtime) ([]connection, error) {
	var found []connection
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+connectionColumns+` FROM `+connectionTable+`
			  WHERE status = $1
			  ORDER BY last_polled_at ASC NULLS FIRST, created_at`, statusConnected)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			conn, err := scanConnection(rows.Scan)
			if err != nil {
				return err
			}
			found = append(found, conn)
		}
		return rows.Err()
	})
	return found, err
}

// pollConnection reads one member's inbox and lands what they were directed at.
func pollConnection(ctx context.Context, rt extension.Runtime, conn connection, dial clientFactory) error {
	api, member, err := providerFor(ctx, rt, conn, dial)
	if err != nil {
		return err
	}
	// THE NEWEST REGION FIRST, always. A member whose backlog is still being
	// filled in should still see this morning's messages this morning, which
	// is the whole reason the cursor carries a separate `top`.
	at := conn.cursor()
	budget := maxPagesPerPoll
	if at.firstPoll() {
		budget = firstPollPages
	}
	forward, err := walkInbox(ctx, api, at.forwardFrom(), 0, budget)
	if err != nil {
		return err
	}
	// A systemic failure stops the tick with NO cursor written: nothing is
	// advanced, the connection records the class, and the next tick walks the
	// same region again — where every record that already landed is a
	// deduplicated no-op on its natural key.
	processedTo, err := landAll(ctx, rt, api, forward.items, conn, member)
	if err != nil {
		return err
	}
	at = afterForward(at, processedTo, forward)

	// Whatever budget the forward walk left goes to the backlog. Nothing is
	// spent on it when there is none, and a first poll never has one.
	if spent := len(forward.items)/maxPageSize + 1; at.unread() && spent < budget {
		at, err = fillGap(ctx, rt, api, conn, member, at, budget-spent)
		if err != nil {
			return err
		}
	}
	return saveCursor(ctx, rt, conn, member, at)
}

// fillGap walks the unread region under the newest messages.
//
// It stops at the FLOOR — the id everything below which has been decided about
// — rather than at the top, because the region it is filling is the one between
// them. Reaching it collapses the two numbers back into one.
func fillGap(ctx context.Context, rt extension.Runtime, api *client, conn connection, member providerUser, at cursor, budget int) (cursor, error) {
	backfill, err := walkInbox(ctx, api, at.floor, at.gap, budget)
	if err != nil {
		return at, err
	}
	if _, err := landAll(ctx, rt, api, backfill.items, conn, member); err != nil {
		return at, err
	}
	return afterBackfill(at, backfill), nil
}

// providerFor resolves the member's token and identifies the account it opens.
//
// The token is read from the unit's user-scoped namespace under the declared
// key, and it is the SAME deposit the ingress port reads as this member's
// consent to be acted for — so a connection whose credential is gone cannot
// poll, and would be refused at the port even if it tried.
func providerFor(ctx context.Context, rt extension.Runtime, conn connection, dial clientFactory) (*client, providerUser, error) {
	token, err := rt.Secrets().GetUser(ctx, extension.UserID(conn.UserID), tokenKey)
	if err != nil {
		if errors.Is(err, extension.ErrSecretNotFound) {
			return nil, providerUser{}, fmt.Errorf("%w: this member has no token on deposit", errUnauthorized)
		}
		return nil, providerUser{}, err
	}
	api, err := dial(conn.BaseURL, string(token))
	if err != nil {
		return nil, providerUser{}, err
	}
	member, err := api.me(ctx)
	if err != nil {
		return nil, providerUser{}, err
	}
	return api, member, nil
}

// landAll ingests the directed notifications of one walk, oldest first.
//
// It answers the highest id it DECIDED ABOUT — which includes what was filtered
// and what the core deliberately skipped, because a cursor that only moved past
// landed records would re-page a feed of reactions forever.
func landAll(ctx context.Context, rt extension.Runtime, api *client, items []inboxItem, conn connection, member providerUser) (processedTo int64, err error) {
	// Oldest first, so that the ids decided about are a contiguous run from the
	// bottom: a tick that stops halfway leaves everything above it untouched
	// and above the mark, where the next tick finds it again.
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	senders, err := resolveSenders(ctx, api, items)
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		if !directed(item) {
			// Decided about: a reaction is not a customer interaction, and the
			// cursor moves past it exactly as it moves past a landed record.
			processedTo = item.ID
			continue
		}
		// A systemic failure — the port refused this unit, the member's
		// authority is gone, the role composed no capture — stops the tick
		// here. Nothing above this id was touched, and the caller writes no
		// cursor, so the whole region is walked again next time.
		//
		// A record this unit cannot represent is NOT that: it will never land,
		// so stopping on it would park the connection on one malformed message
		// forever. landOne separates the two, and both outcomes leave the id
		// decided about.
		if err := landOne(ctx, rt, item, senders[item.SenderID], conn, member); err != nil {
			return processedTo, err
		}
		processedTo = item.ID
	}
	return processedTo, nil
}

// landOne hands one notification to the core, and separates the two failures
// that look alike.
//
// A record the core calls INVALID is one this unit built wrong or cannot build
// at all: retrying it on every tick would park the connection on a single
// malformed message. Every other refusal is about this unit's standing —
// authority, wiring, the provider's own transaction — and those must stop the
// tick rather than skip a record nobody has seen.
func landOne(ctx context.Context, rt extension.Runtime, item inboxItem, sender providerUser, conn connection, member providerUser) error {
	rec, err := recordFor(item, sender, member, member.WorkspaceID)
	if err != nil {
		// Unrepresentable — the sender resolved to no address, so there is no
		// counterparty and no way this record ever becomes one. The cursor
		// moves past it, and a ledger row says so: a provider format change
		// that made EVERY record unrepresentable would otherwise present
		// exactly like a quiet feed. The seam this deserves is filed (#1195).
		return noteDrop(ctx, rt, conn, item, "unrepresentable")
	}
	// on is the CRM MEMBER whose credential produced this record — the
	// connection's own user id, never the provider's account id. The core
	// checks that this member has a credential on deposit with this unit and
	// resolves what they may do right now, so what lands is bounded by their
	// live authority rather than by anything this unit asserts.
	if _, err := rt.Ingest(ctx, extension.UserID(conn.UserID), rec); err != nil {
		if errors.Is(err, extension.ErrInvalid) {
			return noteDrop(ctx, rt, conn, item, "refused_by_the_core")
		}
		return err
	}
	return nil
}

// noteDrop records that one notification will never land, and why.
//
// It writes the unit's own ledger row rather than a core one, because there is
// no core record to hang a drop on — that is the point of a drop. What it buys
// is that "this connector has been dropping every message since Tuesday" is a
// question somebody can answer.
func noteDrop(ctx context.Context, rt extension.Runtime, conn connection, item inboxItem, class string) error {
	payload, err := json.Marshal(struct {
		Notification int64  `json:"notification_id"`
		Class        string `json:"class"`
	}{Notification: item.ID, Class: class})
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

// resolveSenders looks up every distinct sender in ONE call.
//
// Per item it would be one request per notification against a provider this
// unit is a guest of; the batch endpoint exists for exactly this, and a page of
// fifty notifications is usually a handful of people.
func resolveSenders(ctx context.Context, api *client, items []inboxItem) (map[string]providerUser, error) {
	ids := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if !directed(item) || item.SenderID == "" || seen[item.SenderID] {
			continue
		}
		seen[item.SenderID] = true
		ids = append(ids, item.SenderID)
	}
	return api.users(ctx, ids)
}

// saveCursor writes what the tick decided, in a transaction of its own — opened
// after every ingest has returned, which is the rule this file exists to keep.
//
// The account label and the provider workspace are refreshed here rather than
// at connect, because they are what the provider says NOW: a member who renames
// themselves in Relay should not have the CRM screen showing what they were
// called when they pasted a token.
func saveCursor(ctx context.Context, rt extension.Runtime, conn connection, member providerUser, at cursor) error {
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		updated, err := scanConnection(tx.QueryRow(ctx,
			`UPDATE `+connectionTable+`
			    SET high_water_mark = $2,
			        backfill_before = NULLIF($3::bigint, 0),
			        pending_high_water_mark = NULLIF($4::bigint, 0),
			        account_label = $5,
			        provider_workspace_id = $6,
			        last_polled_at = now(),
			        last_error_class = NULL,
			        version = version + 1,
			        updated_at = now()
			  WHERE id = $1::uuid AND version = $7
			 RETURNING `+connectionColumns,
			conn.ID, at.floor, at.gap, at.top, member.name(), member.WorkspaceID, conn.Version).Scan)
		if err != nil {
			if isNoRows(err) {
				// EITHER the member disconnected while this tick was reading
				// their inbox, OR they reconnected and the row moved on
				// without this poll. Both are the same answer: what this tick
				// learned is about a connection that no longer exists in the
				// state it was read in, and writing it would undo whatever the
				// member just did. The records it landed are theirs and stay.
				return nil
			}
			return err
		}
		if updated.cursor() == conn.cursor() {
			// A tick that moved no cursor is a poll that found nothing, and
			// recording it would write one ledger row per member per cadence
			// forever to say that a schedule ran. The touched columns are the
			// timestamp and the label; neither is a fact anybody will later
			// ask who changed.
			return nil
		}
		return recordConnection(ctx, tx, extension.AuditUpdate, eventPolled, &conn, &updated)
	})
}
