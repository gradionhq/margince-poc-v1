// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The ingress seam — what a unit hands the core when it pulls a record out of
// a provider — is part of the published extension surface.
//
//margince:extension-surface

package extension

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// RecordKind is which kind of record a unit lands. It is the vocabulary a unit
// DECLARES in IngressSource.Lands and the vocabulary the core gates a call
// against, so a unit that declared one kind cannot land another.
type RecordKind string

// KindActivity is a captured interaction bound for the shared timeline.
//
// It is the only kind today, and the absence of a lead kind is deliberate
// rather than pending: the sink's lead arm returns before the row is written
// when a captured lead collides with an existing one, so the natural key never
// enters the table and the collision restages on every poll. Publishing a kind
// whose idempotency does not hold would be offering a unit a guarantee this
// side cannot keep.
const KindActivity RecordKind = "activity"

// IngressSource is one provider a unit brings records in from: the unit's own
// stable key for it, and which kinds it lands.
//
// It is inert data, exactly like SecretsRequest — declaring a source reaches no
// provider and lands nothing. What it buys is that `source_system` is derived
// from a DECLARED value rather than from whatever a call passes, so a typo is a
// refusal instead of a silently-minted second provenance namespace, and an
// operator reading manifest.generated.json can see which units reach core
// capture before one of them runs.
type IngressSource struct {
	// System is the unit's own key for the provider, lower snake/kebab, and
	// stable: it becomes half of every landed record's natural key, so changing
	// it re-lands the unit's whole history under a new identity.
	System string

	// Lands are the record kinds this source produces. Empty is invalid rather
	// than meaning "all": a declaration an operator reads must say what it does.
	//
	// One kind is landable today (KindActivity) and a Record carries exactly
	// one payload, so this list currently states what the call could not
	// contradict anyway. It is the growth point: the second kind arrives as a
	// second Record field, and the gate that a unit may land only what it
	// declared arrives with it, in the core.
	Lands []RecordKind
}

// systemGrammar bounds the declared system key to what can sit inside a
// provenance string and a natural key without quoting: lower-case alphanumeric
// segments joined by single hyphens, the same shape a unit name takes.
var systemGrammar = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// maxSystemLength bounds the declared key's share of the stored provenance
// string. `ext:<unit>:<system>` is written into activity.source, which every
// timeline read carries, and a unit name is already capped at 32.
const maxSystemLength = 32

// Validate enforces what a source must state to be usable. It is the published
// check the manifest generator and the boot preflight both run, so generation
// time and boot time cannot disagree about which declarations are legal.
func (s IngressSource) Validate() error {
	switch {
	case strings.TrimSpace(s.System) == "":
		return errors.New("extension: a declared ingress source has an empty system key")
	case len(s.System) > maxSystemLength:
		return fmt.Errorf("extension: ingress system %q is %d characters — it is stamped into every landed record's provenance, so it is capped at %d",
			s.System, len(s.System), maxSystemLength)
	case !systemGrammar.MatchString(s.System):
		return fmt.Errorf("extension: ingress system %q is not a system key (lower-case [a-z0-9] segments joined by single hyphens)", s.System)
	case len(s.Lands) == 0:
		return fmt.Errorf("extension: ingress source %q declares no record kinds — a declaration an operator reads must say what it lands", s.System)
	}
	for _, kind := range s.Lands {
		if kind != KindActivity {
			return fmt.Errorf("extension: ingress source %q declares record kind %q — the only kind an ingress may land is %q",
				s.System, string(kind), string(KindActivity))
		}
	}
	return nil
}

// Message direction, relative to the member whose connection produced the
// record. Restated here rather than taken from the core's connector port,
// because the published surface may not export a type from beneath it.
const (
	DirectionInbound  = "inbound"
	DirectionOutbound = "outbound"
)

