// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package relayprobe is the ingress seam's consumer: a unit that pulls each
// connected member's Relay inbox and lands what they were directed at on the
// CRM timeline, through the same capture pipeline a mailbox goes through.
//
// WHAT THIS UNIT IS NOT. The product's Relay integration — cross-linking a
// deal or a person to a conversation, both directions resolving from one stored
// row, riding the shared event bus — is CORE interop and is untouched by
// anything here. This unit owns no link, writes no conversation_link, and
// consumes no bus. It is the INBOUND half of a channel, built on the extension
// tier: a member deposits their own token, a scheduled poll reads their
// directed messages, and each one becomes an ordinary captured activity.
//
// Which file demonstrates what:
//
//   - migrations/ — ext_relay_probe_connection, one row per connected
//     member, workspace-scoped under forced RLS. It holds the cursor and the
//     status; it deliberately does NOT hold the token.
//   - connection.go — connect / status / disconnect, gating on one RBAC object
//     of the unit's own, which no seeded role holds. Connect binds the token to
//     the CALLER's own user id, taken from the invocation and never from the
//     body: otherwise any holder of that object could deposit a credential for
//     a colleague, and the consent the ingress port checks would be forgeable
//     through this unit's own surface.
//   - secrets — the member's personal access token, at user scope, read back
//     only by the poll that acts for that member. No operation returns it,
//     masked or otherwise.
//   - poll.go — the scheduled job, and the whole reason the unit exists:
//     Runtime.Ingest, called with none of this unit's transactions open,
//     followed by a cursor advance that is a separate commit. The cursor rule
//     lives there and is the part most worth reading.
//   - ledger.go — every state change on the connection row records a ledger row
//     and an event, because a connection appearing, moving or breaking is a
//     fact somebody may later ask about.
//
// NOTHING about this unit's GOVERNANCE is repeated in Go: api/crm.yaml holds
// each operation's tier, scope, RBAC object, prose and schemas, api/jobs.yaml
// holds the cadence and the wall clocks, and the ingress declaration below is
// what an operator reads to see that this unit reaches core capture at all.
package relayprobe

import (
	"embed"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// migrations carries the unit's SQL layer INTO the binary. Shipping the
// directory without setting the field below passes every gate — the SQL
// blessed, the catalog checked — and then boots against a database where the
// table was never created.
//
//go:embed migrations
var migrations embed.FS

// New returns the unit's declaration: inert data, holding no handle into the
// core. Every field is a literal, because the operator manifest is derived from
// this function's AST without compiling it.
func New() extension.Extension {
	return extension.Extension{
		Name:    "relay-probe",
		Version: "1.0.0",
		// The declaration that lets this unit reach core capture, and the
		// source every record it lands is attributed to. A record naming
		// anything else is refused at the call rather than landed under an
		// invented provenance namespace.
		Ingress: []extension.IngressSource{
			// The email merge key is VOUCHED FOR, not merely passed along:
			// Relay answers /api/users/batch from the workspace directory, so
			// the address on a member's account is the one their administrator
			// set rather than one they typed about themselves. That is what the
			// core needs before it will let an address corroborate the human a
			// direct message names by account — and it is the declaration an
			// operator reads in manifest.generated.json before enabling this
			// unit.
			{
				System: "relay",
				Lands:  []extension.RecordKind{extension.KindActivity},
				Merges: []extension.MergeKey{extension.MergeKeyEmail},
			},
		},
		Tools: []extension.Tool{
			{Name: "relay_connect", Handle: connect},
			{Name: "relay_status", Handle: status},
			{Name: "relay_disconnect", Handle: disconnect},
		},
		// User scope, and it is the whole authority story: depositing a
		// credential with this unit is what says "poll this account for me",
		// and the ingress port checks that deposit before it acts as anybody.
		Secrets: []extension.SecretsRequest{
			{Key: "api-token", Scope: extension.SecretScopeUser},
		},
		// The transport this unit supplies (ADR-0107/A158). A message it carries
		// lands as kind `message` with `relay` on the provider column — the
		// unit names the TRANSPORT and never the kind, which is what keeps the
		// two axes separate from outside the core.
		//
		// Live is required because Send is present: a transport that can
		// transmit must be able to say whether it still may, or the core has to
		// guess at the one moment guessing is unrecoverable.
		Channels: []extension.Channel{
			// A literal rather than the `provider` constant, for the reason the
			// ingress source above is one: the manifest is derived STATICALLY,
			// without compiling the unit, so a constant here would be a name the
			// generator cannot resolve. The two are the same string on purpose
			// and a test holds them equal.
			{Provider: "relay", Send: send, Live: live},
		},
		Jobs: []extension.Job{
			{Name: "poll_inbox", Handle: pollInbox},
		},
		// The ways the job above fails, in this unit's own words
		// (failureclasses.go). Declaring them is what lets an operator reading a
		// dead job see that the provider was unreachable rather than that the
		// failure could not be classified — and the list is named rather than
		// inlined because the same values are what the poll returns, so the
		// declared set and the returned class cannot become two sets.
		FailureClasses: failureClasses,
		Migrations:     migrations,
	}
}

// ingressSystem is the declared source above, spelled once. It is what the
// core pairs with the unit name to derive `ext:relay-probe:relay`, the
// provenance every landed record carries — so this constant and the
// declaration are the same string on purpose, and a test holds them equal.
const ingressSystem = "relay"

// tokenKey is the declared secret key the member's personal access token is
// deposited under.
const tokenKey = "api-token"

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
