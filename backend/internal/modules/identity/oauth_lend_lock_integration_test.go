// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// The mutex between a consent that LENDS a passport and a revocation of that
// same passport. Both sides have to take the row lock or they overlap: Postgres
// runs them at READ COMMITTED, so a re-check reading the passport in one
// transaction while the code is written in another has a whole revocation commit
// between its two statements — and hands a client a redeemable code for a
// credential the human had already killed.
//
// Two cases, because they prove different halves and one cannot do both. The
// bounded case proves the consent CONTENDS for the row: it fails on a lock it
// would otherwise have read straight past, which is deterministic and which the
// un-locked version cannot survive. The blocking case proves the production
// OUTCOME of that contention: the consent waits, the revocation commits, and the
// re-check refuses — no error, the human's recoverable "choose again", nothing
// written.
//
// What neither reaches is the CODE's own five minutes — mintLentAuthorizationCode
// states that boundary, and oauth_lend_integration_test.go's
// TestALentPassportRevokedAfterConsentStillRedeems pins it.

import (
	"context"
	"errors"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// lendEnv is one bootstrapped workspace holding what a consent decision needs:
// a lendable passport of the admin's own, and the validated authorize request
// the POST would carry into the decision.
type lendEnv struct {
	*revocationEnv
	passport ids.PassportID
	request  authorizeRequest
}

func setupLendEnv(t *testing.T, slug string) *lendEnv {
	t.Helper()
	e := setupRevocationEnv(t, slug)
	return &lendEnv{
		revocationEnv: e,
		passport:      e.mintLendable(t, e.admin, []string{"read", "write"}),
		request: authorizeRequest{
			ClientID:      "client-" + slug,
			RedirectURI:   "https://client.example/cb",
			Scopes:        []string{"read"},
			CodeChallenge: strings.Repeat("night-challenge", 3), // 45 chars, RFC 7636 range
			State:         "night-state",
		},
	}
}

// mintLendable issues one passport a human may lend: their own, live, and
// answering to no OAuth grant.
func (e *revocationEnv) mintLendable(t *testing.T, human Identity, scopes []string) ids.PassportID {
	t.Helper()
	issued, err := e.svc.IssuePassport(e.wsCtx(human), human, IssuePassportInput{Scopes: scopes})
	if err != nil {
		t.Fatalf("minting a lendable passport: %v", err)
	}
	return issued.ID
}

// lend runs one consent decision for the admin's passport through svc, so a
// caller only says WHICH service drives it — the ordinary one, or the
// lock-bounded one below.
func (e *lendEnv) lend(svc *Service, rawPassportID string) (code string, lendable bool, err error) {
	return svc.mintLentAuthorizationCode(e.wsCtx(e.admin), e.admin, rawPassportID, e.request)
}

// revokeAndHold revokes a passport through the module's OWN cascade inside a
// transaction it does not commit until fn returns, then commits it. The real
// revoke path rather than a copy of its UPDATE: a copy holds fewer locks than
// production does and keeps passing once the real one changes.
func (e *lendEnv) revokeAndHold(t *testing.T, passportID ids.PassportID, fn func(tx pgx.Tx) error) {
	t.Helper()
	ctx := e.wsCtx(e.admin)
	if err := database.WithWorkspaceTx(ctx, e.svc.pool, func(tx pgx.Tx) error {
		if err := e.svc.revokePassportTx(ctx, tx, e.admin, passportID); err != nil {
			return err
		}
		return fn(tx)
	}); err != nil {
		t.Fatalf("holding the revocation open: %v", err)
	}
}

// waitUntilBlockingSomebody asks the DATABASE whether another backend is now
// queued on a lock this transaction holds, and returns once it is. That is what
// makes the waiting branch provably reached instead of hoped for: the consent it
// waits for is a goroutine, and without this the revoke would ordinarily commit
// before that goroutine ever reached the row — leaving a test that asserts a
// refusal it got for the boring reason.
//
// It polls rather than sleeps: every iteration is a round trip that returns the
// live answer, and the deadline is a failure guard, not a race — the consent
// either queues behind this transaction within it or the property under test is
// simply false.
func waitUntilBlockingSomebody(t *testing.T, tx pgx.Tx) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		var blocked bool
		err := tx.QueryRow(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM pg_stat_activity a
			   WHERE a.datname = current_database()
			     AND a.pid <> pg_backend_pid()
			     AND a.wait_event_type = 'Lock'
			     AND pg_backend_pid() = ANY (pg_blocking_pids(a.pid)))`).Scan(&blocked)
		if blocked {
			return
		}
		// A query the deadline cut short is the deadline, reported below as what it
		// means here rather than as a database failure.
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("asking the database who is queued behind this transaction: %v", err)
		}
	}
	t.Fatal("no backend ever queued on the lock this revocation holds — the consent read the passport without contending for it")
}

// codesAndLendAudits counts the two rows a lend commits — the authorization code
// and the audit row naming which passport was lent. A refusal must leave both at
// zero, and they are counted together because a code without its audit row is a
// lend nobody could trace.
func (e *lendEnv) codesAndLendAudits(t *testing.T) (codes, audits int) {
	t.Helper()
	if err := e.owner.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM oauth_authorization_code WHERE workspace_id = $1),
		       (SELECT count(*) FROM audit_log
		         WHERE workspace_id = $1 AND entity_type = 'oauth_authorization_code')`,
		e.admin.WorkspaceID).Scan(&codes, &audits); err != nil {
		t.Fatalf("counting what the consent wrote: %v", err)
	}
	return codes, audits
}

