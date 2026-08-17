// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The provider half: the calls this unit makes against one Official Account,
// and the bounds a remote party is held to on the way back.
//
// ONE THING HERE IS UNLIKE EVERY OTHER CONNECTOR IN THIS TREE, and it is the
// mistake that would be hardest to see afterwards: **every Zalo response is
// HTTP 200**. An expired token is 200. An unregistered endpoint is 200. A
// package too low for the API is 200. The outcome lives in the body's `error`
// field — 0 for success, negative for everything else. A client that classified
// on the status line would read a revoked token as a successful empty page,
// advance nothing, report nothing, and look exactly like an OA nobody messages.
//
// So there is no status classification here at all beyond "did the transport
// work", and the class every caller acts on is derived from the BODY.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// openAPIBase is the OA OpenAPI host, and it is a CONSTANT rather than
// something an operator types.
//
// That is worth stating because the sibling connector in this tree carries a
// hundred lines of egress guard and this one carries none: dispact dials a
// deployment URL a member pastes, so a host resolving to a link-local address
// would make this installation's worker fetch its own cloud metadata. Zalo has
// one endpoint, named here, so there is no member-supplied host to guard. What
// remains is the redirect refusal below — a provider choosing another host
// mid-request is the one way this fixed address could still become an arbitrary
// one.
const openAPIBase = "https://openapi.zalo.me"

// The refusal classes every caller acts on. A provider is a remote party, so
// what crosses this boundary is a CLASS: the connection's status column records
// it, the screen renders it, and neither is a place for another system's prose.
//
// They are CONSTANTS rather than errors.New values, and that is the tier's rule
// rather than a style: a unit's root package may hold no package-level
// initializer that CALLS anything, because an initializer runs at import,
// before the declaration has been validated. A string-kinded error type is
// comparable, so errors.Is answers about these exactly as it does about a
// sentinel.
const (
	// errUnauthorized is the access token no longer being accepted. It is the
	// class a REFRESH answers, and only when the refresh itself fails does the
	// connection park.
	//
	// `-216` is measured and covers both of this provider's wordings ("Access
	// token has expired" and "is invalid"). `-211` is mapped alongside it as
	// DOCUMENTED AND NOT OBSERVED, which is worth marking rather than assuming:
	// this class is what triggers a single-use rotation, so a code mapped here on
	// a guess would spend a credential on a guess.
	errUnauthorized zaloError = "zalo-oa: the access token was rejected"

	// errTierTooLow is `-224`: the OA's package does not include this API. It is
	// the tier gate's refusal at connect, and — because a package EXPIRES — it
	// is also what a working connection starts answering the day its term ends.
	// It costs money to resolve, which is why it may never be collapsed with the
	// class below.
	errTierTooLow zaloError = "zalo-oa: the Official Account's package does not include this API"

	// errAPINotRegistered is `-212`: the developer APP has not enabled this API
	// group in the console. It is a FREE toggle, and telling an admin to pay
	// 2,500,000 đ/year when they need to click a switch is the failure the whole
	// error catalog exists to prevent.
	errAPINotRegistered zaloError = "zalo-oa: this app has not registered this API group"

	// errTransient is a timeout, a rate limit or a 5xx — the caller stops and
	// the next tick tries again, with the cursor exactly where it was.
	errTransient zaloError = "zalo-oa: the provider is unavailable"

	// errProvider is everything else: an answer this unit cannot read, a code it
	// cannot act on. It fails the tick rather than advancing over records nobody
	// has seen.
	errProvider zaloError = "zalo-oa: the provider answered something this unit cannot use"

	// errUnanswered marks a request that WENT OUT and whose outcome never came
	// back. It always accompanies errTransient rather than replacing it, because
	// for a READ the two are the same fact — the next tick asks again.
	//
	// For a SEND they are not. Zalo accepts no idempotency key on a message and
	// offers no prior-send lookup, so a POST whose answer was lost is a question
	// no later attempt can settle. A retry there messages a customer twice with
	// nothing able to detect it, which is why send.go maps this — and only this
	// — onto the core's own unknown-outcome class.
	errUnanswered zaloError = "zalo-oa: the provider never reported the outcome"
)

// zaloError is one of this unit's own refusal classes.
type zaloError string

func (e zaloError) Error() string { return string(e) }

// The provider's own codes, named where they are mapped so a reader can check
// this list against the error catalog without decoding a number in a switch.
const (
	codeOK              = 0
	codeInvalidArgument = -201
	codePageTooLarge    = -210
	// codeTokenInvalid is documented, not observed — see errUnauthorized.
	codeTokenInvalid    = -211
	codeAPINotRegisterd = -212
	codeTokenExpired    = -216
	codeTierTooLow      = -224
	codeRateLimited     = -32
)

