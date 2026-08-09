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
// TWO extensions exist today (io.modelcontextprotocol/tasks and
// io.modelcontextprotocol/ui), and they share one reader. The per-extension
// wrappers stay, because each names the consequence of getting ITS answer
// wrong, and those consequences are not the same.

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
// there costs the caller the plain refusal every client already handles.
func declaresTasks(capabilities json.RawMessage) bool {
	return declaresExtension(capabilities, extensionTasks)
}

// declaresExtension reports whether this request's capabilities declare one
// named extension, and is the shared mechanism every extension negotiates
// through.
//
// It fails CLOSED for every one of them: anything but a JSON object under the
// extension's own key — absent, null, a string, a capabilities object with no
// `extensions` member at all — is read as "not declared". Each caller documents
// what its own false costs.
//
// The extension name is a PARAMETER rather than a set of hand-spelled readers,
// so the exactness below is written once. It was one reader when only Tasks
// existed, and the second extension would otherwise have been bolted onto the
// first's spelling of it.
func declaresExtension(capabilities json.RawMessage, extension string) bool {
	_, present := declaredExtension(capabilities, extension)
	return present
}

// declaredExtension answers the declaration BODY as well as its presence, for an
// extension whose negotiation carries a payload rather than being a bare
// acknowledgement — the App extension declares the content types the client can
// render, and a caller that only knew "it was declared" could not check them.
//
// present is false for everything declaresExtension refuses, and the returned
// bytes are meaningless then.
func declaredExtension(capabilities json.RawMessage, extension string) (json.RawMessage, bool) {
	// Decoded through a MAP, and matched exactly, for the reason metaOf gives
	// about the reserved `_meta` keys: encoding/json matches struct members
	// case-insensitively, so a struct field would read `"Extensions"` — or
	// `"EXTENSIONS"` — as a declaration. A client that mis-cased it would be
	// handed a capability it never claimed to understand.
	var declared map[string]json.RawMessage
	if err := json.Unmarshal(capabilities, &declared); err != nil {
		return nil, false
	}
	var extensions map[string]json.RawMessage
	if err := json.Unmarshal(declared["extensions"], &extensions); err != nil {
		return nil, false
	}
	body := extensions[extension]
	if !isJSONObject(body) {
		return nil, false
	}
	return body, true
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
