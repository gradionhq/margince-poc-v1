// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// The provider half: three calls against one member's Relay, and the bounds
// a remote party is held to on the way back.
//
// Everything here was MEASURED against a live deployment rather than reasoned
// from the API's shape, and three of the measurements contradict what the
// obvious reading would be. They are marked where they are relied on.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The refusal classes a poll acts on. A provider is a remote party, so what
// crosses this boundary is a CLASS: the connection's status column records it,
// the screen renders it, and neither is a place for another system's prose.
// They are CONSTANTS rather than errors.New values, and that is the tier's rule
// rather than a style: a unit's root package may hold no package-level
// initializer that CALLS anything, because an initializer runs at import —
// before the declaration has been validated and before anything has decided
// this unit may run at all. A string-kinded error type is comparable, so
// errors.Is answers about these exactly as it does about a sentinel.
const (
	// errUnauthorized is a 401: the token was revoked, expired, or was never
	// valid. Retrying it on a cadence is how an installation rate-limits
	// itself for nothing, so the connection parks and asks for a new one.
	errUnauthorized relayError = "relay: the provider rejected this member's token"

	// errTransient is a timeout, a 429 or a 5xx — the poll stops and the next
	// tick tries again, with the cursor exactly where it was.
	errTransient relayError = "relay: the provider is unavailable"

	// errProvider is everything else: a shape this unit cannot read, a 4xx it
	// cannot act on. It fails the tick rather than advancing over records
	// nobody has seen.
	errProvider relayError = "relay: the provider answered something this unit cannot use"

	// errUnanswered marks a request that WENT OUT and whose outcome never came
	// back: the connection failed mid-flight, or the answer could not be read.
	// It always accompanies errTransient rather than replacing it, because for
	// a READ the two are the same fact — the next tick asks again.
	//
	// For a SEND they are not. The message may be at the recipient and may not,
	// and this provider offers no idempotency key and no prior-send lookup, so
	// no later attempt could ever find out. A retry there messages a customer
	// twice with nothing able to detect it, which is why the send path maps
	// this — and only this — onto the core's own unknown-outcome class.
	errUnanswered relayError = "relay: the provider never reported the outcome"
)

// relayError is one of this unit's own refusal classes.
type relayError string

func (e relayError) Error() string { return string(e) }

// maxPageSize is 50, and it is a MEASUREMENT rather than a preference: the
// deployment serves at most 50 items per page and — this is the part worth
// knowing — a larger `limit` is not clamped to it. Asking for 100 answers with
// the DEFAULT page of 20. So a poll that asked for more than this would page
// more slowly than one that asked for the maximum, silently.
const maxPageSize = 50

// maxResponseBytes bounds one provider answer. A full page of 50 items with
// bodies runs to tens of kilobytes; this leaves room for a provider that grows
// its shape and still refuses one that answers with a stream.
const maxResponseBytes = 4 << 20

// requestTimeout bounds one call. The job's own wall clock (api/jobs.yaml)
// bounds the tick; this is what keeps a single hung request from spending all
// of it.
const requestTimeout = 20 * time.Second

// client is one member's connection to one Relay deployment.
type client struct {
	base  *url.URL
	token string
	// http is injectable so a test can serve the provider's shapes over a
	// loopback listener, which the guarded transport below refuses by design.
	// The same split webread uses, and for the same reason: the guard is a
	// property of the PRODUCTION client, and a test that disabled it in place
	// would be testing a client no deployment runs.
	http *http.Client
}