// maxChatPage is 10, and it is the provider's cap rather than a preference:
// `listrecentchat` refuses a larger `count` with `-210` instead of clamping it.
const maxChatPage = 10

// maxResponseBytes bounds one provider answer. A full page of ten messages runs
// to a few kilobytes; this leaves room for a provider that grows its shape and
// still refuses one that answers with a stream.
const maxResponseBytes = 4 << 20

// requestTimeout bounds one call. The job's own wall clock (api/jobs.yaml)
// bounds the tick; this is what keeps a single hung request from spending all of
// it — and, for the refresh, what bounds how long a serializing transaction is
// held open.
const requestTimeout = 20 * time.Second

// client is one authenticated connection to one Official Account.
type client struct {
	token string
	// http is injectable so a test can serve the provider's shapes over a
	// loopback listener. The same split the sibling connector uses, and for the
	// same reason: what production runs must not be reconfigured in place by the
	// suite that claims to prove it.
	http *http.Client
	// base is the OpenAPI host. It is a field rather than the constant read
	// directly ONLY so a test can point it at its own listener; every production
	// path builds it from openAPIBase.
	base string
}

// newClient builds the client every production path uses.
func newClient(token string) *client {
	return &client{
		token: token,
		base:  openAPIBase,
		http: &http.Client{
			Timeout: requestTimeout,
			// A redirect is another host, chosen by the provider rather than by
			// this installation. Since the base address is the only guard this
			// unit has (see openAPIBase), following one would give that guard
			// away — an API that answers a redirect to these endpoints is one
			// this unit does not understand.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// clientFactory is how a caller reaches the provider.
//
// It is a parameter at each entry point rather than a direct call to newClient,
// because the provider is this unit's ONE true boundary: everything above it —
// the tier gate, the walk, the cursor, the refusal classes — is this unit's own
// logic and is driven end to end through this seam.
type clientFactory func(token string) *client

// oaProfile is what `getoa` says about the account a token speaks for.
//
// package_name is a LOCALIZED VIETNAMESE DISPLAY STRING (`Cơ bản` free,
// `Tăng trưởng`, `Toàn diện`), which is why it is carried as evidence for an
// admin to read and never compared against anything: Zalo can rename a tier or
// add one, and a connector that gated on the name would refuse a paying
// customer the day marketing renamed their package. The gate is a capability
// probe; see connection.go.
type oaProfile struct {
	OAID                    string `json:"oa_id"`
	Name                    string `json:"name"`
	PackageName             string `json:"package_name"`
	PackageValidThroughDate string `json:"package_valid_through_date"`
}

// chatMessage is one message row, identical from `listrecentchat` and
// `conversation`.
//
// `src` is the DIRECTION and it is explicit: 0 is the OA writing to the user, 1
// is the user writing to the OA. No positional inference is needed and none is
// done — a connector that guessed direction from which id matched the account
// would invert every message the day an OA replied to itself.
type chatMessage struct {
	Src       int    `json:"src"`
	Time      int64  `json:"time"`
	MessageID string `json:"message_id"`
	Type      string `json:"type"`
	Message   string `json:"message"`

	FromID          string `json:"from_id"`
	FromDisplayName string `json:"from_display_name"`
	ToID            string `json:"to_id"`
	ToDisplayName   string `json:"to_display_name"`

	// The type-conditional attributes. Each is present for some types and absent
	// for others, and none of them is fetched or resolved: a link arrives as a
	// bare URL here (unlike the webhook's pre-resolved preview), and resolving
	// one ourselves would reopen a request-forgery surface on customer-supplied
	// URLs. What is stored is what the provider said.
	URL         string `json:"url"`
	Thumb       string `json:"thumb"`
	Description string `json:"description"`
	// Location is longitude and latitude as ONE STRING, not an object. Decoding
	// it into a struct silently yields the zero value.
	Location string `json:"location"`
	// Links is what a `link` or `links` message carries, and it is decoded
	// LOOSELY on purpose: the documentation calls these bare URLs while the
	// webhook's own shape wrapped each in an object, and nobody has measured
	// which arrives here. Reading it as either concrete type would drop the whole
	// message body the day it is the other one, so each element is kept raw and
	// read for a URL by linkURL.
	Links []json.RawMessage `json:"links"`

	// Raw is the provider's own row, kept so what the installation stores as
	// evidence is what the provider sent rather than a re-encoding of the fields
	// this unit happens to know.
	Raw json.RawMessage `json:"-"`
}

// counterparty is the human's own account id and name on this row, which end
// depending on the direction.
func (m chatMessage) counterparty() (id, name string) {
	if m.inbound() {
		return m.FromID, m.FromDisplayName
	}
	return m.ToID, m.ToDisplayName
}

// inbound reports whether the customer wrote this message.
func (m chatMessage) inbound() bool { return m.Src == srcUserToOA }

// The two values `src` takes.
const (
	srcOAToUser = 0
	srcUserToOA = 1
)

// profile identifies the account this token speaks for, and proves the token
// works at all. It is the first call of the tier gate and the cheapest one this
// provider has.
func (c *client) profile(ctx context.Context) (oaProfile, error) {
	var found oaProfile
	if err := c.get(ctx, "/v2.0/oa/getoa", nil, &found); err != nil {
		return oaProfile{}, err
	}
	if strings.TrimSpace(found.Name) == "" {
		return oaProfile{}, fmt.Errorf("%w: the account answer names no Official Account", errProvider)
	}
	// THE ID IS CHECKED HERE, once, because everything downstream keys on it.
	// It namespaces every person binding, every thread key and every natural key
	// this unit writes, and it is the whole of what stops an outbound reply
	// reaching a different human (accountWithinOA). A blank one would make that
	// refusal's empty-account branch the only thing between a send and an
	// arbitrary recipient.
	if !usableOAID(found.OAID) {
		return oaProfile{}, fmt.Errorf("%w: the account answer names no usable Official Account id", errProvider)
	}
	return found, nil
}

// usableOAID reports whether an Official Account id is one this unit can build a
// namespace out of: digits, and between one and maxOAIDBytes of them.
//
// The rule that matters is the absence of a COLON. This unit joins an account id
// and a person's id with one, so an id containing a colon would make the prefix
// ambiguous — `123:456` plus account `789` reads back, to a connection at account
// `123`, as the person `456:789`, which defeats the namespace refusal by
// spelling. Zalo's own ids are numeric, so the narrow rule is also the true one.
//
// It is spelled as a loop rather than as a regexp because a compiled pattern
// would have to be a package-level var, and a unit's root package may hold no
// initializer that CALLS anything — the same rule the error classes above are
// constants for.
func usableOAID(id string) bool {
	if id == "" || len(id) > maxOAIDBytes {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// recentChat reads one page of the GLOBAL message walk, newest first.
//
// It is global rather than per-conversation — offsets shift by MESSAGE and
// cross thread boundaries — which is what makes it the primary ingest lane:
// everything costs ceil(M/10) requests, where walking each thread separately
// costs the sum of ceil(mᵢ/10) and is never smaller.
//
// Paging terminates on a SHORT PAGE and never on an error: an offset past the
// end answers `error: 0` with no rows.
func (c *client) recentChat(ctx context.Context, offset int) ([]chatMessage, error) {
	// The page is read as RAW rows first and each decoded from its own bytes, so
	// every message keeps the provider's original document. What capture stores
	// as evidence must be what the provider said, and a re-marshal of the struct
	// above would store only the fields this unit happens to know.
	var page []json.RawMessage
	err := c.get(ctx, "/v2.0/oa/listrecentchat",
		map[string]any{"offset": offset, "count": maxChatPage}, &page)
	if err != nil {
		return nil, err
	}
	read := make([]chatMessage, 0, len(page))
	for _, raw := range page {
		var row chatMessage
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, fmt.Errorf("%w: a message row is not the shape this unit reads", errProvider)
		}
		row.Raw = raw
		read = append(read, row)
	}
	return read, nil
}

// sendConsultation transmits one text message to one account and returns the
// provider's own id for it.
//
// The id is what a later reply anchors on and what makes a re-read of the same
// message a no-op: `message_id` is proven identical on the send and on the read
// back, which is what lets one number be both the receipt and the natural key.
func (c *client) sendConsultation(ctx context.Context, account, body string) (string, error) {
	var sent struct {
		MessageID string `json:"message_id"`
	}
	payload := map[string]any{
		"recipient": map[string]any{"user_id": account},
		"message":   map[string]any{"text": body},
	}
	if err := c.post(ctx, "/v3.0/oa/message/cs", payload, &sent); err != nil {
		return "", err
	}
	// An accepted send that returns no id is not a failure: the message is gone
	// either way, and reporting an error would have the core retry a delivery
	// the recipient has already received. What is lost is the anchor for a later
	// reply, which the caller records as absent rather than faked.
	return sent.MessageID, nil
}

// chatPageOffset renders an offset for the walk's own bookkeeping. It exists so
// the one place a page position becomes text is next to the cap it obeys.
func chatPageOffset(page int) int { return page * maxChatPage }

// atoiOrZero reads a provider-supplied numeric string, answering zero when it is
// not one.
//
// It exists because this provider types numbers as strings in several places
// (`expires_in` on a grant, `last_interaction` on a quota answer) and a unit
// that assumed either shape would panic on the other. Zero is the honest answer
// for "the provider did not tell us", and every caller treats it as absent
// rather than as the epoch.
func atoiOrZero(raw string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}
