// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector

// The party a captured record was WITH — one end of the exchange, and the one
// direction is defined against. Split from connector.go so the record shape and
// the party identity stay separately legible; MessageParticipant, the FURTHER
// parties, lives in participant.go beside it.

// Counterparty names the non-owner participant of one captured message. A
// mail record identifies them by Email (authoritative); a channel record
// identifies them by ChannelIdentity. A record carrying BOTH is named by the
// channel identity and CORROBORATED by the address: the identity outranks it,
// being the key a reply is routed on and the one a person is bound by, while
// the address is evidence the resolution ladder may match against. Which is
// which is settled once, by capture's counterpartyShapeOf. DisplayName is the
// header's human name (may be empty or hostile — untrusted text); Domain is
// the lowercased mail domain, and stays empty on a channel record even when one
// corroborates, because every gate that reads it is a mail gate the channel
// shape short-circuits past; Direction is relative to the mailbox/bot owner
// (DirectionInbound | DirectionOutbound).
type Counterparty struct {
	Email       string
	DisplayName string
	Domain      string
	Direction   string
	// ChannelIdentity is the channel-record twin of Email: a messaging
	// connector populates this, and populates Email too when its provider
	// knows the sender's address and the source declared the email merge key.
	// Zero for every mail record.
	//
	// The four mail-domain gates (T0 internal-domain, freemail,
	// transactional/ESP, quarantine) are bypassed wholesale for a channel
	// record (capture's sinkensure.go), so a corroborating address does not
	// wake them: the bypass keys on the SHAPE, not on Email being empty.
	ChannelIdentity ChannelIdentity
	// ListUnsubscribe reports whether the message carried an RFC 2369
	// List-Unsubscribe header — the bulk-mail corroboration the transactional
	// suppression gate (CAP-PARAM-6, ADR-0072) requires before a subdomain
	// prefix rule may suppress record creation. Mail connectors populate it;
	// zero for records that carry no such signal.
	ListUnsubscribe bool
	// sentByOwner is the T1 correspondence-positive gate's only evidence
	// (ADR-0072 §1), and it is deliberately UNEXPORTED. The field is the one
	// thing on this struct a connector must not be able to state for itself:
	// whoever sets it can whitelist an arbitrary address past transactional
	// suppression. Unexported, the compiler refuses every route a convention
	// could not — a positional literal, a JSON unmarshal, reflection, a
	// conversion from a look-alike struct, a pointer handed to a decoder.
	// WithOwnerAttestation is the sole way in, and SentByOwner the sole way out.
	sentByOwner bool
	// mayCorroborateByEmail records that the SOURCE of this record declared the
	// email merge key, so an address riding alongside a channel identity may
	// reach the resolution ladder. Like sentByOwner it is unexported, and for
	// the same reason: whoever can set it can have an undeclared source's
	// address treated as identity evidence.
	//
	// It is a derived bool rather than the declared key list it comes from. The
	// list belongs to the declaration and to the manifest, where its members are
	// read by a human; carrying it here would alias the composition's own slice,
	// give nil and empty as two spellings of one state, and foreclose comparing
	// this struct forever. WithDeclaredEmailMerge is the sole way in and
	// MayCorroborateByEmail the sole way out.
	mayCorroborateByEmail bool
}

// Message direction relative to the mailbox owner, as Counterparty.Direction
// reports it.
const (
	DirectionInbound  = "inbound"
	DirectionOutbound = "outbound"
)

// WithOwnerAttestation returns a copy recording that the authenticated mailbox
// owner sent this message. providerFiled is the PROVIDER's own filing — Gmail's
// SENT label, an IMAP \Sent special-use mailbox, Microsoft's SentItems folder —
// and it is honored only where Direction independently names the owner as the
// message's author.
//
// The conjunction lives here, in the port that owns the field, because neither
// half is sufficient and no caller may choose to apply only one. Direction
// compares the forgeable From header against the owner's address, so a spoofed
// From:owner delivered to the inbox would otherwise pass as the owner's own
// correspondence. And placement is not authorship: a server-side rule can file
// a third party's message into the sent container, where the counterparty is
// that stranger's own address.
//
// Build the Counterparty first: this reads Direction as it stands at the call,
// so attesting before Direction is populated — or reassigning it afterwards —
// yields an answer that no longer matches the record. Both mistakes fail toward
// false, and the unexported field carries the same cost at any serialization
// boundary: a Counterparty that ever crosses one arrives un-attested rather
// than wrongly attested.
//
// A caller that attests nothing leaves the answer false, which suppresses
// rather than trusts.
func (c Counterparty) WithOwnerAttestation(providerFiled bool) Counterparty {
	c.sentByOwner = providerFiled && c.Direction == DirectionOutbound
	return c
}

// SentByOwner reports whether both halves of the attestation agreed.
func (c Counterparty) SentByOwner() bool { return c.sentByOwner }

// WithDeclaredEmailMerge returns a copy recording that this record's source
// declared the email merge key, and so may carry an address to corroborate a
// human it names by a channel identity.
//
// declared is the SOURCE's declaration, resolved by the core from the manifest —
// never anything the record itself said. A record that names its human by an
// address alone is unaffected: that address is the identity, not a
// corroboration, and needs no declaration to be honoured.
//
// A caller that declares nothing leaves the answer false, which refuses the
// corroboration rather than trusting it — the same direction WithOwnerAttestation
// fails in, and for the same reason.
func (c Counterparty) WithDeclaredEmailMerge(declared bool) Counterparty {
	c.mayCorroborateByEmail = declared
	return c
}

// MayCorroborateByEmail reports whether the source declared the email merge key.
func (c Counterparty) MayCorroborateByEmail() bool { return c.mayCorroborateByEmail }
