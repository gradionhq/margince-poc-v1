// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The channel counterparty auto-create engine (telegram-oa design §6.4): the
// sibling of EnsureCounterparty for an inbound channel message, whose human is
// named by a provider identity rather than by an address.
//
// It is a SIBLING and not a widening because every load-bearing step of the
// mail path is mail-shaped. That path refuses an empty address, derives the
// display name from the address's local part, owns its rows through the
// granting human, writes an email satellite, derives a company from the mail
// domain, and quarantines on domain-shaped impersonation tells. A channel
// message supplies none of those inputs, and a workspace bot has no granting
// human at all: it serves the whole workspace, so the person it creates is
// OWNERLESS and workspace-visible (design D2 — a deliberate divergence from
// ADR-0063's owner-visible connector records, raised upstream, because
// attributing the record to whoever ran Connect would fill that admin's
// pipeline with conversations they never had).
//
// What it shares with the mail path is what must not fork: the ONE dedupe
// chokepoint (PO-F-1), the erasure-suppression refusal, and the activity link.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// fieldChannelIdentity names the channel key in dedupe evidence, as fieldEmail
// names the address on the mail path.
const fieldChannelIdentity = "channel_identity"

// fieldChannelUsername names the provider handle in an audit image. It is not
// a person column: it is the display handle of the person's channel account,
// and the audit trail says so rather than pretending a person field moved.
const fieldChannelUsername = "channel_username"

// EnsureChannelCounterpartyInput is one inbound channel message's counterparty.
// There is no OwnerID and no Domain: the created person is ownerless by design,
// and this path derives no employer even when an address rides along — reading
// one out of a mail domain is the mail ladder's job, which this path bypasses.
type EnsureChannelCounterpartyInput struct {
	Identity    connector.ChannelIdentity
	DisplayName string // the provider's own name for the sender — untrusted text
	// CorroboratingEmail is the sender's address, where the provider knew one
	// and its source declared the email merge key. It NAMES nobody: Identity
	// does that, and the ladder below prefers it. What this buys is the human
	// already captured from mail being recognised as the same human, instead of
	// becoming a second record nobody notices.
	//
	// Empty for every transport that holds no address, which is every core
	// channel connector.
	CorroboratingEmail string

	ActivityID ids.ActivityID // the captured activity to link
	Source     string         // provenance channel, e.g. "telegram:<bot>:<chat>:<msg>"
	CapturedBy string         // "connector:<provider>"
}

// EnsureChannelCounterpartyResult reports what the ensure did. It carries no
// organization fields, unlike its mail sibling: this path never derives a
// company, so there would be nothing honest to put in them.
type EnsureChannelCounterpartyResult struct {
	PersonID       ids.PersonID
	PersonCreated  bool
	DedupeRecorded bool
	// Conflict is non-nil only when a later exact lane named a different
	// person than the one routing chose (dedupe.go's routeExact). It is a
	// REPORT the caller decides what to do with (design §7.3/D8) — this
	// store already wrote nothing onto the rival, and routing already
	// happened; raising the identity review is the caller's job precisely so
	// a failure to raise it can never fail this ensure.
	Conflict *LaneConflict
}

// EnsureChannelCounterparty resolves-or-creates the person behind one inbound
// channel message, binds the channel identity to them, refreshes the handle the
// provider reported, and links the activity. Idempotent by construction: the
// identity lane lands every later message from the same sender on the same row,
// and the link insert is conflict-free on replay.
func (s *Store) EnsureChannelCounterparty(ctx context.Context, in EnsureChannelCounterpartyInput) (EnsureChannelCounterpartyResult, error) {
	if in.Identity.Provider == "" || in.Identity.ChannelUserID == "" {
		return EnsureChannelCounterpartyResult{},
			errors.New("people: a channel counterparty needs both a provider and a channel user id")
	}
	// Normalized once, here, because four things downstream key on this address
	// and they must agree on what "the same address" is: the subject lock, the
	// suppression probe, the resolution ladder, and the row written onto the
	// person. A provider that pads or capitalizes would otherwise mint a
	// person_email row no later mail capture can match — the duplicate this path
	// exists to prevent, reintroduced by whitespace.
	in.CorroboratingEmail = normalizeEmail(in.CorroboratingEmail)
	var res EnsureChannelCounterpartyResult
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		res, err = s.ensureChannelCounterpartyTx(ctx, tx, in)
		return err
	})
	if err != nil {
		return EnsureChannelCounterpartyResult{}, err
	}
	return res, nil
}