// newClient builds the client a poll uses, with the egress guard attached.
//
// THE GUARD IS THE POINT OF THIS CONSTRUCTOR. base_url is text a member typed:
// without a control hook, `https://<name-that-resolves-to-169.254.169.254>`
// makes this installation's own worker fetch its cloud metadata endpoint and
// hand the answer to whoever asked. The core has exactly this guard
// (internal/platform/netguard) and a unit cannot import it — the published
// surface is stdlib-only and reaches nothing under internal, and an extensions/
// module is its own Go module besides, so not even a fitness test can hold the
// two side by side. What stands in for that is a table of addresses this
// package refuses, kept in client_test.go and worth reading as the guard's real
// definition. Publishing a guarded HTTP client on the tier's own surface is the
// fix that removes the copy (#1194).
func newClient(base, token string) (*client, error) {
	parsed, err := parseBaseURL(base)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: requestTimeout, Control: refusePrivate}
	return &client{
		base:  parsed,
		token: token,
		http: &http.Client{
			Timeout:   requestTimeout,
			Transport: &http.Transport{DialContext: dialer.DialContext},
			// A redirect is another host, chosen by the provider rather than
			// by the member — and the guard would still hold for it, which is
			// exactly why refusing is cheap here. An API that answers a
			// redirect to these three endpoints is one this unit does not
			// understand.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// parseBaseURL is the first half of the guard, applied where a human can still
// read the refusal: at connect, against what they typed.
//
// https only, because the token rides every request. A host name rather than an
// address literal, because an address literal is how the interesting internal
// targets are named and no real deployment is reached that way. No credentials
// in the URL, no query, no fragment: each would be silently carried onto every
// request this unit makes.
func parseBaseURL(base string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return nil, fmt.Errorf("%w: the base URL is not a URL", extension.ErrInvalid)
	}
	switch {
	case parsed.Scheme != "https":
		return nil, fmt.Errorf("%w: the base URL must be https — the member's token rides every request to it", extension.ErrInvalid)
	case parsed.Host == "":
		return nil, fmt.Errorf("%w: the base URL names no host", extension.ErrInvalid)
	case parsed.User != nil:
		return nil, fmt.Errorf("%w: the base URL carries credentials, which would be sent on every request", extension.ErrInvalid)
	case parsed.RawQuery != "" || parsed.Fragment != "":
		return nil, fmt.Errorf("%w: the base URL carries a query or fragment, which is not part of a host", extension.ErrInvalid)
	case net.ParseIP(hostOnly(parsed.Host)) != nil:
		return nil, fmt.Errorf("%w: the base URL must name a host, not an address", extension.ErrInvalid)
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	return parsed, nil
}

// hostOnly drops the port from a host:port, and answers the whole string when
// there is none — which is what net.SplitHostPort reports as an error.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// refusePrivate refuses to dial anything that is not a globally routable
// unicast address, on the CONCRETE address the resolver returned — so a DNS
// answer pointing at the deployment's own network cannot bypass it, which
// checking the name could not prevent.
//
// It is the core's netguard.RefusePrivate rule, restated because a unit cannot
// import it (see newClient). TestTheEgressGuardRefusesEveryNonPublicAddress is
// what holds this copy honest.
func refusePrivate(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("relay: the address %q is not host:port", address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("relay: %q did not resolve to an address", host)
	}
	if !publicIP(ip) {
		return fmt.Errorf("relay: refusing to dial %s — it is not a public address, and a member-supplied host must not become a probe of this deployment's own network", ip)
	}
	return nil
}

// publicIP reports whether ip is a globally routable unicast address.
//
// A CIDR this package cannot parse is treated as MATCHING, which is the safe
// direction: a typo in the list above makes the guard refuse more, never less.
func publicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	// The core's own denylist, read from the published surface rather than
	// restated here. A hand-copy drifts, and a range the core refuses while this
	// unit admits it is a member-supplied host reaching an internal address —
	// which is why the surface publishes it and why nothing below is a literal.
	for _, reserved := range extension.ReservedNets() {
		if reserved.Contains(ip) {
			return false
		}
	}
	return true
}

// providerUser is a Relay account, as both /auth/me and the batch lookup
// answer it. Only the members this unit uses are decoded; the provider's
// document is wider and stays that way.
type providerUser struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	FullName    string `json:"full_name"`
	IsBot       bool   `json:"is_bot"`
}

// name is what a human calls this account: the display name where there is
// one, the full name otherwise, and the address as the last resort — a
// counterparty with no name at all renders as an empty row on the timeline.
func (u providerUser) name() string {
	for _, candidate := range []string{u.DisplayName, u.FullName, u.Email} {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate)
		}
	}
	return ""
}

