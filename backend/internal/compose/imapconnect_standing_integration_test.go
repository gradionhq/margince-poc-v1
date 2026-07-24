// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The standing IMAP connect transport over a real database and a real
// in-memory IMAP server: good credentials probe, seal to the vault, and the
// row lands connected with the secret nowhere on it; each refusal answers
// its own status (401 signed out, 422 missing creds, 422 rejected login,
// 502 unreachable). No Gmail OAuth app exists anywhere here — the standing
// IMAP connect gates on the registry alone.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture/imap"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

const standingIMAPUser = "imap-owner@ws.example"

// standingIMAPPass is generated per run so no password-shaped literal lives
// in the tree (secret scanners cannot tell a fixture from a leak).
var standingIMAPPass = func() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}()

// startPlainIMAPServer boots the in-memory server on loopback; the test
// dial path is exercised through the production handler, which only ever
// sees host+port.
func startPlainIMAPServer(t *testing.T) (host string, port int) {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser(standingIMAPUser, standingIMAPPass)
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
	})
	go func() {
		//craft:ignore swallowed-errors the listener closes at test end; Serve's shutdown error is the expected exit
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		//craft:ignore swallowed-errors test-server shutdown; the assertions already ran
		_ = srv.Close()
	})
	h, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	pn, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return h, pn
}

func TestStandingIMAPConnectTransport(t *testing.T) {
	e := integration.Setup(t)
	host, port := startPlainIMAPServer(t)

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	vault, err := keyvault.New(keyvault.Config{RootKey: key, Pool: e.Pool})
	if err != nil {
		t.Fatal(err)
	}
	h := connectorHandlers{
		registry: NewCaptureRegistry(e.Pool, vault),
		// The production authenticate demands TLS on a public host; the
		// transport's own branches are the subject here, so the probe runs
		// the standing connector over a plain loopback dial.
		imapAuthenticate: plainProbe,
	}

	// Route through the real mux: calling the handler directly is what let the
	// shadowed-route defect survive review, so this asserts reachability too.
	srv := Server{connectorHandlers: h}
	mux := crmcontracts.HandlerFromMuxWithBaseURL(srv, chi.NewRouter(), "/v1")
	post := func(t *testing.T, ctx context.Context, body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/connectors/imap/connect", bytes.NewReader(payload))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	imapBody := func(pass string) map[string]any {
		return map[string]any{"imap": map[string]any{
			"host": host, "port": port, "username": standingIMAPUser, "secret": pass,
		}}
	}
	// A realistic signed-in human session carries RBAC (AdminPerms) but NO
	// passport scopes — scopes are an agent concept. The handler must grant the
	// connector's read scope from the human's authority; hand-setting it here
	// would mask a regression where a real session is refused for lack of it.
	authed := e.As(e.Rep1, nil, integration.AdminPerms)

	t.Run("signed out is 401", func(t *testing.T) {
		if rec := post(t, context.Background(), imapBody(standingIMAPPass)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("missing credentials are 422", func(t *testing.T) {
		if rec := post(t, authed, map[string]any{}); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
	})

	t.Run("a rejected login is 422", func(t *testing.T) {
		if rec := post(t, authed, imapBody(standingIMAPPass[:8])); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
	})

	t.Run("an unreachable server is 502", func(t *testing.T) {
		body := map[string]any{"imap": map[string]any{
			"host": "127.0.0.1", "port": 1, "username": standingIMAPUser, "secret": standingIMAPPass,
		}}
		if rec := post(t, authed, body); rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
	})

	t.Run("good credentials connect, seal, and persist", func(t *testing.T) {
		rec := post(t, authed, imapBody(standingIMAPPass))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Connection struct {
				Provider string `json:"provider"`
				Status   string `json:"status"`
			} `json:"connection"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Connection.Provider != "imap" || resp.Connection.Status != "connected" {
			t.Fatalf("connection = %+v, want a connected imap row", resp.Connection)
		}
		// The credential is a vault ref; the secret never touches the row.
		err := database.WithWorkspaceTx(authed, e.Pool, func(tx pgx.Tx) error {
			var ref *string
			var auth []byte
			if err := tx.QueryRow(context.Background(), `
				SELECT credential_ref, auth FROM capture_connection WHERE provider = 'imap'`).Scan(&ref, &auth); err != nil {
				return err
			}
			if ref == nil || *ref == "" {
				t.Error("credential_ref empty — the bundle was not sealed")
			}
			if len(auth) != 0 {
				t.Error("legacy auth column populated — the secret touched the row")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a non-default mailbox and max_messages reach the sealed credentials", func(t *testing.T) {
		body := map[string]any{"imap": map[string]any{
			"host": host, "port": port, "username": standingIMAPUser, "secret": standingIMAPPass,
			"mailbox": "Archive", "max_messages": 17,
		}}
		rec := post(t, authed, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		var ref string
		err := database.WithWorkspaceTx(authed, e.Pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `
				SELECT credential_ref FROM capture_connection WHERE provider = 'imap'`).Scan(&ref)
		})
		if err != nil {
			t.Fatal(err)
		}
		secret, err := vault.Get(context.Background(), ids.From[ids.WorkspaceKind](e.WS), keyvault.Ref(ref))
		if err != nil {
			t.Fatalf("resolving the sealed bundle: %v", err)
		}
		var sealed imap.Credentials
		if err := json.Unmarshal(secret, &sealed); err != nil {
			t.Fatalf("unmarshaling the sealed bundle: %v", err)
		}
		if sealed.Mailbox != "Archive" || sealed.MaxMessages != 17 {
			t.Fatalf("sealed credentials = %+v, want mailbox=Archive max_messages=17 — these fields must never be defaulted away", sealed)
		}
	})
}

// plainProbe replaces only the transport of the standing probe: plain
// loopback dial + LOGIN, then the same sealed-bundle shape the production
// Authenticate returns (the credentials themselves). The handler's own
// branches — decode, refusal mapping, Connect, read-back — are all
// production code.
func plainProbe(_ context.Context, req connector.AuthRequest) (connector.Auth, error) {
	var creds imap.Credentials
	if err := json.Unmarshal(req.Payload, &creds); err != nil {
		return nil, err
	}
	conn, err := net.Dial("tcp", net.JoinHostPort(creds.Host, strconv.Itoa(creds.Port)))
	if err != nil {
		return nil, imap.ErrUnreachable
	}
	client := imapclient.New(conn, &imapclient.Options{})
	if err := client.Login(creds.Email, creds.Password).Wait(); err != nil {
		//craft:ignore swallowed-errors best-effort close of a session whose login already failed
		_ = client.Close()
		return nil, imap.ErrLoginRejected
	}
	//craft:ignore swallowed-errors best-effort close of the probe session; the login already answered
	_ = client.Close()
	return connector.Auth(req.Payload), nil
}
