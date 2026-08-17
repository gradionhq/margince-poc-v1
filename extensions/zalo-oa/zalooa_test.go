// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// What the declaration promises, pinned.
//
// Every string here is spelled TWICE in the shipped unit — once as a literal the
// manifest generator reads out of the AST without compiling anything, and once as
// a constant the Go uses. That duplication is required by the generator and is
// exactly the kind that rots silently, so these tests are the join.

import (
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

func TestTheDeclarationNamesTheUnitAndItsOneTable(t *testing.T) {
	unit := New()
	if unit.Name != "zalo-oa" {
		t.Fatalf("Name = %q, want zalo-oa — the directory name is the canonical unit name", unit.Name)
	}
	if unit.Version != "1.0.0" {
		t.Fatalf("Version = %q, want 1.0.0", unit.Version)
	}
	if connectionEntity != "ext_zalo_oa_connection" {
		t.Fatalf("the ledger entity is %q, and it must be ext_<unit with underscores>_<table>", connectionEntity)
	}
	if connectionTable != "ext."+connectionEntity {
		t.Fatalf("the SQL name %q is not the ledger name %q under the ext schema — the two must be derived from one another or they become two tables",
			connectionTable, connectionEntity)
	}
}

// The ingress system is KEBAB and the channel provider is SNAKE, they are
// different strings, and each equals the constant its own code spells.
//
// The grammars are the reason they differ: an IngressSource.System may not carry
// an underscore and a channel_provider.provider may not carry a hyphen, because
// the second is a row in a table with its own CHECK. A unit that made them match
// would fail one of the two validators.
func TestTheDeclaredSystemAndProviderMatchTheConstantsAndDifferByGrammar(t *testing.T) {
	unit := New()
	if len(unit.Ingress) != 1 {
		t.Fatalf("the unit declares %d ingress sources, want exactly 1", len(unit.Ingress))
	}
	if unit.Ingress[0].System != ingressSystem {
		t.Fatalf("the declared ingress system %q is not the constant %q — the manifest is derived from the literal and the code uses the constant",
			unit.Ingress[0].System, ingressSystem)
	}
	if len(unit.Channels) != 1 {
		t.Fatalf("the unit declares %d channels, want exactly 1", len(unit.Channels))
	}
	if unit.Channels[0].Provider != provider {
		t.Fatalf("the declared provider %q is not the constant %q", unit.Channels[0].Provider, provider)
	}
	if ingressSystem == provider {
		t.Fatal("the ingress system and the channel provider are the same string; they are governed by different grammars and must be spelled for their own column")
	}
	if err := unit.Ingress[0].Validate(); err != nil {
		t.Fatalf("the declared ingress source is not valid: %v", err)
	}
	if err := unit.Channels[0].Validate(); err != nil {
		t.Fatalf("the declared channel is not valid: %v", err)
	}
}

// The source vouches for NO identity key, because Zalo hands an Official Account
// no address for a human anywhere. Declaring MergeKeyEmail would be vouching for
// a field that does not exist, and the core would then admit an address this unit
// could only have invented.
func TestTheIngressSourceVouchesForNoIdentityKey(t *testing.T) {
	source := New().Ingress[0]
	if len(source.Merges) != 0 {
		t.Fatalf("the source declares merge keys %v; Zalo gives an OA no address, so it vouches for none", source.Merges)
	}
	if len(source.Lands) != 1 || source.Lands[0] != extension.KindActivity {
		t.Fatalf("the source lands %v, want exactly [%s]", source.Lands, extension.KindActivity)
	}
}

// The token is USER-scoped, and that is the decision the whole unit rests on: the
// ingress port admits an ingest only for a member holding one of this unit's
// DECLARED user-scoped keys, so a workspace-only credential would be refused on
// every record it ever tried to land.
func TestTheCredentialIsDeclaredAtUserScopeSoAnIngestCanBeConsentedTo(t *testing.T) {
	scopes := map[string]extension.SecretScope{}
	for _, request := range New().Secrets {
		if err := request.Validate(); err != nil {
			t.Fatalf("the declared secret %q is not valid: %v", request.Key, err)
		}
		scopes[request.Key] = request.Scope
	}
	if scopes[tokenKey] != extension.SecretScopeUser {
		t.Fatalf("%q is declared at %q scope; it must be user-scoped or no ingest this unit makes can be consented to",
			tokenKey, scopes[tokenKey])
	}
	if scopes[verifierKey] != extension.SecretScopeUser {
		t.Fatalf("%q is declared at %q scope; a PKCE verifier belongs to the one authorization its owner has in flight", verifierKey, scopes[verifierKey])
	}
	if scopes[appSecretKey] != extension.SecretScopeWorkspace {
		t.Fatalf("%q is declared at %q scope; one developer app serves every account an operator connects", appSecretKey, scopes[appSecretKey])
	}
}

// Send and Live ship together. A transport that can transmit and cannot say
// whether it still may forces the core to guess at the one moment guessing is
// unrecoverable, and the declaration refuses it — but only if both are wired.
func TestTheChannelSuppliesBothHalvesOfATransport(t *testing.T) {
	channel := New().Channels[0]
	if channel.Send == nil {
		t.Fatal("the channel declares no Send, so a captured conversation could not be answered")
	}
	if channel.Live == nil {
		t.Fatal("the channel declares no Live; Send without it is refused at boot")
	}
	if !channel.SuppliesTransport() {
		t.Fatal("the channel does not report that it supplies transport")
	}
}

// Every declared tool has a handler, and every job has one. A nil handler is a
// published route that answers 501 — legal on this tier, and not what this unit
// means.
func TestEveryDeclaredToolAndJobIsWired(t *testing.T) {
	unit := New()
	wanted := map[string]bool{
		"zalo_oa_authorize": false, "zalo_oa_connect": false,
		"zalo_oa_status": false, "zalo_oa_disconnect": false,
	}
	for _, tool := range unit.Tools {
		if _, declared := wanted[tool.Name]; !declared {
			t.Fatalf("the unit declares an unexpected tool %q", tool.Name)
		}
		if tool.Handle == nil {
			t.Fatalf("tool %q has no handler, so its route would answer 501", tool.Name)
		}
		wanted[tool.Name] = true
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("the unit does not declare the tool %q its contract fragment publishes", name)
		}
	}
	if len(unit.Jobs) != 1 || unit.Jobs[0].Name != "poll_chats" || unit.Jobs[0].Handle == nil {
		t.Fatalf("the unit's jobs are %+v, want exactly one wired poll_chats", unit.Jobs)
	}
}
