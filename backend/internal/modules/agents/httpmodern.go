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

// The mirrored headers, in the specification's own spelling — which is NOT
// Go's canonical form for the first of them (textproto canonicalizes it to
// Mcp-Protocol-Version). That difference is invisible because every read here
// goes through http.Header.Get, which canonicalizes its argument; a direct map
// index on one of these would silently miss.
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

// mirroredName is how one method's name is read out of its params — and it is
// deliberately the SAME reading its handler makes, not an equivalent one.
//
// This is the whole safety of the mirror. A map lookup and a struct decode do
// not agree on a JSON object: encoding/json matches members case-insensitively
// and takes the LAST of a duplicate pair, so a body carrying both
// {"name":"harmless","NAME":"read_record"} reads as `harmless` to a map and as
// `read_record` to the handler. Comparing the header against the first would
// admit exactly the request the mirror exists to refuse — a gateway allowing
// one tool while the server runs another.
type mirroredName func(json.RawMessage) string

// modernNamedMethods maps each method whose body carries a name to that reader.
//
// It lists the name-carrying methods THIS server answers. The specification
// also names prompts/get; this server has no prompts and answers that -32601,
// and demanding a header for a method that does not exist would refuse the
// caller for the wrong reason.
// The three task methods mirror their taskId, which the Tasks extension makes
// a MUST for a different reason than the two above: an intermediary routing on
// Mcp-Name can send a poll to the instance holding that task's state. This
// server holds it in Postgres, so any replica can answer — but the header is
// still required of the client, and a server that skipped the comparison would
// let a gateway route on one task while it read another.
var modernNamedMethods = map[string]mirroredName{
	methodToolsCall:     calledToolName,
	methodResourcesRead: readResourceURI,
	methodTasksGet:      taskIDName,
	methodTasksUpdate:   taskIDName,
	methodTasksCancel:   taskIDName,
}

// calledToolName reads the tool a tools/call body invokes, through the same
// shape Dispatcher.call decodes.
func calledToolName(params json.RawMessage) string {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return ""
	}
	return p.Name
}

// readResourceURI reads the document a resources/read body asks for, through
// the same shape Dispatcher.readResource decodes.
func readResourceURI(params json.RawMessage) string {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return ""
	}
	return p.URI
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
	// A mirrored header sent TWICE is the same defect as a body member sent
	// twice, one layer up: Get answers the first value while an intermediary
	// may route on the last, so the two readings can disagree with nothing on
	// the wire to say which was meant. One value or none.
	for _, mirrored := range []string{headerProtocolVersion, headerMethod, headerName} {
		if len(header.Values(mirrored)) > 1 {
			return &rpcError{
				Code: codeHeaderMismatch,
				Message: "header mismatch: " + mirrored + " was sent more than once, so this server " +
					"and an intermediary between us could read different values from it",
			}
		}
	}
	if header.Get(headerProtocolVersion) != fr.version {
		return headerMismatch(headerProtocolVersion, "_meta["+metaProtocolVersion+"]")
	}
	if header.Get(headerMethod) != req.Method {
		return headerMismatch(headerMethod, "method")
	}
	readName, named := modernNamedMethods[req.Method]
	if !named {
		// A method that mirrors no name must present none. A header naming a
		// tool on a call that invokes none tells an intermediary metering or
		// filtering on Mcp-Name about an invocation that never happens.
		if header.Get(headerName) != "" {
			return &rpcError{
				Code: codeHeaderMismatch,
				Message: "header mismatch: " + headerName + " names something on a method whose body " +
					"names nothing, so it describes a call this server will not make",
			}
		}
		return nil
	}
	return validateMirroredName(header.Get(headerName), req.Params, readName)
}

// validateMirroredName compares the Mcp-Name header with the name its method's
// handler will act on.
//
// The header is REQUIRED on every one of these methods, not merely when the
// body happens to name something: an absent one is a validation failure in its
// own right. A body that names nothing therefore cannot be rescued by a client
// that also sends nothing — and it would be malformed anyway, since the name is
// the one param each of these methods cannot be called without.
func validateMirroredName(presented string, params json.RawMessage, readName mirroredName) *rpcError {
	decoded, ok := decodeHeaderValue(presented)
	// The DECODED value carries the emptiness test, not the presented one:
	// `=?base64??=` is a non-empty header that decodes to nothing, and it would
	// otherwise agree with a body that names nothing — leaving an intermediary
	// metering an empty name the mirror was supposed to have refused.
	if !ok || decoded == "" || decoded != readName(params) {
		return headerMismatch(headerName, "the name its own body invokes")
	}
	return nil
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
		Message: "header mismatch: " + header + " is missing or disagrees with " + member +
			", and the body is what this server executes",
	}
}

// duplicatedVersionHeader reports whether the transport named more than one
// protocol version.
//
// Nothing on this surface acts on such a request, in either era. Get answers
// the first value while an intermediary may route on the last, so a version
// header sent twice has two readings and no way to say which was meant — and
// that is as true of a handshake-era request as of a modern one, which is why
// this is checked before the era is decided rather than inside one framing.
func duplicatedVersionHeader(header http.Header) bool {
	return len(header.Values(headerProtocolVersion)) > 1
}

// declaredTransportVersion is the protocol version the transport names, and
// the empty string when it names more than one.
//
// It is the same rule as above, for the one path that runs BEFORE the refusal:
// a body that does not decode is answered without ever reaching the era check,
// and a duplicated header must not select a framing's status there either.
func declaredTransportVersion(header http.Header) string {
	if duplicatedVersionHeader(header) {
		return ""
	}
	return header.Get(headerProtocolVersion)
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