// Counterparty is the party on the other side of one captured message.
//
// It is identified by EMAIL, and that is the whole of the identity this surface
// offers. The core also knows a channel-identity shape for providers that have
// no address at all, and it is deliberately absent here: it keys a core table
// whose provider vocabulary is a closed set, so a unit reaching it would be an
// extension name entering a list the core maintains.
//
// DisplayName is untrusted text — whatever the provider says the human calls
// themselves — and is bounded and stripped by the core before it is stored.
type Counterparty struct {
	Email       string
	DisplayName string
	// Domain is the lower-cased mail domain. It is what the core's own
	// suppression gates key on, and a unit that leaves it empty is not opting
	// out of them — it is failing to answer, which those gates read as "keep".
	Domain string
	// Direction is relative to the connected member: a message they received is
	// inbound. A record with no honest direction leaves it empty.
	Direction string
}

// ActivityFields is a captured interaction bound for the timeline. It mirrors
// the core's own shape; a fitness test outside this package compares the two so
// a field added there cannot go unnoticed here.
type ActivityFields struct {
	// Kind is the timeline kind — the core admits a closed set, and a value
	// outside it is refused rather than coerced.
	Kind string
	// Subject and Body are the message as a human reads it. Both are bounded
	// (see the Max* constants): a provider is a remote party, and an unbounded
	// field is that party choosing how much this installation stores.
	Subject string
	Body    string
	// OccurredAt is when the message happened at the PROVIDER, not when the
	// poll saw it — a timeline ordered by discovery is a timeline of this
	// system's own scheduling.
	OccurredAt time.Time
	Direction  string
}

// The bounds every ingested record is held to, applied before any transaction
// opens. They are caps on what a REMOTE party may cause this installation to
// store per message, which is why they sit on the published surface rather than
// only in the adapter: a unit can check them itself and skip a doomed call.
const (
	// MaxRawBytes caps the provider's original record. Raw capture is evidence
	// and is written on every landed record, so this is the largest single
	// contribution one message makes to storage.
	MaxRawBytes = 256 * 1024
	// MaxKeyLength caps each half of the natural key. The key is indexed and
	// read back on every replay.
	MaxKeyLength = 256
	// MaxSubjectRunes caps a subject line — generous next to any subject a
	// human writes, small next to anything that would hurt a list rendering it.
	MaxSubjectRunes = 500
	// MaxBodyRunes caps a message body.
	MaxBodyRunes = 32768
	// MaxAddresses caps how many parties one record may enumerate.
	MaxAddresses = 64
	// MaxAddressLength is RFC 5321's ceiling: 64 octets of local part, an @,
	// and 255 of domain.
	MaxAddressLength = 320
	// MaxDisplayNameRunes caps untrusted display text.
	MaxDisplayNameRunes = 200
	// MaxThreadKeyLength caps the conversation key, which is stored on the
	// activity and joined against.
	MaxThreadKeyLength = 512
)

