// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Where a sovereign deployment's local bindings are allowed to point
// (ai-operational-spec §4.3).
//
// The profile check reads the provider NAME, and `ollama` and `vllm` both take
// an operator-supplied base_url with nothing constraining it. So a deployment
// could declare zero egress and send every call to a third-party host, while the
// config validated and the code claimed the guarantee held by construction.
//
// A local provider name is not on its own a local endpoint, and this is the
// other half.

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// localBaseURLDefaults is the endpoint each local provider resolves to when the
// binding names none. Read here rather than re-typed, so "an omitted base_url is
// loopback" cannot become true in the client and false in the check.
//
// `fake` is absent on purpose: it is sovereign-eligible and reaches no endpoint
// at all, so there is nothing about it to check.
var localBaseURLDefaults = map[string]string{
	providerOllama: defaultOllamaBaseURL,
	providerVLLM:   defaultVLLMBaseURL,
}

// requireSovereignEndpoint refuses a binding whose resolved endpoint is not on
// infrastructure the customer controls.
//
// label names the binding under inspection ("tier premium", "the embeddings
// lane") so the error points at a line rather than at the file.
func requireSovereignEndpoint(label, provider, baseURL string) error {
	fallback, reachesAnEndpoint := localBaseURLDefaults[provider]
	if !reachesAnEndpoint {
		// fake (no endpoint at all), or a cloud provider the caller already
		// refused. TestEveryLocalProviderWithAnEndpointIsChecked holds this
		// closed: a new local provider absent from the defaults map would
		// otherwise pass unchecked, which is this rule's own failure mode.
		return nil
	}
	host, err := hostOf(defaulted(baseURL, fallback))
	if err != nil {
		return fmt.Errorf("ai: routing config: %s: %w", label, err)
	}
	switch classifyHost(host) {
	case hostIsLocal:
		return nil
	case hostIsAName:
		// Distinguished from a public address on purpose: the operator running
		// under Kubernetes or Compose writes a service name, and an error about
		// private ranges reads to them as "your host is public" — which it may
		// not be. What is wrong is that it is a NAME.
		return fmt.Errorf(
			"ai: routing config: %s: profile sovereign needs an endpoint this installation can verify for itself, and %q is a name — what it resolves to is decided elsewhere and can change after boot. Give the address instead (an IP in a private range, or a loopback address); `localhost` is also accepted",
			label, host)
	}
	return fmt.Errorf(
		"ai: routing config: %s: profile sovereign forbids the host %q — the profile promises zero egress, and that address is not on infrastructure this installation can see is yours. Point it at loopback, a link-local address, or a private range (10.x, 172.16-31.x, 192.168.x, or an IPv6 unique-local address)",
		label, host)
}

// hostOf extracts the host a base_url names, refusing a value that names none —
// which on this path is not a formatting nit: "no host" is exactly the shape a
// check written as a string comparison would wave through. `localhost:11434`
// lands here too, because a url with no scheme parses as one whose SCHEME is
// "localhost", so the error names the shape rather than the omission.
func hostOf(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		// The value is NOT echoed. A base_url may carry userinfo
		// (http://user:token@host), and this error reaches a boot log — for a
		// binding declared in margince.yaml, from the file whose whole promise
		// is that it carries no credential. url.Parse's own message names the
		// syntax fault without the string.
		// NOT the raw error: url.Error quotes the WHOLE input in its own
		// Error(), so wrapping it would put back exactly what omitting the
		// value was meant to keep out. Its .Err is the syntax fault alone.
		return "", fmt.Errorf("base_url cannot be parsed as a url: %w", parseFault(err))
	}
	host := parsed.Hostname()
	if host == "" {
		// Redacted for the same reason: Redacted() replaces any password with
		// xxxxx, and a value with no host is exactly the malformed shape most
		// likely to have been pasted with a credential still in it.
		return "", fmt.Errorf("base_url %q names no host; write the whole url, e.g. http://127.0.0.1:11434", parsed.Redacted())
	}
	// The scheme is checked HERE rather than left to the first call: a scheme
	// this adapter cannot dial makes the endpoint unreachable, and an endpoint
	// nothing can reach is not the local one the profile was promised — it is a
	// deployment that fails at 3am with a transport error instead of at boot
	// with a config one.
	if scheme := strings.ToLower(parsed.Scheme); scheme != "http" && scheme != "https" {
		// Redacted, like the two branches above: a scheme this adapter cannot
		// dial is a malformed value, and a malformed value is the shape most
		// likely to have been pasted with a credential still in it. The scheme
		// itself is safe to name and is what the operator has to change.
		return "", fmt.Errorf("base_url %q must be an http(s) url; %q is not a scheme this adapter can call", parsed.Redacted(), parsed.Scheme)
	}
	return host, nil
}

