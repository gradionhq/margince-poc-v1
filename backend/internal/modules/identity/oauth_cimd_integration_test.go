// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// What a metadata document DOES to this installation's rows, against a real
// database: the client it creates, the provenance it creates it under, the row
// it may and may not overwrite, and the cache that decides whether the next
// authorize fetches again.
//
// The unit suite next door proves the document is judged correctly; none of it
// can prove any of this, because every one of these properties is a row.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// cimdEnv is one installation plus a document server it controls.
type cimdEnv struct {
	svc  *Service
	ctx  context.Context //nolint:containedctx // the workspace binding every query here needs, held so each test does not rebuild it
	docs *httptest.Server
	body func(url string) string
}

// setupCIMD stands up an installation and a TLS document server, and points the
// package's fetch client at a transport WITHOUT the egress guard.
//
// TLS, not plaintext: a client_id has to be an https URL with a path or it is
// not a metadata document at all, and a plaintext test server would prove the
// scheme check rather than anything past it.
//
// That swap is the whole reason this file can exist. netguard refuses a
// loopback address in the dialer's Control hook — correctly, and the unit suite
// asserts exactly that — so a test server on 127.0.0.1 is unreachable to the
// real client by design. Swapping the client here tests what the guard protects
// rather than the guard itself, and the guard keeps its own test.
func setupCIMD(t *testing.T, slug string) *cimdEnv {
	t.Helper()
	owner, pool := setupIdentityDB(t)
	_ = owner
	svc := NewService(pool)
	slug += "-" + ids.NewV7().String()[24:]

	var wsID ids.WorkspaceID
	err := database.WithInfraTx(context.Background(), pool, func(tx pgx.Tx) error {
		var err error
		wsID, err = createInstallation(context.Background(), tx, InstallationBootstrap{
			OrganizationName: slug,
			AdminEmail:       "admin@" + slug + ".test",
			AdminName:        "Admin",
			AdminPassword:    "correct-horse-battery-staple",
		}, nil)
		return err
	})
	if err != nil {
		t.Fatalf("creating the installation: %v", err)
	}

	e := &cimdEnv{
		svc: svc,
		ctx: principal.WithWorkspaceID(context.Background(), wsID.UUID),
		body: func(url string) string {
			return `{"client_id":"` + url + `","client_name":"Metadata Client",` +
				`"redirect_uris":["http://127.0.0.1:9876/callback"]}`
		},
	}
	e.docs = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=3600")
		//craft:ignore swallowed-errors a test server's write failure is the client hanging up
		_, _ = w.Write([]byte(e.body(e.docs.URL + r.URL.Path)))
	}))
	t.Cleanup(e.docs.Close)

	guarded := cimdClient
	cimdClient = e.docs.Client()
	t.Cleanup(func() { cimdClient = guarded })
	return e
}

// client reads back the one row a document produced.
func (e *cimdEnv) client(t *testing.T, clientID string) (name, via string, redirects []string, expires *time.Time) {
	t.Helper()
	err := database.WithWorkspaceTx(e.ctx, e.svc.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx,
			`SELECT client_name, created_via, redirect_uris, metadata_expires_at
			   FROM oauth_client WHERE client_id = $1`, clientID).
			Scan(&name, &via, &redirects, &expires)
	})
	if err != nil {
		t.Fatalf("reading the client row for %q: %v", clientID, err)
	}
	return name, via, redirects, expires
}

// A validated document becomes an ORDINARY client row. That is the whole
// integration: oauth_grant carries a foreign key onto this table, and it is
// what makes a connection revocable — a CIMD client with no row would be a
// connection an admin can see the passport for and never the client.
func TestAValidatedDocumentBecomesAnOrdinaryClientRow(t *testing.T) {
	e := setupCIMD(t, "cimd-row")
	clientID := e.docs.URL + "/client.json"

	if err := e.svc.resolveCIMDClient(e.ctx, clientID); err != nil {
		t.Fatalf("resolving a valid document: %v", err)
	}

	name, via, redirects, expires := e.client(t, clientID)
	if name != "Metadata Client" {
		t.Errorf("client_name is %q, want the document's own", name)
	}
	if via != "cimd" {
		t.Errorf("created_via is %q, want cimd — an admin has to be able to tell it from a registration", via)
	}
	if len(redirects) != 1 || redirects[0] != "http://127.0.0.1:9876/callback" {
		t.Errorf("redirect_uris is %v, want the document's own", redirects)
	}
	if expires == nil || !expires.After(time.Now()) {
		t.Errorf("metadata_expires_at is %v; a row with no live expiry is refetched on every authorize", expires)
	}
}

// A fresh row is NOT refetched. Without the cache every authorize is an
// outbound request whose rate the client sets, which is the thing the TTL floor
// exists to bound.
func TestAFreshDocumentIsNotFetchedAgain(t *testing.T) {
	e := setupCIMD(t, "cimd-cache")
	clientID := e.docs.URL + "/client.json"
	if err := e.svc.resolveCIMDClient(e.ctx, clientID); err != nil {
		t.Fatal(err)
	}

	// The server now answers a DIFFERENT name. A refetch would show it.
	e.body = func(url string) string {
		return `{"client_id":"` + url + `","client_name":"Renamed","redirect_uris":["https://a.example/cb"]}`
	}
	if err := e.svc.resolveCIMDClient(e.ctx, clientID); err != nil {
		t.Fatal(err)
	}

	if name, _, _, _ := e.client(t, clientID); name != "Metadata Client" {
		t.Errorf("the row was refetched inside its cache window (name is now %q)", name)
	}
}

