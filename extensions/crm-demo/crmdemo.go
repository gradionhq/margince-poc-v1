// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package crmdemo is the tier's REFERENCE extension: one first-party unit that
// exercises every capability an extension can hold, so PR1's acceptance is a
// human driving the SPA rather than a green test suite. It ships enabled in the
// vanilla tree alongside de and yogi — a demo nobody runs is not a demo.
//
// The six surfaces, and the one screen (#/ext/crm-demo, "Demo Notepad") that
// makes each of them visible:
//
//   - migrations/ — ext_crm_demo_note, workspace-scoped under forced RLS. Add a
//     note, restart the stack, it is still there.
//   - api/ — six governed operations under /ext/crm-demo/, three of them gating
//     on the unit's own RBAC object ext_crm_demo_note. A read-only seat sees the
//     list and no Add control.
//   - secrets — a stored HMAC signing key, proven by USE. Signing a payload is
//     the whole demonstration; no operation returns the key, masked or
//     otherwise, because the production shape this stands in for (a webhook
//     signature, a request signature) never needs one to.
//   - Jobs — a heartbeat tick that writes one row naming its own workspace. It
//     is the only thing on the screen that happens without a user, and naming
//     the workspace is what makes the dispatcher's FAN-OUT visible rather than
//     silently demonstrating the single-tenant case.
//   - Tools — the same six operations reach the agent as governed tools;
//     demo_list_notes is the one an operator asks "what's in my demo notepad".
//   - the screen — served from the CORE frontend tree, not from this unit.
//     extensions/<name>/frontend/ is still an unbuilt capability layer that
//     gen-composition refuses on sight, and lifting it means bundling
//     unit-authored TSX into the SPA — a supply-chain decision with its own
//     reviewed slice. See frontend/src/screens/ext/crmdemo.tsx.
//
// NOTHING about this unit's GOVERNANCE is repeated in Go. api/crm.yaml holds
// every operation's tier, scope, RBAC object, prose and schemas; api/jobs.yaml
// holds the job's cadence, wall clocks, queue and attempt cap. These files hold
// the one thing a static document cannot: the functions.
package crmdemo

import (
	"embed"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// migrations carries the unit's SQL layer INTO the binary.
//
// The embed is not a convenience and dropping it is the most dangerous mistake
// available here: check-ext-migrations and the derived-identifier collision
// check both key off the on-disk directory, while cmd/migrate applies the SQL
// out of THIS filesystem. A unit that shipped migrations/ without setting the
// Migrations field below would pass every gate green — the SQL blessed, the
// catalog checked — and ext_crm_demo_note would never be created.
//
//go:embed migrations
var migrations embed.FS

// New returns the unit's declaration: inert data, holding no handle into the
// core. Every capability arrives at the handlers below through the Runtime the
// core mints for one invocation and releases when the handler returns.
//
// Every field is a literal, including the tool and job names, because the
// operator manifest is derived from this function's AST without compiling it —
// a named constant here would be a value the manifest reader cannot resolve.
func New() extension.Extension {
	return extension.Extension{
		Name:    "crm-demo",
		Version: "1.0.0",
		Tools: []extension.Tool{
			{Name: "demo_list_notes", Handle: listNotes},
			{Name: "demo_add_note", Handle: addNote},
			{Name: "demo_remove_note", Handle: removeNote},
			{Name: "demo_store_signing_key", Handle: storeSigningKey},
			{Name: "demo_signing_key_status", Handle: signingKeyStatus},
			{Name: "demo_sign_payload", Handle: signPayload},
		},
		Secrets: []extension.SecretsRequest{
			{Key: "signing", Scope: extension.SecretScopeWorkspace},
		},
		Jobs: []extension.Job{
			{Name: "heartbeat", Handle: heartbeat},
		},
		Migrations: migrations,
	}
}

// noteTable is the unit's one table, schema-qualified. Every statement in this
// package writes it through this constant: the ext schema is on no search_path
// the app connects with, so an unqualified name would resolve to a public table
// the unit does not own.
const noteTable = "ext.ext_crm_demo_note"

// callerWorkspace is the tenant the invocation is pinned to, as SQL sees it.
//
// The Runtime binds app.workspace_id before any statement runs and the table's
// RLS policy compares this exact expression, so reading it back is how a
// handler learns which workspace it is in WITHOUT the surface growing a method
// that hands one out — and an INSERT spelling it names the only workspace the
// policy's WITH CHECK would accept anyway.
const callerWorkspace = `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
