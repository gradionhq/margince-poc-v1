// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Client ID Metadata Documents (ADR-0092 §4; MCP 2026-07-28
// basic/authorization/client-registration; draft-ietf-oauth-client-id-metadata-document-00).
//
// A modern client identifies itself with an HTTPS URL that resolves to its own
// metadata document, instead of registering first and being handed an opaque
// id. It is the forward path because it needs no prior relationship at all —
// which is the ordinary MCP case — and because a client id that IS a document
// is portable across authorization servers.
//
// DCR keeps working for the compatibility window (ADR-0092 §4), so a client
// registered before this existed is not stranded by a revision it never asked
// for. Nothing below touches that path: a client_id that is not an https URL
// with a path is not a CIMD id and is resolved from the table exactly as before.
//
// THE DANGEROUS PART IS THE FETCH. An unauthenticated caller chooses a URL and
// this server dereferences it, which is textbook SSRF, so every guard below is
// load-bearing rather than defensive style. They are stated with the attack each
// one closes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/netguard"
)

// The fetch's bounds. Each is a refusal, not a preference.
const (
	// cimdFetchTimeout: a human is waiting on a consent page while this runs,
	// and a client that cannot serve a few hundred bytes in five seconds is not
	// one this server should hold a browser open for.
	cimdFetchTimeout = 5 * time.Second
	// cimdMaxDocument bounds what is read into memory. A metadata document is a
	// few hundred bytes; anything near this ceiling is a client trying to spend
	// the server's memory rather than describe itself.
	cimdMaxDocument = 64 << 10
	// cimdMinCache / cimdMaxCache clamp what the client's own cache headers may
	// ask for. Without a floor a client can make this server refetch on every
	// authorize, which turns one consent page into an outbound request the
	// client controls the rate of; without a ceiling a rotated redirect_uris
	// list would take a year to be believed.
	cimdMinCache = 5 * time.Minute
	cimdMaxCache = 24 * time.Hour
)

// errNotCIMD marks a client_id that is not a metadata-document URL at all — the
// DCR and pre-registered case, resolved from the table.
var errNotCIMD = errors.New("identity: client_id is not a metadata document URL")

// cimdDocument is the metadata a client publishes about itself. Only the three
// members the profile makes REQUIRED are read: everything else in the draft is
// display or capability, and a member this server does not act on is one it
// should not pretend to have validated.
type cimdDocument struct {
	ClientID     string   `json:"client_id"`
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
}

// cimdClientID reports whether raw is a Client ID Metadata Document URL, and
// answers errNotCIMD when it is not.
//
// The rule is the draft's: https, with a PATH. The path requirement is not
// cosmetic — it is what stops a bare origin from being a client id, and a bare
// origin is exactly what an attacker would want, since one host would then be
// one identity rather than one document per client.
//
// A fragment or userinfo is refused rather than stripped. Both would make two
// spellings of one identity, and the equality check further down compares the
// PRESENTED string against the document's own claim: a normalizer here would
// mean the string this server compares is not the string the client sent.
func cimdClientID(raw string) error {
	if !strings.HasPrefix(raw, "https://") {
		return errNotCIMD
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errNotCIMD
	}
	switch {
	case u.Scheme != "https", u.Host == "", u.User != nil, u.Fragment != "":
		return errNotCIMD
	case u.Path == "" || u.Path == "/":
		return errNotCIMD
	}
	return nil
}

// resolveCIMDClient makes a metadata-document client resolvable by the ordinary
// authorize path: it ensures the oauth_client row for clientID exists and is
// backed by a document fresh enough to authorize a redirect.
//
// It answers errNotCIMD for anything that is not a metadata-document URL, which
// is how the caller tells "resolve this from the table" from "this failed".
func (s *Service) resolveCIMDClient(ctx context.Context, clientID string) error {
	if err := cimdClientID(clientID); err != nil {
		return err
	}
	fresh, err := s.cimdCacheIsFresh(ctx, clientID)
	if err != nil {
		return err
	}
	if fresh {
		return nil
	}
	doc, ttl, err := fetchCIMD(ctx, clientID)
	if err != nil {
		// The operator's half. The caller is answered `invalid_client` and
		// nothing else — a detailed refusal would make this server a network
		// prober reporting back to the prober — so this log line is the ONLY
		// place the reason survives, and without it a client author whose
		// document is subtly wrong is indistinguishable from one that is not
		// registered.
		slog.WarnContext(ctx, "a client's metadata document was refused",
			"client_id", clientID, "err", err)
		return err
	}
	return s.upsertCIMDClient(ctx, doc, ttl)
}