// A STALE row is refetched, and what the document says now wins. The other
// direction of the same property: a client that rotates its redirect list must
// not be held to the old one forever.
func TestAStaleDocumentIsRefetchedAndItsNewContentWins(t *testing.T) {
	e := setupCIMD(t, "cimd-stale")
	clientID := e.docs.URL + "/client.json"
	if err := e.svc.resolveCIMDClient(e.ctx, clientID); err != nil {
		t.Fatal(err)
	}
	e.expire(t, clientID)

	e.body = func(url string) string {
		return `{"client_id":"` + url + `","client_name":"Renamed","redirect_uris":["https://a.example/cb"]}`
	}
	if err := e.svc.resolveCIMDClient(e.ctx, clientID); err != nil {
		t.Fatal(err)
	}

	name, _, redirects, _ := e.client(t, clientID)
	if name != "Renamed" {
		t.Errorf("a stale row kept the old name %q", name)
	}
	if len(redirects) != 1 || redirects[0] != "https://a.example/cb" {
		t.Errorf("a stale row kept the old redirect list %v — a rotated client would be refused forever", redirects)
	}
}

// expire backdates the cache so the next resolve has to fetch.
func (e *cimdEnv) expire(t *testing.T, clientID string) {
	t.Helper()
	err := database.WithWorkspaceTx(e.ctx, e.svc.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(e.ctx,
			`UPDATE oauth_client SET metadata_expires_at = now() - interval '1 hour' WHERE client_id = $1`, clientID)
		return err
	})
	if err != nil {
		t.Fatalf("expiring the cache: %v", err)
	}
}

// A DISABLED client is not refetched, however stale it is. Refetching would let
// a client an admin switched off keep this server making outbound requests on
// its behalf — the switch has to stop the traffic, not only the authorization.
func TestADisabledClientIsNotFetchedAgain(t *testing.T) {
	e := setupCIMD(t, "cimd-disabled")
	clientID := e.docs.URL + "/client.json"
	if err := e.svc.resolveCIMDClient(e.ctx, clientID); err != nil {
		t.Fatal(err)
	}
	e.expire(t, clientID)
	err := database.WithWorkspaceTx(e.ctx, e.svc.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(e.ctx, `UPDATE oauth_client SET disabled_at = now() WHERE client_id = $1`, clientID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	e.body = func(url string) string {
		return `{"client_id":"` + url + `","client_name":"Renamed","redirect_uris":["https://a.example/cb"]}`
	}

	if err := e.svc.resolveCIMDClient(e.ctx, clientID); err != nil {
		t.Fatalf("resolving a disabled client: %v", err)
	}

	if name, _, _, _ := e.client(t, clientID); name != "Metadata Client" {
		t.Error("a disabled client was refetched; switching it off must stop the outbound traffic too")
	}
}

// A DCR-registered row is never re-provenanced by a document. The upsert's
// conflict clause is what holds this: without it, anyone who could serve a
// document at a registered client's id could rewrite its redirect list.
func TestADocumentCannotTakeOverARegisteredClient(t *testing.T) {
	e := setupCIMD(t, "cimd-takeover")
	clientID := e.docs.URL + "/client.json"
	err := database.WithWorkspaceTx(e.ctx, e.svc.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(e.ctx, `
			INSERT INTO oauth_client (workspace_id, client_id, client_name, redirect_uris, created_via)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, 'Registered', $2, 'dcr')`,
			clientID, []string{"https://registered.example/cb"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := e.svc.resolveCIMDClient(e.ctx, clientID); err != nil {
		t.Fatalf("resolving over a registered client: %v", err)
	}

	name, via, redirects, _ := e.client(t, clientID)
	if via != "dcr" || name != "Registered" {
		t.Errorf("a document re-provenanced a registered client to %q/%q", via, name)
	}
	if len(redirects) != 1 || redirects[0] != "https://registered.example/cb" {
		t.Errorf("a document rewrote a registered client's redirect list to %v", redirects)
	}
}

// A client_id that is not a metadata-document URL resolves nothing and says so,
// which is how the authorize path tells "resolve this from the table" from
// "this failed".
func TestAnOpaqueClientIDIsNotResolvedAsADocument(t *testing.T) {
	e := setupCIMD(t, "cimd-opaque")

	err := e.svc.resolveCIMDClient(e.ctx, "s6BhdRkqt3")

	if err == nil || !strings.Contains(err.Error(), "not a metadata document URL") {
		t.Fatalf("an opaque client_id → %v, want errNotCIMD", err)
	}
}

// A document the server refuses leaves NO row. A row written before validation
// would be a client this installation believes in on the strength of a document
// it rejected.
func TestARefusedDocumentLeavesNoRow(t *testing.T) {
	e := setupCIMD(t, "cimd-refused")
	clientID := e.docs.URL + "/client.json"
	e.body = func(string) string {
		return `{"client_id":"https://elsewhere.example/other.json","client_name":"Impostor",` +
			`"redirect_uris":["https://a.example/cb"]}`
	}

	if err := e.svc.resolveCIMDClient(e.ctx, clientID); err == nil {
		t.Fatal("a document claiming another URL was accepted")
	}

	var rows int
	if err := database.WithWorkspaceTx(e.ctx, e.svc.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `SELECT count(*) FROM oauth_client WHERE client_id = $1`, clientID).Scan(&rows)
	}); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("a refused document left %d client rows", rows)
	}
}
