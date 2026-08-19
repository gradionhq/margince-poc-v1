// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// HTTP plumbing: the serializable cookie jar (a personal session IS its
// cookies, so they have to survive a process restart), the shared request
// helper, and the URL builder that stamps every call with the API version pair.

import (
	"context"
	//nolint:gosec // G501: MD5 is Zalo Web's wire format, not a choice — the device IMEI is MD5(userAgent), transcribed from zca-js src/utils.ts (PROTOCOL.md §"Layer 2"). Changing the hash changes the device identity, which silently invalidates every sealed session at its next handshake.
	"crypto/md5"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	apiType    = 30
	apiVersion = 689

	// defaultUserAgent is CREDENTIAL MATERIAL, not a header preference: the
	// imei is MD5(userAgent), so a session's identity is derived from the exact
	// string that was in force when it was minted. Changing this constant does
	// not "modernise a header" — it invalidates every session sealed under the
	// old value, silently, at the next handshake. Which is why [zaloSealed] carries
	// its own user agent and this constant only seeds a NEW login.
	defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0"

	// defaultLanguage is what the login params declare; Zalo echoes it back in
	// server-composed strings.
	defaultLanguage = "vi"

	// maxBodyBytes caps what this package will read from any one response. The
	// login page is the largest of them at a few hundred kilobytes.
	maxBodyBytes = 1 << 20
)

// zaloHosts is the complete set of domains this layer will speak to, store a
// credential for, or follow a redirect into. Everything else about this file
// answers to it, because "we only ever talk to Zalo" is ONE invariant and three
// separate spellings of it would drift apart.
//
// Every entry is a domain the live protocol capture actually shows: the session
// spans `zalo.me`, and Zalo mirrors cookies onto `zaloapp.com`, `zalo.gg` and
// `zalo.cx` while its own service map hands out `*.zaloapp.com` hosts. Nothing
// here is speculative — a domain this layer never dials does not belong in an
// allowlist, since the list's whole value is that it is exhaustive.
//
// The list also subsumes a public-suffix check without needing one: `me` is
// neither equal to nor a subdomain of any entry, so `Domain=me` — the attribute
// a hostile response would use to claim every `.me` site at once — cannot match.
var zaloHosts = [...]string{"zalo.me", "zaloapp.com", "zalo.gg", "zalo.cx"}

// isZaloHost reports whether host is one of Zalo's own, matching on a LABEL
// boundary so `evilzalo.me` and `zalo.me.attacker.example` are both outside it.
func isZaloHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, domain := range zaloHosts {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// isTLSScheme reports whether a cookie-bearing request on this scheme is
// encrypted. Two schemes qualify, and they are the two this layer speaks:
// `https` for every REST call and `wss` for the message socket, whose handshake
// is hand-built and asks the jar for its header directly.
func isTLSScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "https", "wss":
		return true
	default:
		return false
	}
}

// cookieScope resolves the domain a Set-Cookie may claim, or reports that it may
// claim none.
//
// A response is an untrusted party, and the Domain attribute is that party
// choosing where its cookie will be sent BACK to. Taking it as written lets one
// response widen a session cookie's scope to a domain it does not own — and,
// paired with a redirect, hand the member's live Zalo session to whoever asked.
// So a claimed scope has to be both a domain Zalo owns and one this response's
// own host sits inside.
func cookieScope(u *url.URL, c *http.Cookie) (string, bool) {
	host := strings.ToLower(u.Hostname())
	if c.Domain == "" {
		// No attribute means host-scoped, which is self-evidently host-related.
		return host, isZaloHost(host)
	}

	domain := strings.ToLower(strings.TrimPrefix(c.Domain, "."))
	if !isZaloHost(domain) {
		return "", false
	}
	if host != domain && !strings.HasSuffix(host, "."+domain) {
		return "", false
	}
	return domain, true
}

// zaloCookie is one persisted cookie, flattened for JSON. net/http's own jar cannot
// be serialized, and a sealed credential that cannot be written down is not a
// credential a connector can hold.
type zaloCookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
}

// jar is a deliberately small http.CookieJar: match on domain suffix, ignore
// the public-suffix list, and ignore expiry EXCEPT for deletion. Zalo spreads
// the session across `.zalo.me`, `chat.zalo.me` and `id.zalo.me`, so a cookie
// must not be dropped between hosts mid-login — but the mirror failure is the
// expensive one: a cleared cookie kept at a broader scope shadows the live one
// and the server rejects the whole session.
type jar struct {
	mu      sync.Mutex
	cookies map[string]map[string]zaloCookie // domain -> name -> cookie
	now     func() time.Time
}