// ensureChannelCounterpartyTx is the whole decision on one transaction: the
// person, the binding, the handle refresh and the link commit together, so a
// message never leaves behind a person nobody is bound to or a binding no
// message points at.
func (s *Store) ensureChannelCounterpartyTx(ctx context.Context, tx pgx.Tx, in EnsureChannelCounterpartyInput) (EnsureChannelCounterpartyResult, error) {
	// The probe below is a READ COMMITTED read followed by a dependent write, so
	// it needs the lock the eraser holds across its whole purge: without it this
	// transaction can pass the probe and then bind a LIVE identity to a new
	// person while the erasure destroys the old one. uq_person_channel_identity
	// is partial on archived_at IS NULL, so nothing in the database refuses that
	// second binding — and the erasure writes by person_id, so the rival it
	// never named keeps the erased human's account and name for good.
	if err := storekit.LockSubjectKeys(ctx, tx,
		[]storekit.ChannelIdentityKey{
			{Provider: in.Identity.Provider, ChannelUserID: in.Identity.ChannelUserID},
		},
		// The address is locked too, and it is the half that covers the subject
		// this path can otherwise reach mid-erasure: a human known only from
		// mail holds no channel account, so an eraser locking accounts alone
		// takes no lock, and the bind below would land inside their purge.
		correspondingEmails(in.CorroboratingEmail),
	); err != nil {
		return EnsureChannelCounterpartyResult{}, err
	}

	// A13: deletion sticks on the channel key exactly as it does on an address.
	// This is EnsureCounterpartyTx's EmailSuppressed probe in its channel
	// spelling — an erased subject whose only identifier was a Telegram id must
	// not be re-created by their next message.
	suppressed, err := s.channelSubjectSuppressed(ctx, tx, in)
	if err != nil {
		return EnsureChannelCounterpartyResult{}, err
	}
	if suppressed {
		return EnsureChannelCounterpartyResult{}, ErrCounterpartySuppressed
	}

	var res EnsureChannelCounterpartyResult
	if err := s.resolveChannelPerson(ctx, tx, in, &res); err != nil {
		return EnsureChannelCounterpartyResult{}, err
	}
	if err := refreshChannelUsername(ctx, tx, in.Identity); err != nil {
		return EnsureChannelCounterpartyResult{}, err
	}
	if err := s.linkActivityToPerson(ctx, tx, in.ActivityID, res.PersonID); err != nil {
		return EnsureChannelCounterpartyResult{}, err
	}
	return res, nil
}

// channelSubjectSuppressed asks both keys the subject can have been erased by.
//
// The channel key is the one this path always has. The address is asked only
// when the record carries one, and asking it is not optional politeness: an
// erasure keyed on an address would otherwise be undone by the subject's next
// direct message, which arrives naming them by an account the suppression list
// has never heard of. A13 says deletion sticks; it has to stick on every key the
// record can be recognised by, not on the one this path happened to start with.
func (s *Store) channelSubjectSuppressed(ctx context.Context, tx pgx.Tx, in EnsureChannelCounterpartyInput) (bool, error) {
	suppressed, err := storekit.ChannelIdentitySuppressed(ctx, tx, in.Identity.Provider, in.Identity.ChannelUserID)
	if err != nil || suppressed {
		return suppressed, err
	}
	if in.CorroboratingEmail == "" {
		return false, nil
	}
	return storekit.EmailSuppressed(ctx, tx, in.CorroboratingEmail)
}

