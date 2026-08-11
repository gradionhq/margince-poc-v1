// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

// The readable half of the send gate (ADR-0098 D6).
//
// It answers per purpose and channel, with the reason in the reader's words, so
// a composer can show the verdict before a word is typed. Two things about it
// are load-bearing:
//
// It is computed by VerdictForPerson — the SAME code the dispatcher runs at
// transmit — so a preview and the check that fires at send cannot drift.
//
// It never grants anything. The transmit-time recheck stays authoritative and
// refuses with the newer answer when state changed after the drawer opened; a
// guard read that had authority would be a way to freeze a verdict and outrun a
// withdrawal.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// GetPersonConsentGuard implements GET /people/{id}/consent/guard.
func (h Handlers) GetPersonConsentGuard(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	guard, err := h.store.PersonConsentGuard(r.Context(), pathID[ids.PersonKind](id))
	if err != nil {
		writeConsentErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, guard)
}

// PersonConsentGuard answers, for every configured purpose, whether an outbound
// message to this person is allowed right now.
//
// Channel is derived from the class rather than multiplied across it: asking
// "may I phone them for the newsletter purpose" is not a question anybody has,
// and a matrix full of combinations nobody sends would bury the two rows a rep
// actually reads.
func (s *Store) PersonConsentGuard(ctx context.Context, personID ids.PersonID) (crmcontracts.PersonConsentGuard, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return crmcontracts.PersonConsentGuard{}, err
	}
	out := crmcontracts.PersonConsentGuard{
		PersonId: openapi_types.UUID(personID.UUID),
		Entries:  []crmcontracts.PersonConsentGuardEntry{},
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Anything that names a record is gated: answering about a person the
		// caller cannot read would confirm that person exists.
		if err := auth.EnsureVisibleLive(ctx, tx, "person", personID.UUID); err != nil {
			return err
		}
		purposes, err := PurposesForGuard(ctx, tx)
		if err != nil {
			return err
		}
		for _, purpose := range purposes {
			verdict, err := VerdictForPerson(ctx, tx, personID.String(), purpose)
			if err != nil {
				return err
			}
			entry, err := wireGuardEntry(purpose, verdict)
			if err != nil {
				return err
			}
			out.Entries = append(out.Entries, entry)
		}
		return nil
	})
	if err != nil {
		return crmcontracts.PersonConsentGuard{}, err
	}
	return out, nil
}

func wireGuardEntry(purpose PurposeRow, verdict Verdict) (crmcontracts.PersonConsentGuardEntry, error) {
	label := purpose.Label
	entry := crmcontracts.PersonConsentGuardEntry{
		PurposeKey:   purpose.Key,
		PurposeLabel: &label,
		PurposeClass: crmcontracts.PersonConsentGuardEntryPurposeClass(purpose.Class),
		Channel:      channelFor(purpose.Class),
		Verdict:      crmcontracts.PersonConsentGuardEntryVerdict(verdict.State),
		Reason:       verdict.Reason,
	}
	if verdict.Qualifying != nil {
		qualifying, err := wireQualifying(*verdict.Qualifying)
		if err != nil {
			return crmcontracts.PersonConsentGuardEntry{}, err
		}
		entry.QualifyingEvent = qualifying
	}
	return entry, nil
}

// channelFor names the channel a purpose is actually sent on. Only the phone
// class is not mail, and nothing sends on it yet.
func channelFor(class Class) crmcontracts.PersonConsentGuardEntryChannel {
	if class == ClassPhoneOutreach {
		return crmcontracts.PersonConsentGuardEntryChannelPhone
	}
	return crmcontracts.PersonConsentGuardEntryChannelEmail
}

func wireQualifying(event QualifyingEvent) (*crmcontracts.ConsentQualifyingEvent, error) {
	wired := crmcontracts.ConsentQualifyingEvent{
		Kind:       crmcontracts.ConsentQualifyingEventKind(event.Kind),
		OccurredAt: event.OccurredAt,
	}
	if event.SourceEntityType != "" {
		sourceType := crmcontracts.ConsentQualifyingEventSourceEntityType(event.SourceEntityType)
		wired.SourceEntityType = &sourceType
	}
	if event.SourceEntityID != "" {
		parsed, err := ids.Parse(event.SourceEntityID)
		if err != nil {
			// The column is a uuid and the DDL requires it beside its type, so
			// an unparseable value means the row is not what the schema says it
			// is. Rendering the verdict without its proof would present an
			// unevidenced "allowed" as an evidenced one.
			return nil, fmt.Errorf("the qualifying event names an unreadable source record: %w", err)
		}
		sourceID := openapi_types.UUID(parsed)
		wired.SourceEntityId = &sourceID
	}
	if event.Note != "" {
		note := event.Note
		wired.Note = &note
	}
	return &wired, nil
}
