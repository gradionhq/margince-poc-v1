// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The transport half of the 2026-07-28 framing: the headers a modern POST
// mirrors its body into, and the HTTP status each answer carries.
//
// Why mirroring is checked at all, rather than trusted or ignored: the headers
// exist so an intermediary can route and rate-limit without parsing the body,
// and the body remains what this server executes. A request whose header says
// one tool and whose body calls another is the shape where those two readings
// part company — a gateway allows what a server then does not run, or the
// reverse. The specification closes it by making the server compare, and
// answer -32020 when they disagree.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

// The mirrored headers. Case is irrelevant on the wire (RFC 9110) and
// http.Header canonicalizes on both sides of Get, so these are the canonical
// spellings rather than a claim about what a client sends.
const (
	headerProtocolVersion = "MCP-Protocol-Version"
	headerMethod          = "Mcp-Method"
	headerName            = "Mcp-Name"
)

// The sentinel that wraps a header value a client had to Base64-encode —
// because it is not plain ASCII, is padded with whitespace, or would itself
// have looked like this sentinel.
const (
	base64SentinelPrefix = "=?base64?"
	base64SentinelSuffix = "?="
)

// modernNamedMethods maps each method whose body carries a name to the params
// member holding it, which is what Mcp-Name mirrors.
//
// It lists the name-carrying methods THIS server answers. The specification
// also names prompts/get; this server has no prompts and answers that -32601,
// and demanding a header for a method that does not exist would refuse the
// caller for the wrong reason.
var modernNamedMethods = map[string]string{
	methodToolsCall:     "name",
	methodResourcesRead: "uri",
}

// validateModernHeaders holds a modern request to the mirroring contract: the
// required headers are present, and each says the same thing its body does.
//
// It answers a refusal rather than writing one, so the transport keeps the one
// place that decides a status. It runs AFTER the body precheck because the
// body is the source of truth — a request that has not yet declared a version
// has nothing for a header to disagree with.
//
// No refusal echoes the value it read. The header is caller-controlled and
// its length is not, and naming the header and the member it contradicts is
// what a client author acts on anyway.
func validateModernHeaders(header http.Header, req rpcRequest, fr framing) *rpcError {
	if header.Get(headerProtocolVersion) != fr.version {
		return headerMismatch(headerProtocolVersion, "_meta["+metaProtocolVersion+"]")
	}
	if header.Get(headerMethod) != req.Method {
		return headerMismatch(headerMethod, "method")
	}
	member, named := modernNamedMethods[req.Method]
	if !named {
		return nil
	}
	return validateMirroredName(header.Get(headerName), req.Params, member)
}

// validateMirroredName compares the Mcp-Name header with the params member it
// mirrors. Absent on both sides is admissible: a body that names nothing is
// answered by the dispatcher, which can say what is actually missing, and a
// header requirement here would report the wrong fault.
func validateMirroredName(presented string, params json.RawMessage, member string) *rpcError {
	inBody := paramsMember(params, member)
	if presented == "" && inBody == "" {
		return nil
	}
	decoded, ok := decodeHeaderValue(presented)
	if !ok || decoded != inBody {
		return headerMismatch(headerName, "params."+member)
	}
	return nil
}

// paramsMember reads one string member out of a request's params, answering
// the empty string when the params are absent, are not an object, or hold
// something other than a string there. Each of those is a body that does not
// name what the header claims, which is the only question this file asks.
func paramsMember(params json.RawMessage, member string) string {
	if len(params) == 0 {
		return ""
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(params, &members); err != nil {
		return ""
	}
	var value string
	if err := json.Unmarshal(members[member], &value); err != nil {
		return ""
	}
	return value
}

// decodeHeaderValue resolves the Base64 sentinel a client must use for any
// value that cannot travel as plain ASCII, and reports whether the value could
// be read at all.
//
// A value that merely looks like the sentinel is not treated as a literal:
// clients are required to encode even a plain-ASCII value that matches the
// pattern, so an undecodable one is a malformed header rather than a name that
// happens to spell it.
func decodeHeaderValue(presented string) (string, bool) {
	payload, wrapped := strings.CutPrefix(presented, base64SentinelPrefix)
	if !wrapped {
		return presented, true
	}
	payload, wrapped = strings.CutSuffix(payload, base64SentinelSuffix)
	if !wrapped {
		return presented, true
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", false
	}
	return string(decoded), true
}

func headerMismatch(header, member string) *rpcError {
	return &rpcError{
		Code: codeHeaderMismatch,
		Message: "header mismatch: " + header + " is missing or does not match " + member +
			" in the request body, which is what this server executes",
	}
}

// modernStatus is the HTTP status one dispatched modern response carries. An
// unknown method answers 404 with its JSON-RPC error, which is how a dual-era
// client tells a modern server that lacks the method from a legacy server that
// does not host the endpoint at all.
func modernStatus(resp rpcResponse) int {
	if resp.Error != nil && resp.Error.Code == codeMethodNotFound {
		return http.StatusNotFound
	}
	return http.StatusOK
}