// cimdCacheIsFresh reports whether the stored document is still believable.
//
// A DISABLED or soft-deleted client reads as "fresh" so nothing is refetched
// for it: the authorize lookup that follows applies the same liveness predicate
// every other client gets and refuses it there. Refetching would let a client an
// admin switched off keep this server making outbound requests on its behalf.
func (s *Service) cimdCacheIsFresh(ctx context.Context, clientID string) (bool, error) {
	var fresh bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT (metadata_expires_at IS NOT NULL AND metadata_expires_at > now())
			       OR disabled_at IS NOT NULL OR deleted_at IS NOT NULL
			  FROM oauth_client WHERE client_id = $1`, clientID).Scan(&fresh)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("identity: reading a client's metadata cache: %w", err)
	}
	return fresh, nil
}

// upsertCIMDClient writes what the document says, under the provenance that says
// where it came from.
//
// It never CHANGES an existing row's provenance. A DCR-registered client whose
// opaque id happened to look like a URL would otherwise be re-provenanced by
// anyone who could serve a document at that URL — and with it, its redirect
// list. The conflict target is the row this document may own or nothing.
func (s *Service) upsertCIMDClient(ctx context.Context, doc cimdDocument, ttl time.Duration) error {
	expires := time.Now().Add(ttl)
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO oauth_client (workspace_id, client_id, client_name, redirect_uris, created_via, metadata_expires_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, 'cimd', $4)
			ON CONFLICT (workspace_id, client_id) DO UPDATE SET
			  client_name         = EXCLUDED.client_name,
			  redirect_uris       = EXCLUDED.redirect_uris,
			  metadata_expires_at = EXCLUDED.metadata_expires_at
			WHERE oauth_client.created_via = 'cimd'`,
			doc.ClientID, doc.ClientName, doc.RedirectURIs, expires)
		if err != nil {
			return fmt.Errorf("identity: recording a client's metadata document: %w", err)
		}
		return nil
	})
}

// cimdClient is the HTTP client every metadata fetch rides — ONE of them, for
// the process, which is the same discipline platform/webread and
// modules/webhooks keep.
//
// Per-fetch was wrong in a way that does not look wrong: the Transport becomes
// unreachable after the call, but the idle connection it parked holds a
// readLoop goroutine that keeps it alive, and an unset IdleConnTimeout means
// nothing ever reaps it. A caller naming a fresh path per request — which costs
// it nothing, since each miss is a cache miss — leaks a socket and two
// goroutines per request until the process runs out of file descriptors.
//
// Two guards, and the second is the one that is easy to leave out. The DIALER
// refuses any non-public address, checked on the resolved IP inside the socket's
// Control hook — so a hostname that resolves to 169.254.169.254 is refused at
// connect time rather than trusted because it looked like a public name. And
// REDIRECTS ARE NOT FOLLOWED: a followed redirect is a second URL the caller
// chose, which the client_id equality check below no longer covers, and which
// would walk straight past the first guard on the next hop.
var cimdClient = func() *http.Client {
	dialer := &net.Dialer{Timeout: cimdFetchTimeout, Control: netguard.RefusePrivate}
	return &http.Client{
		Timeout: cimdFetchTimeout,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: cimdFetchTimeout,
			// Bounded on every axis, because the hosts here are named by
			// callers: an unbounded idle pool is a caller-sized pool.
			IdleConnTimeout:     30 * time.Second,
			MaxIdleConns:        16,
			MaxIdleConnsPerHost: 2,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}()

// fetchCIMD reads and validates the document at clientID, and answers how long
// it may be believed.
func fetchCIMD(ctx context.Context, clientID string) (cimdDocument, time.Duration, error) {
	//nolint:gosec // G704: dereferencing a caller-named URL IS what a metadata document is; the egress is guarded beneath — the dialer's netguard.RefusePrivate control on the resolved address, no redirect following, a 64 KiB body ceiling and a 5s timeout — not at request construction
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return cimdDocument{}, 0, fmt.Errorf("identity: building the metadata request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	//nolint:gosec // G704: see the guards named on the request above
	resp, err := cimdClient.Do(req)
	if err != nil {
		return cimdDocument{}, 0, fmt.Errorf("identity: fetching the client's metadata document: %w", err)
	}
	defer func() {
		//craft:ignore swallowed-errors closing a response body whose content is already read reports nothing actionable
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		// A redirect lands here too, by construction: CheckRedirect stops at the
		// first response, so a 30x is a status this server does not accept
		// rather than a hop it takes.
		return cimdDocument{}, 0, fmt.Errorf("identity: the client's metadata document answered %d", resp.StatusCode)
	}
	if media := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]); media != "application/json" {
		return cimdDocument{}, 0, fmt.Errorf("identity: the client's metadata document is %q, not application/json", media)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, cimdMaxDocument+1))
	if err != nil {
		return cimdDocument{}, 0, fmt.Errorf("identity: reading the client's metadata document: %w", err)
	}
	if len(body) > cimdMaxDocument {
		return cimdDocument{}, 0, errors.New("identity: the client's metadata document is larger than this server reads")
	}
	doc, err := validCIMD(body, clientID)
	if err != nil {
		return cimdDocument{}, 0, err
	}
	return doc, cimdCacheTTL(resp.Header.Get("Cache-Control")), nil
}

