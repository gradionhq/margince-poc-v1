// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package zalopersonal connects a rep's OWN personal Zalo account by QR scan
// and lets their replies leave as them.
//
// WHAT MAKES THIS UNIT DIFFERENT from every other connector in the tree, and
// the reason its rules are stricter rather than merely similar: the credential
// a member deposits here reads their ENTIRE personal chat life — their family,
// their doctor, their other employer — because a personal Zalo account has no
// business/private split. The sibling unit ../zalo-oa/ holds one workspace
// credential for an Official Account, which is a company asset; this one holds
// one sealed session per human, which is not.
//
// Three consequences run through every file:
//
//   - EVERY operation binds to rt.Caller().UserID and takes no member
//     argument. Not as a convention — a surface that accepted one would let a
//     holder of this unit's RBAC object deposit a credential FOR a colleague,
//     and thereby read that colleague's private life through this unit's own
//     front door.
//   - CAPTURE IS OFF UNTIL THE MEMBER CHOOSES. connection.capture_enabled
//     defaults false and no operation here turns it on; the scheduled ingress
//     that lands in the next change refuses to open a socket without it, so
//     connecting an account captures nothing on its own.
//   - THE SESSION IS NEVER A COLUMN. It is one sealed document in the unit's
//     user-scoped secret namespace, and no operation returns it, masked or
//     otherwise. The connection row records only THAT one is on deposit.
//
// Which file does what:
//
//   - migrations/ — ext_zalo_personal_connection, one row per connected
//     member. Status, the account's Zalo uid, and whether capture is armed.
//   - connection.go — the four operations a member drives from the screen.
//     Connect is TWO of them because the QR handshake's own steps are a 100s
//     and a 120s long-poll, which cannot live inside one HTTP call; the
//     in-flight jar is sealed under a second secret between them.
//   - send.go — the transport half. A reply leaves on the member's own
//     session, and Live answers from the row rather than by spending it.
//   - ledger.go — every state change on a connection row is recorded, because
//     a member's personal account becoming readable by this installation is a
//     fact somebody may later ask about.
//   - zalo*.go — the wire protocol, importing nothing from the core. It is
//     named for its layer rather than filed under one because a unit's tree
//     holds exactly one Go package; zaloprotocol.go opens it.
//
// NOTHING about this unit's GOVERNANCE is repeated in Go: api/crm.yaml holds
// each operation's method, tier, scope, RBAC object, prose and schemas.
package zalopersonal

import (
	"embed"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// migrations carries the unit's SQL layer INTO the binary. Shipping the
// directory without setting the field below passes every gate and then boots
// against a database where the table was never created.
//
//go:embed migrations
var migrations embed.FS

// New returns the unit's declaration: inert data, holding no handle into the
// core. Every field is a LITERAL, never a constant reference, because the
// operator manifest is derived from this function's AST without compiling it —
// a constant here is a name the generator cannot resolve. The literals below
// are duplicated as constants elsewhere in the package on purpose, and tests
// hold the two equal.
//
// No Ingress and no Jobs yet: this change connects an account and sends on it.
// The scheduled capture that needs both lands next, and declaring an ingress
// source before anything ingests would advertise a reach this unit does not
// have.
func New() extension.Extension {
	return extension.Extension{
		Name:    "zalo-personal",
		Version: "1.0.0",
		// USER scope, both, and there is no workspace-scoped secret in this
		// unit at all — an installation credential for a personal account is
		// not a thing that exists. `session` is the member's sealed login;
		// `pending-login` is the in-flight QR handshake between the two connect
		// operations, which is credential material in its own right and so is
		// sealed rather than held in a process's memory.
		Secrets: []extension.SecretsRequest{
			{Key: "session", Scope: extension.SecretScopeUser},
			{Key: "pending-login", Scope: extension.SecretScopeUser},
		},
		// The transport this unit supplies (ADR-0107/A158). A message it
		// carries lands as kind `message` with `zalo_personal` on the provider
		// column — the unit names the TRANSPORT and never the kind.
		//
		// Live is required because Send is present: a transport that can
		// transmit must be able to say whether it still may.
		Channels: []extension.Channel{
			{Provider: "zalo_personal", Send: send, Live: live},
		},
		Tools: []extension.Tool{
			{Name: "zalo_personal_connect_start", Handle: connectStart},
			{Name: "zalo_personal_connect_status", Handle: connectStatus},
			{Name: "zalo_personal_status", Handle: status},
			{Name: "zalo_personal_disconnect", Handle: disconnect},
		},
		Migrations: migrations,
	}
}

// provider is this unit's key in channel_provider, spelled once. It is the same
// string as the channel literal above on purpose, and a test holds them equal.
const provider = "zalo_personal"

// The two declared secret keys, spelled once for the handlers that address
// them. Same strings as the literals above; a test holds them equal.
const (
	sessionKey = "session"
	pendingKey = "pending-login"
)

// connectionTable is the unit's one table, schema-qualified. Every statement
// writes it through this constant: the ext schema is on no search_path the app
// connects with, so an unqualified name would resolve to a public table this
// unit does not own.
//
// The LEDGER names the same table without the schema (connectionEntity),
// because audit_log.entity_type names a kind of record rather than a path to
// one. One is derived from the other so the two cannot drift into two tables.
const connectionTable = "ext." + connectionEntity

// callerWorkspace is the tenant the invocation is pinned to, as SQL sees it.
// The Runtime binds app.workspace_id before any statement runs and the table's
// policy compares this exact expression, so an INSERT spelling it names the
// only workspace the policy's WITH CHECK would accept anyway.
const callerWorkspace = `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