// lockWaitBoundedService is a second Service whose statements refuse to WAIT for
// a row lock. It makes the contended lock decide the outcome instead of the
// clock: a consent that takes the lent passport's lock fails immediately while
// another transaction holds that row, and one that never takes it never waits at
// all — so the case below needs no goroutine and no interleaving to get lucky
// with. The holder is the calling transaction itself, so the lock is provably
// held for the whole consent call.
//
// It is built through database.NewPool like every other pool in the product, with
// the bound as a DSN runtime parameter, so it inherits the one spelling of the
// pool contract (typed ids, the ping that turns an unreachable database into a
// legible failure) instead of a second copy of it.
//
// The bound is a test instrument, not this endpoint's production behaviour: a
// real consent waits, which the blocking case below is about.
func lockWaitBoundedService(t *testing.T) *Service {
	t.Helper()
	pool, err := database.NewPool(context.Background(), lockBoundedDSN(t, 250*time.Millisecond))
	if err != nil {
		t.Fatalf("opening the lock-bounded pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewService(pool)
}

// lockBoundedDSN adds lock_timeout to the test DSN through the URL's query, not
// by concatenation: an operator whose database needs `?sslmode=require` already
// spends the one `?`, and a second one would make the whole parameter list
// unparseable — a failure that would read as the lock case breaking rather than
// as the DSN being malformed.
func lockBoundedDSN(t *testing.T, bound time.Duration) string {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_APP_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		t.Fatalf("MARGINCE_TEST_APP_DSN is not a postgres:// URL, so the lock bound cannot be added to it: %q", dsn)
	}
	q := u.Query()
	q.Set("lock_timeout", bound.String())
	u.RawQuery = q.Encode()
	return u.String()
}

// A consent must CONTEND for the lent passport's row. The transaction below holds
// that row — revoked through the module's own cascade — for the whole consent
// call, so a consent that reaches a code did so by reading past a lock: exactly
// what a re-check in its own transaction did, since at READ COMMITTED it cannot
// see the uncommitted revocation, reads the passport as lendable, and writes the
// code anyway.
//
// Whatever the consent answered, nothing durable may exist afterwards.
func TestAConsentRacingARevocationOfTheLentPassportWritesNoCode(t *testing.T) {
	e := setupLendEnv(t, "lend-race-revoke")
	bounded := lockWaitBoundedService(t)

	var (
		code       string
		lendable   bool
		consentErr error
	)
	e.revokeAndHold(t, e.passport, func(pgx.Tx) error {
		code, lendable, consentErr = e.lend(bounded, e.passport.String())
		return nil
	})

	var pgErr *pgconn.PgError
	if !errors.As(consentErr, &pgErr) || pgErr.Code != pgerrcode.LockNotAvailable {
		t.Fatalf("the consent answered code %q lendable=%v err=%v, want a lock-wait timeout: it did not take the lent passport's row lock, so a revocation commits between the re-check and the code write",
			code, lendable, consentErr)
	}
	if codes, audits := e.codesAndLendAudits(t); codes != 0 || audits != 0 {
		t.Fatalf("the refused consent wrote %d code row(s) and %d lend audit row(s), want none of either",
			codes, audits)
	}
}

// What that contention produces in production, where nothing bounds the wait: the
// consent BLOCKS on the row, the revocation commits, and the re-check —
// re-evaluated by Postgres against the row version that revocation wrote —
// refuses the lend. Not an error and not a 500: the ordinary recoverable answer
// that sends the human back to choose again.
//
// The revocation does not commit until the database confirms the consent is queued
// behind it, which is what makes the waiting branch the branch this asserts.
// Without that confirmation the goroutine reaches the row only after the commit,
// and the refusal that follows is the ordinary predicate miss — a case that would
// pass identically against a re-check taking no lock at all.
func TestAConsentBlockedByARevocationRefusesInsteadOfMintingACode(t *testing.T) {
	e := setupLendEnv(t, "lend-blocked-by-revoke")

	var (
		wg         sync.WaitGroup
		code       string
		lendable   bool
		consentErr error
	)
	wg.Add(1)
	e.revokeAndHold(t, e.passport, func(tx pgx.Tx) error {
		go func() {
			defer wg.Done()
			code, lendable, consentErr = e.lend(e.svc, e.passport.String())
		}()
		waitUntilBlockingSomebody(t, tx)
		return nil
	})
	wg.Wait()

	if consentErr != nil {
		t.Fatalf("the consent failed instead of refusing: %v — a revocation it queued behind is an answer, not an error",
			consentErr)
	}
	if lendable || code != "" {
		t.Fatalf("lendable=%v code %q: the revocation this consent waited for must refuse the lend", lendable, code)
	}
	if codes, audits := e.codesAndLendAudits(t); codes != 0 || audits != 0 {
		t.Fatalf("the refused consent wrote %d code row(s) and %d lend audit row(s), want none of either",
			codes, audits)
	}
}

// The control that makes the contended case mean something: with nothing holding
// the row, the same lock-bounded consent commits — so the failure there is the
// passport's row lock and not the bound. It also pins what the locked statement
// reads: the code carries the LENT passport's own scopes, never the client's
// narrower request, and the courier itself is stored only as a hash.
func TestAnUncontendedLendCommitsTheCodeAndItsAudit(t *testing.T) {
	e := setupLendEnv(t, "lend-uncontended")
	bounded := lockWaitBoundedService(t)

	code, lendable, err := e.lend(bounded, e.passport.String())
	if err != nil {
		t.Fatalf("an uncontended consent: %v", err)
	}
	if !lendable || code == "" {
		t.Fatalf("lendable=%v code %q: the human's own live unbound passport is lendable", lendable, code)
	}
	if codes, audits := e.codesAndLendAudits(t); codes != 1 || audits != 1 {
		t.Fatalf("the consent wrote %d code row(s) and %d lend audit row(s), want exactly one of each",
			codes, audits)
	}
	var scopes []string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT scopes FROM oauth_authorization_code WHERE workspace_id = $1 AND code_hash = $2`,
		e.admin.WorkspaceID, hashOAuthCode(code)).Scan(&scopes); err != nil {
		t.Fatalf("reading the code row behind the returned courier: %v", err)
	}
	if !slices.Equal(scopes, []string{"read", "write"}) {
		t.Fatalf("the code carries %v, want the lent passport's own [read write] rather than the client's request",
			scopes)
	}
}

// The lock is per PASSPORT, not on the table: a revocation of some other
// credential — the same human's second passport here — must not delay a consent
// lending this one. Without this case a re-check that locked every row it looked
// at would pass the two above and serialize every consent in the workspace behind
// any revocation.
func TestALendIsUnaffectedByARevocationOfAnotherPassport(t *testing.T) {
	e := setupLendEnv(t, "lend-other-passport")
	bounded := lockWaitBoundedService(t)
	other := e.mintLendable(t, e.admin, []string{"read"})

	var (
		code       string
		lendable   bool
		consentErr error
	)
	e.revokeAndHold(t, other, func(pgx.Tx) error {
		code, lendable, consentErr = e.lend(bounded, e.passport.String())
		return nil
	})

	if consentErr != nil {
		t.Fatalf("consent while another passport was being revoked: %v — the lend takes a row lock, not a table-wide one",
			consentErr)
	}
	if !lendable || code == "" {
		t.Fatalf("lendable=%v code %q: revoking a DIFFERENT passport does not make this one unlendable",
			lendable, code)
	}
}

// The locked re-check is a second statement carrying the shared selectability
// predicate, so every exclusion is pinned at this seam too — the list the screen
// rendered is not the only place they have to hold.
func TestALendRefusesWhatIsNotTheHumansToLend(t *testing.T) {
	e := setupLendEnv(t, "lend-not-yours")
	// You may only lend your OWN authority: another human's live, unbound
	// passport is not lendable however the form names it.
	members := e.mintLendable(t, e.member, []string{"read"})
	// A dead credential is not a template, whichever way it died: revoked, or
	// simply past its own expiry — the clock is the database's, so the row is
	// aged rather than the test waiting for one.
	expired := e.mintLendable(t, e.admin, []string{"read"})
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE passport SET expires_at = now() - interval '1 minute' WHERE id = $1`, expired); err != nil {
		t.Fatalf("ageing the passport past its expiry: %v", err)
	}
	// A passport already bound to a connection is not lendable, or revoking one
	// connection would appear to affect another.
	connection := e.connectOAuth(t)
	bound := e.mintUnderGrant(t, connection.grantID)

	for name, rawID := range map[string]string{
		"another human's passport": members.String(),
		"an expired passport":      expired.String(),
		"a grant-bound passport":   bound.String(),
		// A form value that is not an id names no passport at all, and must never
		// reach the query as a zero value that could match a zero row.
		"a malformed passport_id": "not-a-passport-id",
		"an empty passport_id":    "",
	} {
		t.Run(name, func(t *testing.T) {
			code, lendable, err := e.lend(e.svc, rawID)
			if err != nil {
				t.Fatalf("the consent failed instead of refusing: %v", err)
			}
			if lendable || code != "" {
				t.Fatalf("lendable=%v code %q, want a refusal", lendable, code)
			}
			if codes, audits := e.codesAndLendAudits(t); codes != 0 || audits != 0 {
				t.Fatalf("the refusal wrote %d code row(s) and %d lend audit row(s), want none of either",
					codes, audits)
			}
		})
	}
}
