// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// Where a captured row says it came from.
//
// Four small derivations, together because they answer one question and are
// read as a set: which channel, which connector, and which human behind it.
// Every one of them derives from the AUTHENTICATED principal rather than from
// the record a connector handed over — provenance a caller can assert is
// provenance a caller can forge.

import (
	"context"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// captureSource is the provenance channel column value; the natural
// key's system is the honest channel name.
func captureSource(rec connector.NormalizedRecord) string {
	if rec.Source != "" {
		return rec.Source
	}
	return rec.NaturalKey.SourceSystem
}

// connectorPrincipalID renders the audit identity for a connector.
func connectorPrincipalID(name string) string {
	return "connector:" + strings.TrimPrefix(name, "connector:")
}

// connectorProvenance is what a captured activity's captured_by records: the
// connector AND the mailbox owner behind it, `connector:gmail:<user>`.
//
// The connector alone was not enough to say anything useful. Two colleagues
// who have both connected Gmail produce rows stamped identically, so nothing
// downstream could tell whose mailbox a message came from — the provenance
// named the software rather than the person, and any later attempt to
// attribute history had to guess or decline.
//
// It is derived from the authenticated principal, never from the record the
// connector handed us: provenance a caller can assert is provenance a caller
// can forge. A principal carrying no granting user falls back to the bare
// connector id, which is the honest answer for a connection with no human
// behind it.
func connectorProvenance(actor principal.Principal) string {
	if actor.UserID == ids.Nil {
		return actor.ID
	}
	return actor.ID + ":" + actor.UserID.String()
}

// capturedByFor is the provenance stamped on a captured activity: the acting
// connector plus the mailbox owner behind it. It falls back to the record's
// own value only when no actor is bound, which the sink has already refused
// by the time this runs — the fallback exists so a future caller cannot get a
// blank provenance out of a missing principal.
func capturedByFor(ctx context.Context, rec connector.NormalizedRecord) string {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return rec.CapturedBy
	}
	return connectorProvenance(actor)
}