// Record is one provider record on its way into the CRM.
//
// WHAT IT DELIBERATELY DOES NOT CARRY, because each absence is a decision:
//
//   - No Source and no CapturedBy. Both are stamped by the core from the
//     invoking unit's own identity, so a unit cannot attribute its records to
//     another unit or to a core connector. They are absent rather than
//     validated: a field a caller may not set, that the caller can still set,
//     is a validation rule somebody eventually relaxes.
//   - No Links. A record naming the core rows it attaches to would make the
//     core's link-visibility probe a per-row existence oracle over the scope
//     the ingest runs under. What a message is about is decided by the core's
//     own counterparty resolution, from the addresses below.
type Record struct {
	// System names which of the unit's declared ingress sources this came from.
	System string

	// Key is the provider's own identifier for this record — the half of the
	// natural key the unit supplies.
	//
	// It MUST be something the provider reports identically on a re-read. That
	// is the one obligation here a unit can silently get wrong: a key derived
	// from a timestamp, a page position, or the unit's own row id produces a
	// second copy of the record on every poll, and nothing fails.
	Key string

	// Activity is the record itself. One field rather than a kind-tagged union
	// because one kind is landable today (see KindActivity); a second kind
	// arrives as a second field plus a rule that exactly one is set, which
	// leaves every unit written against this compiling.
	Activity ActivityFields

	// ThreadKey is the conversation this belongs to, and it MUST be namespaced
	// by the provider — a bare provider id shares one column with every other
	// source, where two of them can collide and join a stranger's conversation
	// onto this one.
	ThreadKey string

	// Counterparty is the other end of the exchange.
	Counterparty Counterparty

	// Addresses is EVERY party this record names, including the connected
	// member's own.
	//
	// It is not optional in practice, and the reason is easy to get wrong: the
	// core decides whether a message is purely internal — colleagues talking,
	// which is not evidence of a customer relationship — by asking whether
	// every party is on the installation's own domains. An EMPTY set is read as
	// "this connector could not enumerate the parties", which is a different
	// statement from "there were none", and it fails toward keeping the
	// message. So a unit that leaves this empty has not opted out of that gate;
	// it has silently disabled it.
	Addresses []string

	// Raw is the provider's record as received, kept as evidence.
	Raw []byte
}

// Validate checks everything decidable without knowing which unit is calling.
// Whether System is one the CALLER declared, and whether the caller may ingest
// at all, are the core's checks — made at the port against the invocation, for
// the same reason a Change's entity namespace is.
func (r Record) Validate() error {
	if err := r.validateKey(); err != nil {
		return err
	}
	if err := r.Activity.validate(); err != nil {
		return err
	}
	if err := r.validateAddresses(); err != nil {
		return err
	}
	if err := r.Counterparty.validate(); err != nil {
		return err
	}
	if len(r.ThreadKey) > MaxThreadKeyLength {
		return fmt.Errorf("extension: the thread key is %d bytes, over the %d-byte cap", len(r.ThreadKey), MaxThreadKeyLength)
	}
	if len(r.Raw) > MaxRawBytes {
		return fmt.Errorf("extension: the raw record is %d bytes, over the %d-byte cap every landed record pays for", len(r.Raw), MaxRawBytes)
	}
	return nil
}

func (r Record) validateKey() error {
	switch {
	case strings.TrimSpace(r.System) == "":
		return errors.New("extension: the record names no ingress system")
	case strings.TrimSpace(r.Key) == "":
		return errors.New("extension: the record carries no key — unkeyed capture cannot be idempotent, so a replay would land a second copy")
	case len(r.Key) > MaxKeyLength:
		return fmt.Errorf("extension: the record key is %d bytes, over the %d-byte cap", len(r.Key), MaxKeyLength)
	}
	return nil
}

func (r Record) validateAddresses() error {
	if len(r.Addresses) > MaxAddresses {
		return fmt.Errorf("extension: the record names %d addresses, over the cap of %d", len(r.Addresses), MaxAddresses)
	}
	for _, addr := range r.Addresses {
		if len(addr) > MaxAddressLength {
			return fmt.Errorf("extension: an address is %d bytes, over the %d-byte cap", len(addr), MaxAddressLength)
		}
	}
	return nil
}

func (c Counterparty) validate() error {
	if len(c.Email) > MaxAddressLength {
		return fmt.Errorf("extension: the counterparty address is %d bytes, over the %d-byte cap", len(c.Email), MaxAddressLength)
	}
	if utf8.RuneCountInString(c.DisplayName) > MaxDisplayNameRunes {
		return fmt.Errorf("extension: the counterparty display name is over the %d-rune cap", MaxDisplayNameRunes)
	}
	if c.Direction != "" && c.Direction != DirectionInbound && c.Direction != DirectionOutbound {
		return fmt.Errorf("extension: %q is not a direction (%q, %q, or empty for a record with no honest direction)",
			c.Direction, DirectionInbound, DirectionOutbound)
	}
	return nil
}