func newJar(now func() time.Time) *jar {
	return &jar{cookies: map[string]map[string]zaloCookie{}, now: now}
}

func (j *jar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()

	for _, c := range cookies {
		domain, ok := cookieScope(u, c)
		if !ok {
			// Dropped in silence on purpose: this is an untrusted party being
			// told no, not an error in anything we did, and there is no caller
			// whose behaviour should change because a response overreached.
			continue
		}
		path := c.Path
		if path == "" {
			path = "/"
		}
		// A clear is not a cookie. Zalo hands out the live `zpw_sek` on
		// chat.zalo.me while clearing the same name at .zalo.me in the same
		// response; keeping the clear means every later request carries two
		// `zpw_sek` values and the server answers error_code 102 — which reads
		// as an expired session and never as a jar bug.
		if isCleared(c, j.now()) {
			delete(j.cookies[domain], c.Name)
			continue
		}

		if _, ok := j.cookies[domain]; !ok {
			j.cookies[domain] = map[string]zaloCookie{}
		}
		j.cookies[domain][c.Name] = zaloCookie{Name: c.Name, Value: c.Value, Domain: domain, Path: path}
	}
}

// isCleared reports the three ways a server says "forget this cookie". Expiry
// is otherwise ignored by this jar on purpose — a session cookie has no
// lifetime we could honour — so deletion is the one expiry case that counts.
func isCleared(c *http.Cookie, now time.Time) bool {
	return c.Value == "" || c.MaxAge < 0 || (!c.Expires.IsZero() && c.Expires.Before(now))
}

func (j *jar) Cookies(u *url.URL) []*http.Cookie {
	// Every cookie in here is live session credential, so the TRANSPORT is part
	// of a cookie's scope: a plaintext request puts `zpw_sek` on the wire for
	// anyone on the path. Zalo is HTTPS-only and its socket is wss, so the rule
	// belongs to the jar rather than to a Secure attribute some response would
	// forget — which also makes it hold for a URL nobody here audited, since the
	// hosts in `zpw_service_map_v3` are chosen by the server, not by this source.
	if !isTLSScheme(u.Scheme) {
		return nil
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	host := u.Hostname()

	var domains []string
	for domain := range j.cookies {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			domains = append(domains, domain)
		}
	}
	// Most specific scope first, as RFC 6265 asks. Two scopes may legitimately
	// carry the same name, and which one the server reads first must not depend
	// on Go's map iteration order.
	sort.Slice(domains, func(a, b int) bool {
		if len(domains[a]) != len(domains[b]) {
			return len(domains[a]) > len(domains[b])
		}
		return domains[a] < domains[b]
	})

	var out []*http.Cookie
	for _, domain := range domains {
		byName := j.cookies[domain]
		names := make([]string, 0, len(byName))
		for name := range byName {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			// Secure, HttpOnly and SameSite are Set-Cookie RESPONSE attributes.
			// This is the jar's read side: net/http renders these into an
			// outgoing `Cookie:` request header, which by RFC 6265 §4.2.1
			// carries name=value pairs and nothing else. Setting the flags here
			// would change no byte on the wire.
			//nolint:gosec // G124: a client-side request cookie has no Secure/HttpOnly/SameSite to set — those attributes exist only on a server's Set-Cookie response.
			out = append(out, &http.Cookie{Name: name, Value: byName[name].Value})
		}
	}
	return out
}

func (j *jar) export() []zaloCookie {
	j.mu.Lock()
	defer j.mu.Unlock()

	out := make([]zaloCookie, 0, len(j.cookies))
	domains := make([]string, 0, len(j.cookies))
	for domain := range j.cookies {
		domains = append(domains, domain)
	}
	// Sorted so a re-seal of an unchanged jar produces an unchanged blob; a
	// credential whose bytes churn on every read looks rotated when it is not.
	sort.Strings(domains)
	for _, domain := range domains {
		byName := j.cookies[domain]
		names := make([]string, 0, len(byName))
		for name := range byName {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			out = append(out, byName[name])
		}
	}
	return out
}

func (j *jar) load(cookies []zaloCookie) {
	j.mu.Lock()
	defer j.mu.Unlock()

	for _, c := range cookies {
		// A blob sealed before the jar honoured deletion still holds cleared
		// cookies; they must not come back to life on restore.
		if c.Value == "" {
			continue
		}
		domain := c.Domain
		if domain == "" {
			continue
		}
		if _, ok := j.cookies[domain]; !ok {
			j.cookies[domain] = map[string]zaloCookie{}
		}
		j.cookies[domain][c.Name] = c
	}
}