// What a base_url's host is, from the profile's point of view. Three answers
// rather than a bool, because "somebody else's address" and "an address nobody
// here can resolve" are different mistakes and lead an operator to different
// fixes.
type hostVerdict int

const (
	hostIsLocal hostVerdict = iota
	hostIsElsewhere
	hostIsAName
)

// classifyHost answers the question the profile actually asks: does this address
// live on the customer's own infrastructure?
//
// A private-range host on ANOTHER machine counts. A customer's own GPU box on
// their own network is their infrastructure — the guarantee is about where data
// goes, not about which process it lands in (spec §4.3).
//
// Only a host this installation can judge for ITSELF is local: an IP literal, or
// `localhost`, which RFC 6761 reserves for loopback. A DNS name is refused even
// though resolving one would be easy, because resolving it at boot says only
// where it pointed at boot — and a profile satisfied by an answer that can
// change an hour later is the same false guarantee this check exists to remove.
func classifyHost(host string) hostVerdict {
	// The trailing dot is dropped for the NAME check only, where it is the
	// fully-qualified spelling of the same reserved name. It must NOT be dropped
	// before parsing an address: `127.0.0.1.` is not an IP literal to Go's
	// resolver either, so a dial to it goes to DNS — and trimming here would call
	// local an endpoint the dial then resolves somewhere else entirely.
	if isReservedLoopbackName(strings.TrimSuffix(host, ".")) {
		return hostIsLocal
	}
	// A zone ("fe80::1%eth0") says which interface a link-local address is
	// reached on, and net.ParseIP does not take one. Dropped for the judgment,
	// which is about the address: an interface cannot make a link-local address
	// non-local.
	address, _, _ := strings.Cut(host, "%")
	ip := net.ParseIP(address)
	if ip == nil {
		return hostIsAName
	}
	// IsPrivate covers RFC 1918 and IPv6 unique-local (fc00::/7); the other two
	// carry loopback and the link-local ranges an on-host or same-segment
	// deployment uses. An IPv4-mapped IPv6 address is judged by its IPv4 rules,
	// which is what the net package's own predicates do — so ::ffff:8.8.8.8 is
	// as public as 8.8.8.8, and the mapping buys nothing.
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return hostIsLocal
	}
	return hostIsElsewhere
}

// isReservedLoopbackName reports whether host is a name the standards themselves
// pin to loopback: `localhost` and anything under it (RFC 6761 §6.3). Matched
// case-insensitively, because host names are.
func isReservedLoopbackName(host string) bool {
	lowered := strings.ToLower(host)
	return lowered == "localhost" || strings.HasSuffix(lowered, ".localhost")
}

// parseFault is url.Parse's diagnosis without its subject.
//
// url.Error.Error() renders as `parse "<the whole input>": <fault>`, so every
// error from url.Parse carries the value that produced it. Unwrapping to .Err
// keeps the fault — "invalid control character in URL" — and drops the string,
// which on this path may be a credential.
func parseFault(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}