// validCIMD holds the document to what the profile REQUIRES of it.
//
// The equality check is the whole security of this mechanism, and it compares
// the presented client_id byte for byte against the document's own claim. Not a
// normalized form, not a parsed-and-reassembled one: a normalizer would be a
// second reading of one value, and the two readings eventually disagree — which
// is how a document published at one URL comes to speak for another.
func validCIMD(body []byte, clientID string) (cimdDocument, error) {
	var doc cimdDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return cimdDocument{}, fmt.Errorf("identity: the client's metadata document is not valid JSON: %w", err)
	}
	if doc.ClientID != clientID {
		return cimdDocument{}, errors.New(
			"identity: the client's metadata document claims a different client_id than the URL it was fetched from")
	}
	if strings.TrimSpace(doc.ClientName) == "" {
		return cimdDocument{}, errors.New("identity: the client's metadata document names no client_name")
	}
	if len(doc.RedirectURIs) == 0 {
		return cimdDocument{}, errors.New("identity: the client's metadata document lists no redirect_uris")
	}
	for _, raw := range doc.RedirectURIs {
		// The SAME predicate a DCR registration is held to. One rule for what a
		// redirect may be, whichever door the client came through — otherwise
		// CIMD becomes the way to register the redirect DCR refuses.
		if !validRedirectURI(raw) {
			return cimdDocument{}, fmt.Errorf(
				"identity: the client's metadata document lists redirect uri %q, which must be https or http on localhost", raw)
		}
	}
	return doc, nil
}

// cimdCacheTTL reads how long the client says its document may be cached,
// clamped to the bounds above. An absent or unreadable Cache-Control answers the
// floor: a client that says nothing gets the shortest life this server offers,
// never the longest.
func cimdCacheTTL(cacheControl string) time.Duration {
	for _, directive := range strings.Split(cacheControl, ",") {
		directive = strings.TrimSpace(strings.ToLower(directive))
		if directive == "no-store" || directive == "no-cache" {
			return cimdMinCache
		}
		age, found := strings.CutPrefix(directive, "max-age=")
		if !found {
			continue
		}
		seconds, err := strconv.Atoi(age)
		if err != nil {
			continue
		}
		// Clamped in SECONDS, before the multiply. A client asking for a max-age
		// near the int ceiling overflows the Duration and comes back negative,
		// which the clamp below would then read as "below the floor" — so the
		// longest cache anyone asked for would become the shortest this server
		// offers, and the document would be refetched every five minutes.
		if seconds > int(cimdMaxCache.Seconds()) {
			return cimdMaxCache
		}
		return min(max(time.Duration(seconds)*time.Second, cimdMinCache), cimdMaxCache)
	}
	return cimdMinCache
}