// cookieString renders the jar the way a browser would for one origin. The
// websocket handshake needs it: that request is hand-built rather than issued
// through this client, so the header is not applied by the transport.
func (j *jar) cookieString(rawURL string) (string, error) {
	safe := safeURL(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", safe, err)
	}
	sending := j.Cookies(u)
	parts := make([]string, 0, len(sending))
	for _, c := range sending {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; "), nil
}

type client struct {
	http      *http.Client
	jar       *jar
	userAgent string
	now       func() time.Time
}

func newClient(opts zaloOptions) *client {
	j := newJar(opts.clock())
	return &client{
		http: &http.Client{
			Jar:       j,
			Transport: opts.transport(),
			// Zalo's login chain redirects across id.zalo.me and chat.zalo.me,
			// and the referer it expects on the way is id.zalo.me regardless of
			// where the hop came from.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("stopped after 10 redirects")
				}
				// A hop's destination is chosen by the response, and this
				// client carries the member's live session cookies on every
				// one. Following an off-allowlist Location would hand that
				// session to whoever named it.
				if !isZaloHost(req.URL.Hostname()) {
					return fmt.Errorf("refusing to follow a redirect to %s: it is not a Zalo host, and this request carries the member's session", req.URL.Hostname())
				}
				// Downgrading the scheme is something a response gets to
				// propose. The jar refuses to attach cookies to a plaintext
				// hop; refusing the hop too means the request does not instead
				// proceed silently stripped of its session.
				if !isTLSScheme(req.URL.Scheme) {
					return fmt.Errorf("refusing to follow a redirect to a %s:// URL: this request carries the member's session and Zalo is HTTPS-only", req.URL.Scheme)
				}
				req.Header.Set("Referer", "https://id.zalo.me/")
				return nil
			},
		},
		jar:       j,
		userAgent: opts.userAgent(),
		now:       opts.clock(),
	}
}

// do issues a request with the browser-shaped headers Zalo Web sends. Zalo
// serves an HTML challenge page instead of JSON when the headers look wrong,
// which surfaces as a JSON parse error far from the cause.
func (c *client) do(ctx context.Context, method, rawURL string, body io.Reader, headers map[string]string) (*http.Response, error) {
	// Named once, at the top, so no error path below can reach the raw URL by
	// accident. This layer's secrets ARE query parameters, and the leak this
	// prevents has already been introduced twice by adding an error path that
	// formatted the obvious variable — TestNoErrorInThisFileCanCarryTheRawURL
	// is what makes the third time fail in CI instead of in a log.
	safe := safeURL(rawURL)

	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", method, safe, err)
	}

	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "vi-VN,vi;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Origin", "https://chat.zalo.me")
	req.Header.Set("Referer", "https://chat.zalo.me/")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, unreachable(method, safe, err)
	}
	return resp, nil
}

// doJSON runs a request and returns its body. Everything it can fail on —
// a dead connection, a truncated read, a status line that is not 200 — means
// the same thing to a caller: no answer from this API was read, so the outcome
// is unknown rather than refused. All of it is a transportError.
func (c *client) doJSON(ctx context.Context, method, rawURL string, body io.Reader, headers map[string]string) ([]byte, error) {
	safe := safeURL(rawURL)

	resp, err := c.do(ctx, method, rawURL, body, headers)
	if err != nil {
		return nil, err
	}
	//craft:ignore swallowed-errors best-effort close: the capped read below may leave the body mid-stream, so a close error carries no signal for this call's result
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, unreachable(method, safe, fmt.Errorf("read body: %w", err))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, unreachable(method, safe,
			fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200)))
	}
	return raw, nil
}

// makeURL appends params and, unless told otherwise, the version pair every
// authenticated endpoint expects.
func makeURL(base string, params map[string]string, withAPIVersion bool) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", base, err)
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	if withAPIVersion {
		if !q.Has("zpw_ver") {
			q.Set("zpw_ver", fmt.Sprint(apiVersion))
		}
		if !q.Has("zpw_type") {
			q.Set("zpw_type", fmt.Sprint(apiType))
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// newIMEI mints the device identity: a random UUID joined to the MD5 of the
// user agent. The user agent half is why the agent string is sealed alongside
// it — see defaultUserAgent.
func newIMEI(userAgent string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80

	uuid := fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
	//nolint:gosec // G401: the imei's second half IS MD5(userAgent) by Zalo's definition — this digest is an identifier the server recomputes and compares, never a secret, and a different hash is a different device.
	return uuid + "-" + fmt.Sprintf("%x", md5.Sum([]byte(userAgent))), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
