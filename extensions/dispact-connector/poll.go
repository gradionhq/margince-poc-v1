// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dispact

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
	"errors"
	"fmt"
	"sort"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// pollInbox is the workspace tick: every connected member of this workspace,
// polled once.
//
// One member's failure does not stop the others. Their connection records the
// class and the next tick tries again — a token that was revoked this morning
// must not be the reason nobody else's messages arrive.
func pollInbox(ctx context.Context, rt extension.Runtime) error {
	connections, err := connectedMembers(ctx, rt)
	if err != nil {
		return err
	}
	var failures int
	for _, conn := range connections {
		if err := pollConnection(ctx, rt, conn); err != nil {
			failures++
			// The failure is recorded on the row rather than returned, so the
			// screen shows which connection is broken and the tick's own
			// outcome stays about the fleet.
			if noted := noteFailure(ctx, rt, conn, err); noted != nil {
				return noted
			}
		}
	}
	if failures > 0 && failures == len(connections) {
		// Every connection failing is not one member's problem: it is this
		// installation's egress, or the provider being down, and a tick that
		// answered success would leave a fleet-wide outage with no signal
		// anywhere but the rows.
		return fmt.Errorf("dispact: all %d connection(s) failed this tick", failures)
	}
	return nil
}

// connectedMembers reads this workspace's connections and CLOSES the
// transaction before anything is ingested.
//
// The whole set is read at once rather than one row at a time: holding a cursor
// open across the provider I/O below would be the nested-transaction defect
// wearing a different hat, and a workspace's connected members are a handful.
func connectedMembers(ctx context.Context, rt extension.Runtime) ([]connection, error) {
	var found []connection
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+connectionColumns+` FROM `+connectionTable+`
			  WHERE status = $1 ORDER BY created_at`, statusConnected)
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
func pollConnection(ctx context.Context, rt extension.Runtime, conn connection) error {
	api, member, err := providerFor(ctx, rt, conn)
	if err != nil {
		return err
	}
	// A connection that has never polled takes ONE page and does not walk back:
	// connecting an account brings the CRM what arrives from now on, not an
	// import of the member's history. A history import is a decision with a
	// scope and a cost, and it is not one a token paste should make silently.
	budget, from := maxPagesPerPoll, conn.BackfillBefore
	if conn.HighWaterMark == 0 && conn.BackfillBefore == 0 {
		budget = 1
	}
	walked, err := walkInbox(ctx, api, conn.HighWaterMark, from, budget)
	if err != nil {
		return err
	}
	// A systemic failure stops the tick with NO cursor written: nothing is
	// advanced, the connection records the class, and the next tick walks the
	// same region again — where every record that already landed is a
	// deduplicated no-op on its natural key.
	processedTo, err := landAll(ctx, rt, api, walked.items, conn, member)
	if err != nil {
		return err
	}
	mark, gap := advanced(conn.HighWaterMark, conn.BackfillBefore, processedTo, walked)
	return saveCursor(ctx, rt, conn, member, mark, gap)
}

// providerFor resolves the member's token and identifies the account it opens.
//
// The token is read from the unit's user-scoped namespace under the declared
// key, and it is the SAME deposit the ingress port reads as this member's
// consent to be acted for — so a connection whose credential is gone cannot
// poll, and would be refused at the port even if it tried.
func providerFor(ctx context.Context, rt extension.Runtime, conn connection) (*client, providerUser, error) {
	token, err := rt.Secrets().GetUser(ctx, extension.UserID(conn.UserID), tokenKey)
	if err != nil {
		if errors.Is(err, extension.ErrSecretNotFound) {
			return nil, providerUser{}, fmt.Errorf("%w: this member has no token on deposit", errUnauthorized)
		}
		return nil, providerUser{}, err
	}
	api, err := newClient(conn.BaseURL, string(token))
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
		// Unrepresentable, and named as such rather than returned: the sender
		// resolved to no address, so there is no counterparty and no way this
		// record ever becomes one.
		return nil
	}
	// on is the CRM MEMBER whose credential produced this record — the
	// connection's own user id, never the provider's account id. The core
	// checks that this member has a credential on deposit with this unit and
	// resolves what they may do right now, so what lands is bounded by their
	// live authority rather than by anything this unit asserts.
	if _, err := rt.Ingest(ctx, extension.UserID(conn.UserID), rec); err != nil {
		if errors.Is(err, extension.ErrInvalid) {
			return nil
		}
		return err
	}
	return nil
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
// themselves in Dispact should not have the CRM screen showing what they were
// called when they pasted a token.
func saveCursor(ctx context.Context, rt extension.Runtime, conn connection, member providerUser, mark, gap int64) error {
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		updated, err := scanConnection(tx.QueryRow(ctx,
			`UPDATE `+connectionTable+`
			    SET high_water_mark = $2,
			        backfill_before = NULLIF($3::bigint, 0),
			        account_label = $4,
			        provider_workspace_id = $5,
			        last_polled_at = now(),
			        last_error_class = NULL,
			        version = version + 1,
			        updated_at = now()
			  WHERE id = $1::uuid
			 RETURNING `+connectionColumns,
			conn.ID, mark, gap, member.name(), member.WorkspaceID).Scan)
		if err != nil {
			if isNoRows(err) {
				// The member disconnected while this tick was reading their
				// inbox. Their records landed and are theirs; there is no row
				// left to move a cursor on, and inventing one would resurrect
				// a connection somebody just withdrew.
				return nil
			}
			return err
		}
		if updated.HighWaterMark == conn.HighWaterMark && updated.BackfillBefore == conn.BackfillBefore {
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

// noteFailure records on the row what went wrong, so the screen can say it.
//
// The class is this unit's, never the provider's message. An unauthorized
// connection PARKS: retrying a revoked token on a cadence is how an
// installation rate-limits itself for nothing, and the member has to paste a
// new one anyway.
func noteFailure(ctx context.Context, rt extension.Runtime, conn connection, cause error) error {
	class, status := failureClass(cause), statusConnected
	if errors.Is(cause, errUnauthorized) {
		status = statusReauth
	}
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		updated, err := scanConnection(tx.QueryRow(ctx,
			`UPDATE `+connectionTable+`
			    SET status = $2, last_error_class = $3, last_polled_at = now(),
			        version = version + 1, updated_at = now()
			  WHERE id = $1::uuid
			 RETURNING `+connectionColumns, conn.ID, status, class).Scan)
		if err != nil {
			if isNoRows(err) {
				return nil
			}
			return err
		}
		verb := eventPolled
		if status == statusReauth {
			verb = eventReauth
		}
		return recordConnection(ctx, tx, extension.AuditUpdate, verb, &conn, &updated)
	})
}

// failureClass names what went wrong in this unit's own vocabulary. The
// provider's text is deliberately not carried: it is rendered on a screen, and
// a remote party's prose is not this installation's to display.
func failureClass(cause error) string {
	switch {
	case errors.Is(cause, errUnauthorized):
		return "token_rejected"
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
