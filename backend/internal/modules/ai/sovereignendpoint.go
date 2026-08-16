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
	fallback, isLocal := localBaseURLDefaults[provider]
	if !isLocal {
		return nil // fake, or a cloud provider the caller already refused
	}
	host, err := hostOf(defaulted(baseURL, fallback))
	if err != nil {
		return fmt.Errorf("ai: routing config: %s: %w", label, err)
	}
	if hostIsCustomerControlled(host) {
		return nil
	}
	return fmt.Errorf(
		"ai: routing config: %s: profile sovereign forbids the host %q — the profile promises zero egress, and %q is not an address on infrastructure this installation can see is yours; point it at loopback, a link-local address, or a private range (10.x, 172.16-31.x, 192.168.x, or an IPv6 unique-local address)",
		label, host, host)
}

// hostOf extracts the host a base_url names, refusing a value that names none —
// which on this path is not a formatting nit: "no host" is exactly the shape a
// check written as a string comparison would wave through.
func hostOf(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("base_url %q cannot be parsed as a url: %w", baseURL, err)
	}
	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("base_url %q names no host", baseURL)
	}
	return host, nil
}

// hostIsCustomerControlled answers the question the profile actually asks: does
// this address live on the customer's own infrastructure?
//
// A private-range host on ANOTHER machine counts. A customer's own GPU box on
// their own network is their infrastructure — the guarantee is about where data
// goes, not about which process it lands in (spec §4.3).
//
// Only a host this installation can judge for ITSELF is accepted: an IP literal,
// or `localhost`, which RFC 6761 reserves for loopback. A DNS name is refused
// even though resolving one would be easy, because resolving it at boot says
// only where it pointed at boot — and a profile satisfied by an answer that can
// change an hour later is the same false guarantee this check exists to remove.
func hostIsCustomerControlled(host string) bool {
	if isReservedLoopbackName(host) {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false // a DNS name: unverifiable here, and mutable later
	}
	// IsPrivate covers RFC 1918 and IPv6 unique-local (fc00::/7); the other two
	// carry loopback and the link-local ranges an on-host or same-segment
	// deployment uses.
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// isReservedLoopbackName reports whether host is a name the standards themselves
// pin to loopback: `localhost` and anything under it (RFC 6761 §6.3). Matched
// case-insensitively, because host names are.
func isReservedLoopbackName(host string) bool {
	lowered := strings.ToLower(host)
	return lowered == "localhost" || strings.HasSuffix(lowered, ".localhost")
}