func (a ActivityFields) validate() error {
	if strings.TrimSpace(a.Kind) == "" {
		return errors.New("extension: the activity names no kind")
	}
	if utf8.RuneCountInString(a.Subject) > MaxSubjectRunes {
		return fmt.Errorf("extension: the subject is over the %d-rune cap", MaxSubjectRunes)
	}
	if utf8.RuneCountInString(a.Body) > MaxBodyRunes {
		return fmt.Errorf("extension: the body is over the %d-rune cap", MaxBodyRunes)
	}
	if a.OccurredAt.IsZero() {
		return errors.New("extension: the activity has no occurred-at — a timeline ordered by when a poll noticed is a timeline of this system's own scheduling")
	}
	if a.Direction != "" && a.Direction != DirectionInbound && a.Direction != DirectionOutbound {
		return fmt.Errorf("extension: %q is not a direction (%q, %q, or empty)", a.Direction, DirectionInbound, DirectionOutbound)
	}
	return nil
}

// Disposition is what became of an ingested record. It exists because a row is
// not the only success: the core drops a wholly-internal message on purpose,
// committing a breadcrumb that says so, and a seam that reported that as a
// failure would have its caller retry a deliberate drop forever.
type Disposition string

const (
	// DispositionAccepted is a record that is now in the CRM, and the Ref names
	// it.
	//
	// It deliberately does NOT distinguish a row this call created from one a
	// previous call already had. The pipeline does not report that difference —
	// an idempotent upsert answers with the same reference either way — so a
	// published "replayed" would be a promise this side cannot keep, and a unit
	// reading it would be reading a value the core never produces. A unit that
	// wants new-versus-seen counts already knows: it is the one holding the
	// cursor.
	DispositionAccepted Disposition = "accepted"

	// DispositionSkipped is a record the core deliberately did not keep — a
	// message every party to which is on the installation's own domains, which
	// is colleagues talking rather than evidence of a customer relationship.
	//
	// It is a SUCCESS carrying a zero Ref. The core committed a breadcrumb
	// recording the drop, and its own contract is that a skip advances a
	// connector's watermark, so a unit treats this exactly as it treats
	// Accepted: move the cursor past it.
	DispositionSkipped Disposition = "skipped"
)

// Ref names a record the core holds.
type Ref struct {
	Type string
	ID   string
}

// Result is what one ingest did.
//
// BOTH DISPOSITIONS ADVANCE A CURSOR, and that is the point of returning one
// instead of an error for the skip: a unit's watermark moves past every record
// the core has finished deciding about, whether or not a row came of it.
type Result struct {
	Ref         Ref
	Disposition Disposition
}

// The refusals an ingest can answer. Sentinels rather than typed errors,
// for the reason the core port's are: a unit's only reasonable response to each
// is a decision, and the core's own error text must not become a string some
// unit parses.
var (
	// ErrIngressNotDeclared is a unit that declared no ingress source, or named
	// one it did not declare. The manifest is the contract: a unit reaches core
	// capture only where an operator can see that it does.
	ErrIngressNotDeclared = errors.New("extension: this unit declared no such ingress source")

	// ErrAttendedIngest is an ingest from an invocation that has a caller.
	//
	// It is the exact mirror of the core port refusing a job tick. Ingress runs
	// on the authority of the member whose credential produced the record, and
	// an invocation that ALSO has a caller has two authorities in play — which
	// is the shape where a low-privileged caller has a unit act as somebody
	// else and reads the answer. A unit wanting an on-demand sync enqueues its
	// job rather than ingesting inline.
	ErrAttendedIngest = errors.New("extension: a record is ingested by an unattended run, never on a caller's invocation")

	// ErrNestedIngest is an ingest from inside the unit's own transaction. The
	// core's capture pipeline opens its own, so this would take a second
	// connection while holding one — which on a small pool does not fail, it
	// hangs.
	ErrNestedIngest = errors.New("extension: a record cannot be ingested from inside a transaction the unit is holding")
)
