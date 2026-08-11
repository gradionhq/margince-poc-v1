// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The record links a caller NAMES, for the 🟡 verbs that attach to records
// instead of anchoring on one: a booking says what the meeting is about, an
// account-started send says which records the new conversation is filed under.
// Neither has a row handed to it, so both have to answer the same three
// questions about the list they were given — is it bounded, is it free of
// repeats, and is every record one the caller may actually reach — and they
// answer them here, once.

import (
	"context"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// RecordLink is one (record type, id) pair naming a record a staged effect
// attaches to. Comparable, so it can key the dedupe set below.
type RecordLink struct {
	EntityType string   `json:"entity_type"`
	EntityID   ids.UUID `json:"entity_id"`
}

// requireAddressee refuses a mail send that reaches nobody.
//
// One spelling for both mail verbs. The store refuses it too, at execution, so
// the point of raising it at staging is that a human is never asked to approve
// a send that was already impossible — and two copies of that sentence is how
// one of the two verbs quietly stops asking.
func requireAddressee(to []string) error {
	if len(to) > 0 {
		return nil
	}
	return &BadArgsError{Cause: errors.New(
		"`to` is empty; a send with no addressee reaches nobody and would be refused after approval")}
}

// maxRecordLinks bounds how many records one call may attach to.
//
// This is a REQUEST BOUND, not a modelling opinion. Each link costs its own
// row-scoped provider read in its own transaction, and the array is chosen
// freely by the caller: at the 1 MiB body limit a single tools/call could
// carry ~15,000 of them, spending tens of thousands of queries against a
// 16-connection pool inside one request — before any human has approved
// anything, since staging runs on the refusal path. A meeting, or a message,
// that genuinely touches more records than this is neither.
const maxRecordLinks = 25

// uniqueRecordLinks validates and de-duplicates the links a call attaches to.
// Deduplicating matters as much as the cap: the same id repeated is the
// cheapest way to turn one call into N reads, and it is also just a caller
// mistake worth not charging for twice.
func uniqueRecordLinks(links []RecordLink) ([]RecordLink, error) {
	if len(links) > maxRecordLinks {
		return nil, &BadArgsError{Cause: fmt.Errorf(
			"a call may attach to at most %d records; this one names %d", maxRecordLinks, len(links))}
	}
	seen := make(map[RecordLink]struct{}, len(links))
	unique := make([]RecordLink, 0, len(links))
	for _, link := range links {
		if _, dup := seen[link]; dup {
			continue
		}
		seen[link] = struct{}{}
		unique = append(unique, link)
	}
	return unique, nil
}

// readStageableLinks reads every named link through the record provider and
// refuses the staging if any one of them cannot carry an approval.
//
// EVERY link, not just the one a summary happens to display. Two refusals ride
// on this read and each would otherwise mint an approval nobody can use: a
// record outside the caller's row scope answers not-found here rather than
// after a human has spent the one-shot approval on it, and a record whose
// authority lives in an external system of record has no row in our tables for
// redemption's version pin to read — the un-releasable approval
// refuseStagingElsewhere exists to prevent. A call naming one local record and
// one mirrored record is exactly the case a first-link-only check waves through.
//
// The records come back in the caller's order, so a caller that needs one of
// them — a booking pins its first link's version — reads it from the result
// rather than fetching it a second time.
func readStageableLinks(
	ctx context.Context, p datasource.SystemOfRecordProvider, links []RecordLink,
) ([]datasource.Record, error) {
	records := make([]datasource.Record, 0, len(links))
	for _, link := range links {
		rec, err := p.Read(ctx, datasource.EntityRef{
			Type: datasource.EntityType(link.EntityType), ID: link.EntityID,
		})
		if err != nil {
			return nil, err
		}
		if err := refuseStagingElsewhere(rec); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}
