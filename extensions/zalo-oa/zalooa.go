// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package zalooa connects one Zalo Official Account to this installation: it
// pulls the OA's conversations onto the CRM timeline through the core's own
// capture pipeline, and it carries a rep's reply back out as the OA.
//
// WHOSE CREDENTIAL THIS RUNS ON is the one thing to understand before anything
// else here reads correctly. Zalo has no client-credentials grant — an OA ADMIN
// clicks *Cho phép* in a browser and the token pair that comes back is the grant
// that one human made. This unit does not conduct that browser trip (see
// connect.go); it takes the pair the administrator brings back from it, deposits
// it at USER scope under the person who brought it, names them in authorized_by,
// and lands every record on THEIR live authority.
//
// That is not a per-member integration. There is ONE connection, ONE OA and one
// setup, and every rep in the workspace replies through it; what is per-member
// is only the custody of the credential, held by the single human Zalo bound the
// grant to. Two rules make that necessary rather than stylistic:
//
//   - The ingress port admits an ingest only for a member who currently holds one
//     of this unit's DECLARED user-scoped secrets — depositing a credential is
//     the act that says "act for me here". A unit whose only credential were
//     workspace-scoped would be refused on every record it ever tried to land.
//   - An OA token belongs to the admin who authorized it, and Zalo will not renew
//     it for anybody else. Recording who that is turns "the connection stopped
//     when somebody left" from a mystery into a status with a name on it.
//
// Which file demonstrates what:
//
//   - migrations/ — ext_zalo_oa_connection, ONE row per workspace: which OA, who
//     authorized it, the tier evidence and the cursor. It holds no token.
//   - client.go — the provider, and the trap it sets: EVERY Zalo response is HTTP
//     200, including an invalid token and an unknown endpoint. The error lives in
//     the body. A client that classified on status would read a revoked token as
//     a successful empty page.
//   - oauth.go — the token endpoint, which this unit reaches for exactly one
//     thing: the single-use REFRESH.
//   - connect.go — connecting: the tier gate, which is a CAPABILITY PROBE and
//     never a match on the localized package name; the one renewal that takes
//     custody of the credential; and the row it writes.
//   - connection.go — the row itself, plus status and disconnect.
//   - credential.go — the sealed token document and the serialized refresh. It
//     is the sharpest correctness requirement in this unit: the refresh token is
//     single-use, and a rotation issued but not persisted costs an OA-admin
//     re-authorization in another company.
//   - walk.go / poll.go — the scheduled pull, and the cursor rule that keeps a
//     truncated walk from stranding the messages under it.
//   - send.go — the transport half. The core stages a rep's reply and calls in;
//     this unit never sends on its own initiative.
//
// NOTHING about this unit's GOVERNANCE is repeated in Go: api/crm.yaml holds
// each operation's tier, scope, RBAC object, prose and schemas, and api/jobs.yaml
// holds the cadence and the wall clocks.
package zalooa

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
// core. Every field is a literal, because the operator manifest is derived from
// this function's AST without compiling it.
func New() extension.Extension {
	return extension.Extension{
		Name:    "zalo-oa",
		Version: "1.0.0",
		// The declaration that lets this unit reach core capture, and the source
		// every record it lands is attributed to.
		//
		// Merges is EMPTY, and that is a statement rather than an omission: this
		// source vouches for no identity key, because Zalo hands an OA an opaque
		// account id, a display name and an avatar and no address anywhere. A
		// declaration of MergeKeyEmail would be vouching for a field that does
		// not exist.
		Ingress: []extension.IngressSource{
			{
				System: "zalo-oa",
				Lands:  []extension.RecordKind{extension.KindActivity},
			},
		},
		Tools: []extension.Tool{
			{Name: "zalo_oa_connect", Handle: connect},
			{Name: "zalo_oa_status", Handle: status},
			{Name: "zalo_oa_disconnect", Handle: disconnect},
		},
		// Two keys in the two scopes, and which is which is the whole custody
		// argument above.
		//
		// The TOKEN is user-scoped under the administrator who connected the
		// account, and that scope is load-bearing rather than chosen: it is the
		// deposit the ingress port reads as that human's consent to be acted for,
		// so a workspace-scoped token would be refused on every record this unit
		// ever tried to land.
		//
		// The APP SECRET is the installation's own — one developer app serves
		// every account an operator connects, so it describes the installation and
		// not the person, and every renewal spends it. It is also what places this
		// unit under Settings → Integrations rather than beside a member's
		// personal accounts on Connections, which is the honest page for one
		// Official Account that serves a whole workspace.
		Secrets: []extension.SecretsRequest{
			{Key: "oa-token", Scope: extension.SecretScopeUser},
			{Key: "app-secret", Scope: extension.SecretScopeWorkspace},
		},
		// The transport this unit supplies (ADR-0107/A158). A message it carries
		// lands as kind `message` with `zalo_oa` on the provider column — the
		// unit names the TRANSPORT and never the kind.
		//
		// The provider is a LITERAL rather than the `provider` constant, and so
		// is the ingress system above: the manifest is derived STATICALLY from
		// this AST without compiling the unit, so a constant here is a name the
		// generator cannot resolve. Tests hold both pairs equal.
		//
		// Note the two do NOT match, and must not be made to: an ingress system
		// is kebab and a channel provider is snake, because the latter is a row
		// in channel_provider and has to satisfy that column's own CHECK.
		Channels: []extension.Channel{
			{Provider: "zalo_oa", Send: send, Live: live},
		},
		Jobs: []extension.Job{
			{Name: "poll_chats", Handle: pollChats},
		},
		Migrations: migrations,
	}
}

// ingressSystem is the declared source above, spelled once for the Go side. The
// core pairs it with the unit name to derive `ext:zalo-oa:zalo-oa`, the
// provenance every landed record carries.
const ingressSystem = "zalo-oa"

// provider is this unit's key in channel_provider, and it is NOT an activity
// kind: a message it carries lands as kind `message` with this name on its own
// column.
const provider = "zalo_oa"

// The declared secret keys, spelled once each.
const (
	// tokenKey holds the sealed token document — access token, refresh token and
	// expiry as ONE value, so a rotation is one write and the halves cannot land
	// apart. See credential.go.
	tokenKey = "oa-token"
	// appSecretKey holds the developer app's secret, which every renewal spends.
	appSecretKey = "app-secret"
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
// policy compares this exact expression.
const callerWorkspace = `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