// resolveChannelPerson runs PO-F-1 over the channel identity and, when nothing
// resolves, offers a new person — speculatively, because the bind is the
// arbiter of the identity race, not this lookup.
func (s *Store) resolveChannelPerson(ctx context.Context, tx pgx.Tx, in EnsureChannelCounterpartyInput, res *EnsureChannelCounterpartyResult) error {
	if err := auth.Require(ctx, entityPerson, principal.ActionCreate); err != nil {
		return err
	}
	name := channelCounterpartyName(in.DisplayName, in.Identity)
	match, err := DedupePerson(ctx, tx, channelCandidate(in, name))
	if err != nil {
		return err
	}
	if match.Decision == DecisionExactCollision && match.MatchedLane == LaneEmail {
		// The address found a human the channel lane could not, which means
		// they exist from mail capture and hold no binding for this account
		// yet. Routing alone would leave them unbound — unreachable for a
		// reply, invisible to a channel-keyed erasure, and re-resolved by
		// address on every later message. Adopting them is what makes the two
		// captures one person rather than one person and one alias.
		return s.adoptEmailRoutedIncumbent(ctx, tx, in, match, res)
	}
	if match.Decision == DecisionExactCollision {
		// A live binding already names this human; every later message from the
		// same sender takes this branch. Conflict rides along unread here —
		// routing is this function's whole job, and design §7.3/D8 puts the
		// identity-review decision one layer up, where a failure to raise it
		// can be logged without risking the routing this message needs.
		//
		// A corroborating address is deliberately not written onto this person.
		// The binding already names them, so the address buys no routing, and
		// writing it per message would need its own add-if-absent guard and its
		// own audit gate to avoid claiming a change on every message that
		// changed nothing. A person bound here but holding no address is one
		// minted before their source vouched for one; the ladder still reads the
		// address on every message, so nothing is lost that a later mail capture
		// would not also supply.
		res.PersonID = match.PersonID
		res.Conflict = match.Conflict
		return nil
	}

	// Nothing resolved, so a person has to be offered — inside a savepoint,
	// because a concurrent first message from the same sender may be creating
	// one too. ResolveOrCreateChannelIdentity settles that in the database
	// (channelidentity.go), and the loser's own person row, its audit entry and
	// its outbox event must leave no trace, or one human ends up on two records
	// with the conversation on only one of them.
	speculative, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("people: opening the speculative channel-person savepoint: %w", err)
	}
	offered, winner, recorded, err := s.offerChannelPerson(ctx, speculative, in, name, match)
	if err != nil {
		if rbErr := speculative.Rollback(ctx); rbErr != nil {
			// Without a clean rollback the enclosing transaction stays poisoned,
			// so nothing can commit — report both faults, hide neither.
			return errors.Join(err, rbErr)
		}
		return err
	}
	if winner != offered {
		if err := speculative.Rollback(ctx); err != nil {
			return fmt.Errorf("people: withdrawing the speculative channel person: %w", err)
		}
		res.PersonID = winner
		return nil
	}
	if err := speculative.Commit(ctx); err != nil {
		return fmt.Errorf("people: committing the channel person: %w", err)
	}
	res.PersonID, res.PersonCreated, res.DedupeRecorded = offered, true, recorded
	return nil
}

