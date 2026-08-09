// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Extension negotiation in the modern framing.
//
// An extension is not a feature this server turns on: it is something the CLIENT
// and the server each declare, per request, and only what both of them named is
// in play for that call. modern.go owns which ERA a request is in; this file
// owns what a request declared WITHIN that era, and what a method whose contract
// depends on an undeclared one answers.
//
// One extension exists today (io.modelcontextprotocol/tasks). The shape here is
// still per-extension rather than a single boolean, because the negotiation is
// the same mechanism for every one of them and the second would otherwise be
// bolted onto the first's spelling.

import "encoding/json"

// extensionTasks is the Tasks extension's identifier, used in three places that
// must agree: what a client declares in its per-request capabilities, what
// server/discover advertises, and what a -32021 refusal names as missing. One
// spelling, because a typo in the first would silently read every client as
// unable to hold a task handle.
const extensionTasks = "io.modelcontextprotocol/tasks"

// declaresTasks reports whether this request's capabilities declare the Tasks
// extension.
//
// It fails CLOSED, and the asymmetry is the point: reading a declaration that
// is not there hands a client a task handle it will never poll, stranding an
// approved effect that the human believes they released. Missing one that IS
// there costs the caller the plain refusal every client already handles. So
// anything but a JSON object under the extension's own key — absent, null, a
// string, a capabilities object with no `extensions` member at all — is read as
// "not declared".
func declaresTasks(capabilities json.RawMessage) bool {
	var declared struct {
		Extensions map[string]json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(capabilities, &declared); err != nil {
		return false
	}
	return isJSONObject(declared.Extensions[extensionTasks])
}

// missingClientCapability refuses a method whose whole contract depends on a
// capability this request did not declare. `data.requiredCapabilities` carries
// the declaration the client would have to send, in the shape it would send it,
// so a client author reads the fix rather than deriving it.
func missingClientCapability(method string) *rpcError {
	return &rpcError{
		Code: codeMissingClientCapability,
		Message: method + " is part of the " + extensionTasks + " extension, and this request did not " +
			"declare it: send it in _meta[" + metaClientCapabilities + "].extensions on every request that uses it",
		Data: &rpcErrorData{RequiredCapabilities: &requiredCapabilities{
			Extensions: map[string]struct{}{extensionTasks: {}},
		}},
	}
}

// resultTyped is implemented by a result whose modern resultType is not
// "complete". Everything else is complete by default, so a handler says nothing
// and gets the right answer.
type resultTyped interface{ resultType() string }

// modernResultTypeOf answers the discriminator one result claims for itself.
// Everything is "complete" unless it says otherwise, so a handler that has
// nothing to declare declares nothing and still gets the required member.
//
//craft:ignore naked-any it reads rpcResponse.Result, which every handler on this surface fills with its own shape — the whole point is to ask an unknown result what it is
func modernResultTypeOf(result any) string {
	if typed, ok := result.(resultTyped); ok {
		return typed.resultType()
	}
	return resultTypeComplete
}