// inboxItem is one notification. The id is an INT64 and monotonic, which is
// what makes it usable as both the cursor and the natural key's provider half.
type inboxItem struct {
	ID          int64           `json:"id"`
	Type        string          `json:"type"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	ChannelID   string          `json:"channel_id"`
	ChannelSlug string          `json:"channel_slug"`
	MessageID   string          `json:"message_id"`
	SenderID    string          `json:"sender_id"`
	CreatedAt   time.Time       `json:"created_at"`
	Metadata    itemMetadata    `json:"metadata"`
	Raw         json.RawMessage `json:"-"`
}

// itemMetadata carries the one flag this unit reads: whether a bot sent it.
//
// It is read CLIENT-side, and that is the second measurement: the API takes a
// `source=human` parameter and answering it changes nothing — the same
// reactions come back either way. A unit that trusted the parameter would
// believe it had filtered bots and would not have.
type itemMetadata struct {
	SenderIsBot bool `json:"sender_is_bot"`
}

// inboxPage is one page of the member's inbox, newest first.
type inboxPage struct {
	Items   []inboxItem `json:"items"`
	HasMore bool        `json:"has_more"`
}

// me identifies the token's owner: which Relay workspace this connection
// reads, and the member's own address, which the record needs so the core can
// see both ends of a message.
func (c *client) me(ctx context.Context) (providerUser, error) {
	var user providerUser
	if err := c.get(ctx, "/api/auth/me", nil, &user); err != nil {
		return providerUser{}, err
	}
	if user.ID == "" || user.WorkspaceID == "" {
		return providerUser{}, fmt.Errorf("%w: the account answer names no user or no workspace", errProvider)
	}
	// The address is checked HERE, once per connection, rather than per record.
	// Every record this connection builds names the member as a party, and the
	// core reads that party to decide whether a message is only colleagues
	// talking. A member with no address is a party that decision cannot weigh,
	// so the CONNECTION is refused — once, with a sentence naming the account —
	// rather than each of the records it would go on to build.
	if strings.TrimSpace(user.Email) == "" {
		return providerUser{}, fmt.Errorf("%w: the account answer names no address for this member, and every record names them as a party", errProvider)
	}
	return user, nil
}

// inbox reads one page of notifications, newest first. before pages BACKWARD:
// zero asks for the newest page, and any other value asks for the page just
// older than that id.
func (c *client) inbox(ctx context.Context, before int64) (inboxPage, error) {
	query := url.Values{"limit": {strconv.Itoa(maxPageSize)}}
	if before > 0 {
		query.Set("before", strconv.FormatInt(before, 10))
	}
	// The page is read as RAW items first and each one decoded from its own
	// bytes, so every item keeps the provider's original document. What capture
	// stores as evidence must be what the provider said, and a re-marshal of the
	// struct below would store the fields this unit happens to know — which is
	// exactly the thing evidence is not.
	//
	// Read this way rather than by decoding into one struct carrying both a
	// typed and a raw `items` member: two fields cannot share a JSON name, so
	// one of them silently stays empty. That version compiled, and every page it
	// read decoded to zero notifications.
	var page struct {
		Items   []json.RawMessage `json:"items"`
		HasMore bool              `json:"has_more"`
	}
	if err := c.get(ctx, "/api/notifications/inbox", query, &page); err != nil {
		return inboxPage{}, err
	}
	read := inboxPage{HasMore: page.HasMore, Items: make([]inboxItem, 0, len(page.Items))}
	for _, raw := range page.Items {
		var item inboxItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return inboxPage{}, fmt.Errorf("%w: a notification is not the shape this unit reads", errProvider)
		}
		item.Raw = raw
		read.Items = append(read.Items, item)
	}
	return read, nil
}

// users resolves senders to accounts, in one call for the whole batch.
//
// The answer is a bare ARRAY, not an object with a member — the third
// measurement, and the one a hand-written client gets wrong in a way that
// compiles: decoding into a wrapper answers zero users, every sender resolves
// to nothing, and every landed record carries a counterparty with no address.
func (c *client) users(ctx context.Context, ids []string) (map[string]providerUser, error) {
	resolved := make(map[string]providerUser, len(ids))
	if len(ids) == 0 {
		return resolved, nil
	}
	var answer []providerUser
	if err := c.post(ctx, "/api/users/batch", map[string]any{"ids": ids}, &answer); err != nil {
		return nil, err
	}
	for _, user := range answer {
		resolved[user.ID] = user
	}
	return resolved, nil
}

//craft:ignore naked-any the decode target is whatever the caller reads a provider answer into — the same contract encoding/json itself has, and there is no method-level type parameter in Go to state it with
func (c *client) get(ctx context.Context, path string, query url.Values, into any) error {
	endpoint := *c.base
	endpoint.Path += path
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("%w: building the request: %s", errProvider, err.Error())
	}
	return c.do(req, into)
}

//craft:ignore naked-any the request body and the decode target are both the caller's shapes; see get
func (c *client) post(ctx context.Context, path string, body any, into any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%w: encoding the request: %s", errProvider, err.Error())
	}
	endpoint := *c.base
	endpoint.Path += path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(string(encoded)))
	if err != nil {
		return fmt.Errorf("%w: building the request: %s", errProvider, err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, into)
}

// do runs one request and classifies whatever comes back.
//
//craft:ignore naked-any the decode target is the caller's shape; see get
func (c *client) do(req *http.Request, into any) error {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// Every transport failure is transient from a poll's point of view,
		// INCLUDING the guard's refusal — which is not transient at all, but
		// answering it as a token problem would tell a member to re-paste a
		// token that is fine. The tick fails, the class is recorded, and the
		// next tick meets the same wall.
		//
		// errUnanswered rides along because the SEND path needs the distinction
		// a poll does not: the request may have arrived. See there.
		return fmt.Errorf("%w: %w: %s", errTransient, errUnanswered, err.Error())
	}
	//craft:ignore swallowed-errors best-effort close: the capped read below may leave the body mid-stream, so a close error carries no signal for this call's result
	defer func() { _ = resp.Body.Close() }()
	if err := classify(resp.StatusCode); err != nil {
		return err
	}
	// Bounded, because a provider deciding how much this worker reads into
	// memory is the same class of problem as a provider deciding how much it
	// stores.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		// Also unanswered: the provider accepted the request and this side
		// never learned what it decided.
		return fmt.Errorf("%w: %w: reading the answer: %s", errTransient, errUnanswered, err.Error())
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("%w: the answer is over the %d-byte cap", errProvider, maxResponseBytes)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("%w: the answer is not the shape this unit reads", errProvider)
	}
	return nil
}

// classify maps a status onto the three classes a poll can act on.
func classify(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return errUnauthorized
	case status == http.StatusTooManyRequests, status >= http.StatusInternalServerError:
		return fmt.Errorf("%w: the provider answered %d", errTransient, status)
	case status >= http.StatusBadRequest:
		return fmt.Errorf("%w: the provider answered %d", errProvider, status)
	}
	return nil
}