// offerChannelPerson writes the ownerless person, binds the identity to it, and
// reports which person the binding actually named — id when this call won, the
// incumbent when a concurrent first message got there first. A fuzzy near-match
// still creates (DEDUPE_FUZZY_AUTOMERGE is pinned never) and records the pair
// for the review queue, so a channel-created twin is as visible as a mail one.
// It reports the person it offered as well as the winner, because the caller
// tells a won race from a lost one by comparing the two.
func (s *Store) offerChannelPerson(ctx context.Context, tx pgx.Tx, in EnsureChannelCounterpartyInput, name string, match PersonResolution) (offered, winner ids.PersonID, recorded bool, err error) {
	// owner_id is left NULL and visibility is 'workspace' (design D2): the
	// bot serves the workspace, and auth's owner predicate already treats an
	// ownerless row as workspace-shared at every row-scope tier.
	//
	// quarantined_at is left NULL, and not because impersonation does not
	// happen here: both tells quarantineSuspect reads — a punycode domain, a
	// display name naming a DIFFERENT domain than the message arrived from —
	// are statements about a mail domain this record does not have.
	id, err := createPerson(ctx, tx, match, PersonSpec{
		FullName: name,
		// The address the provider vouched for, written with the person rather
		// than after it, so the person-create audit and event cover it — the
		// mail path's own shape. An addressless record is one the next mail from
		// this human cannot match, so it mints a second one.
		Emails:     channelPersonEmails(in.CorroboratingEmail),
		Visibility: visibilityWorkspace,
		Source:     in.Source,
		CapturedBy: in.CapturedBy,
	})
	if err != nil {
		return ids.PersonID{}, ids.PersonID{}, false, err
	}
	auditID, err := storekit.Audit(ctx, tx, "create", entityPerson, id.UUID, nil, map[string]any{fieldFullName: name})
	if err != nil {
		return ids.PersonID{}, ids.PersonID{}, false, err
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventPersonCreated{FullName: name}); err != nil {
		return ids.PersonID{}, ids.PersonID{}, false, err
	}
	bound, err := ResolveOrCreateChannelIdentity(ctx, tx, id, in.Identity)
	if err != nil {
		return ids.PersonID{}, ids.PersonID{}, false, err
	}
	if bound != id {
		return id, bound, false, nil
	}
	recorded, err = s.recordChannelDedupeCandidate(ctx, tx, in, name, id, match)
	if err != nil {
		return ids.PersonID{}, ids.PersonID{}, false, err
	}
	return id, id, recorded, nil
}

// channelPersonEmails is the address list a minted channel person starts with:
// the corroborating address when there is one, and nothing at all when there is
// not. It is primary because it is the only one — a person with a single address
// and no primary reads as a person nobody can be sure how to write to.
func channelPersonEmails(email string) []PersonEmailInput {
	if email == "" {
		return nil
	}
	// Vouched, not corresponded: the provider's directory says this is their
	// address, and nobody here has ever exchanged mail with it. Recording that
	// is what keeps one direct message from a stranger out of the mail ladder's
	// verdict, where it would mark the address a known counterparty for good and
	// switch off the noise sweep for it permanently.
	return []PersonEmailInput{{
		Email: email, EmailType: emailTypeWork, IsPrimary: true,
		VouchedNotCorresponded: true,
	}}
}

// recordChannelDedupeCandidate stores the fuzzy pair the review queue renders,
// with the detection-time snapshot of the incumbent (DH-N-8) — never re-derived
// later. A decision other than fuzzy review records nothing.
func (s *Store) recordChannelDedupeCandidate(ctx context.Context, tx pgx.Tx, in EnsureChannelCounterpartyInput, name string, id ids.PersonID, match PersonResolution) (bool, error) {
	if match.Decision != DecisionFuzzyReview {
		return false, nil
	}
	var incumbentName string
	if err := tx.QueryRow(ctx, `SELECT full_name FROM person WHERE id = $1`, match.PersonID).Scan(&incumbentName); err != nil {
		return false, fmt.Errorf("people: reading dedupe incumbent: %w", err)
	}
	evidence := []map[string]any{
		{evidenceFieldKey: fieldFullName, evidenceLeftKey: name, evidenceRightKey: incumbentName, evidenceSignalKey: evidenceSignalCollide, evidenceScoreKey: match.Confidence},
		{evidenceFieldKey: fieldChannelIdentity, evidenceLeftKey: channelIdentityKey(in.Identity), evidenceRightKey: nil, evidenceSignalKey: evidenceSignalOneSided},
	}
	return recordDedupeCandidate(ctx, tx, entityPerson, id.UUID, match.PersonID.UUID, match.Confidence,
		evidence, in.Source, in.CapturedBy)
}

