// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// What this installation SENT, remembered just long enough that the poll does not
// capture it back as a second copy.
//
// THE PROBLEM IS THE PROVIDER'S WALK, not a choice this unit made.
// `listrecentchat` is global and includes `src = 0` — the Official Account's own
// outbound — so a reply staged through the CRM's timeline is read back by the
// next tick, two minutes later. The core writes that reply as an activity with no
// provider id on it, so the two rows cannot meet on a natural key and both land:
// one real message, two entries on a person's timeline, and no way for a reader
// to tell which is the duplicate.
//
// The alternative was to stop capturing outbound entirely. It was rejected: an
// Official Account is answered from Zalo's own console too, and those replies are
// exactly the conversation history a CRM exists to hold — dropping all of them to
// keep one duplicate away would make every timeline one-sided.

import (
	"context"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// sentTable is this unit's second table, schema-qualified for the reason the
// first one is: the ext schema is on no search_path the app connects with.
const sentTable = "ext." + sentEntity

// sentEntity is what the ledger would call the table. Nothing records against it
// today — a marker is bookkeeping about a message whose real record is the
// activity the core already wrote — but the name is derived here so a future
// ledger row cannot invent a second spelling.
const sentEntity = "ext_zalo_oa_sent_message"

// sentMarkerLife is how long a marker is worth keeping.
//
// It is bounded by the PROVIDER rather than chosen: Zalo drops a message from
// this API after roughly nine days, so a marker older than that can never match
// anything a walk could still read. The margin over nine days is for the walk
// that is behind, not for the provider.
const sentMarkerLife = 14 * 24 * time.Hour

// rememberSent records that this installation sent one message.
//
// It is written AFTER the provider accepted the message, because a marker for a
// message that was never sent would suppress a capture of somebody else's — and
// the id it keys on is the provider's own, which only exists once the send has
// returned one.
//
// A send that returned no id records nothing, which is the honest outcome: there
// is no key to suppress on, so that one message will be captured back and will
// appear twice. Inventing a key would be worse — it would suppress a real message
// that happened to be read at the same moment.
func rememberSent(ctx context.Context, rt extension.Runtime, oaID, messageID string) error {
	if messageID == "" {
		return nil
	}
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO `+sentTable+` (workspace_id, oa_id, message_id)
			 VALUES (`+callerWorkspace+`, $1, $2)
			 ON CONFLICT (workspace_id, oa_id, message_id) DO NOTHING`, oaID, messageID)
		return err
	})
}

// sentByThisInstallation answers which of a walk's message ids this installation
// sent itself.
//
// It asks in ONE statement for the whole page rather than per message: a tick
// reads up to sixty messages, and sixty round trips to answer a bookkeeping
// question would cost more than the capture they guard.
func sentByThisInstallation(ctx context.Context, rt extension.Runtime, oaID string, ids []string) (map[string]bool, error) {
	found := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return found, nil
	}
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT message_id FROM `+sentTable+`
			  WHERE oa_id = $1 AND message_id = ANY($2::text[])`, oaID, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			found[id] = true
		}
		return rows.Err()
	})
	return found, err
}

// forgetOldSentMarkers drops markers the provider can no longer serve a message
// for.
//
// It runs on the tick rather than on a schedule of its own, because the only
// thing that makes a marker worth keeping is a walk that might still read the
// message — and the walk is here.
func forgetOldSentMarkers(ctx context.Context, rt extension.Runtime) error {
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM `+sentTable+` WHERE sent_at < now() - $1::interval`,
			sentMarkerLife.String())
		return err
	})
}