// refreshChannelUsername keeps the binding's handle current — design §4.2 makes
// it display data refreshed by every inbound message, because a human can
// rename themselves on the provider at any time and the stored copy would
// otherwise be whatever they were called the day they first wrote.
//
// It is a write of its own rather than an ON CONFLICT DO UPDATE arm on the
// bind: the bind's ZERO row count is what tells a caller it lost the identity
// race, and an update arm would report a row and destroy that signal.
//
// A sender who has no handle at all — Telegram accounts need none — reports an
// empty one, and clearing the stored value is the honest answer: keeping a
// handle its owner has dropped would show a name nobody answers to. Comparing
// against the stored handle makes the ordinary unchanged case touch no row, so
// a conversation does not bump version and updated_at on every message — and
// leaves no audit row either, which is why the trail records handle CHANGES
// rather than one line per inbound message.
//
// The prior handle is read under FOR UPDATE and compared in Go. The tempting
// one-statement shape — an UPDATE joined to a subquery holding the old value —
// cannot answer this correctly: the subquery is a NON-target read, so it stays
// on the statement-start snapshot even when the UPDATE itself blocked and
// resumed against a newer row version. Two messages from a renaming sender are
// in flight at once often enough for that to bite, and the second would then
// audit a handle the account had two writes ago and re-publish a rename the
// first already published. The extra round trip buys the one thing this trail
// exists for — saying what actually changed. (RETURNING OLD.* would answer
// both in one statement; PG16, which this deployment pins, has no such thing.)
func refreshChannelUsername(ctx context.Context, tx pgx.Tx, ci connector.ChannelIdentity) error {
	var boundID ids.UUID
	var personID ids.PersonID
	var previous *string
	err := tx.QueryRow(ctx, `
		SELECT id, person_id, username FROM person_channel_identity
		 WHERE provider = $1 AND channel_user_id = $2 AND archived_at IS NULL
		 FOR UPDATE`,
		ci.Provider, ci.ChannelUserID).Scan(&boundID, &personID, &previous)
	if errors.Is(err, pgx.ErrNoRows) {
		// Nothing is bound to this identity yet, which the bind above this call
		// is what fixes.
		return nil
	}
	if err != nil {
		return fmt.Errorf("people: reading the channel identity handle: %w", err)
	}
	current := emptyToNil(ci.Username)
	if sameHandle(previous, current) {
		// The ordinary case, every message after the first.
		return nil
	}
	if _, err := tx.Exec(ctx,
		`UPDATE person_channel_identity SET username = $2 WHERE id = $1`, boundID, current); err != nil {
		return fmt.Errorf("people: refreshing the channel identity handle: %w", err)
	}
	return auditChannelIdentityChange(ctx, tx, personID,
		handleImage(ci.Provider, previous), handleImage(ci.Provider, current))
}

// sameHandle compares two handles with "no handle at all" as ONE state rather
// than two: emptyToNil has already collapsed the empty string the provider
// reports for a handle-less account into the NULL the column stores.
func sameHandle(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// handleImage is the audit/event field image for a handle refresh, provider
// included for the same reason reachabilityImage carries it. A nil handle is
// the honest image of an account that has none.
func handleImage(provider string, handle *string) map[string]any {
	return map[string]any{fieldChannelUsername: map[string]any{
		"provider": provider, "username": handle,
	}}
}

// emptyToNil mirrors the columns' NULLIF: absent is one state, not two. Shared
// by the channel handle and the organization name columns, both of which read
// NULL as "not set" and would otherwise grow a second, silent spelling of it.
func emptyToNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// channelCounterpartyName is the display name we can honestly store for a
// channel sender: the provider's own name for them, else their handle, else the
// channel key itself — never empty (person pins full_name NOT NULL). There is
// no address here whose local part could serve as the fallback.
func channelCounterpartyName(displayName string, ci connector.ChannelIdentity) string {
	if name := strings.TrimSpace(displayName); name != "" {
		return name
	}
	if handle := strings.TrimSpace(ci.Username); handle != "" {
		return "@" + handle
	}
	return channelIdentityKey(ci)
}

// channelIdentityKey renders the identity as the one string a human reads in
// evidence and a fallback name — the same provider:channel_user_id shape the
// suppression hash keys on, and deliberately without the bot id for the same
// reason (Telegram user ids are global).
func channelIdentityKey(ci connector.ChannelIdentity) string {
	return ci.Provider + ":" + ci.ChannelUserID
}
