// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The renewal, which is the one place in this unit where a mistake cannot be
// undone by anybody in this building.
//
// A Zalo refresh token is single-use: spending it kills it. So every test here is
// about one of four things — that the pair is written as ONE value, that exactly
// one caller ever spends it, that it is kept before it is used, and that a
// rotation nobody kept parks the connection instead of being tried again.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// seal puts a token pair on deposit for the authorizing admin, through the unit's
// own writer rather than by scripting a secret — a fixture that wrote the
// document itself would prove nothing about the shape production stores.
func seal(t *testing.T, rt *fakeRuntime, pair tokenPair) {
	t.Helper()
	if err := sealTokens(t.Context(), rt, adminUserID, pair); err != nil {
		t.Fatalf("sealTokens: %v", err)
	}
}

func livePair(expiresAt time.Time) tokenPair {
	return tokenPair{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresAt: expiresAt}
}

func connectedConn() connection {
	return connection{
		ID: fixtureConnectionID, OAID: fixtureOAID, AppID: "app-1",
		AuthorizedBy: adminUserID, Status: statusConnected, Version: 1,
	}
}

// The two tokens and the expiry are ONE sealed document, so a rotation is one
// write and the halves cannot land apart — a live access token beside a dead
// refresh token is a connection that works for a day and then cannot be renewed
// by anybody.
func TestTheTokenPairIsSealedAndReadBackAsOneDocument(t *testing.T) {
	rt := newRuntime()
	pair := livePair(at(20 * time.Hour))
	seal(t, rt, pair)

	stored, ok := rt.secrets.stored["user/"+adminUserID+"/"+tokenKey]
	if !ok {
		t.Fatal("nothing was deposited under the declared token key")
	}
	var document map[string]any
	if err := json.Unmarshal(stored, &document); err != nil {
		t.Fatalf("the sealed value is not one document: %v", err)
	}
	for _, half := range []string{"access_token", "refresh_token", "expires_at"} {
		if _, carried := document[half]; !carried {
			t.Fatalf("the sealed document does not carry %q; the halves would have to be written separately", half)
		}
	}
	read, err := unsealTokens(t.Context(), rt, adminUserID)
	if err != nil {
		t.Fatalf("unsealTokens: %v", err)
	}
	if read.AccessToken != pair.AccessToken || read.RefreshToken != pair.RefreshToken {
		t.Fatalf("read back %+v, want what was sealed", read)
	}
}

// A credential that is simply absent is its own class: the admin was never
// connected, or somebody withdrew the deposit, and neither is a provider problem.
func TestAnAbsentCredentialIsItsOwnClass(t *testing.T) {
	rt := newRuntime()
	if _, err := unsealTokens(t.Context(), rt, adminUserID); !errors.Is(err, errCredentialGone) {
		t.Fatalf("error = %v, want the credential-gone class", err)
	}
}

// A sealed document this unit cannot read is not a token problem to retry:
// nothing later will decode it either, so it reads as a rotation that was lost.
func TestASealedDocumentThatCannotBeReadIsALostRotation(t *testing.T) {
	for name, sealed := range map[string]string{
		"not a document": `not json at all`,
		"half a pair":    `{"access_token":"a"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rt := newRuntime()
			rt.secrets.stored["user/"+adminUserID+"/"+tokenKey] = []byte(sealed)
			if _, err := unsealTokens(t.Context(), rt, adminUserID); !errors.Is(err, errRotationLost) {
				t.Fatalf("error = %v, want the lost-rotation class", err)
			}
		})
	}
}

// A usable token is handed straight back and the token endpoint is never reached.
// A renewal that ran when it did not have to would spend a single-use token for
// nothing.
func TestAUsableTokenIsSpentWithoutRenewingIt(t *testing.T) {
	rt := newRuntime()
	seal(t, rt, livePair(at(20*time.Hour)))
	grants := &fakeGrants{}

	got, _, err := usableToken(t.Context(), rt, grants, connectedConn(), at(0))
	if err != nil {
		t.Fatalf("usableToken: %v", err)
	}
	if got.AccessToken != "access-1" {
		t.Fatalf("access token = %q, want the sealed one", got.AccessToken)
	}
	if grants.rotations != 0 {
		t.Fatalf("the token endpoint was reached %d times for a token that was still good", grants.rotations)
	}
}

// EXACTLY ONE caller renews. The lease is claimed with a compare-and-set before
// anything is sent, so the caller that loses it does no work — and does not
// present a refresh token another caller is already spending.
func TestOnlyOneCallerEverSpendsTheRefreshToken(t *testing.T) {
	rt := newRuntime()
	seal(t, rt, livePair(at(-time.Hour)))
	rt.secrets.stored["workspace//"+appSecretKey] = []byte("secret")
	grants := &fakeGrants{rotated: livePair(at(25 * time.Hour))}
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, nil, cursor{}),
		connectionRow(statusConnected, nil, cursor{}),
	}

	if _, _, err := usableToken(t.Context(), rt, grants, connectedConn(), at(0)); err != nil {
		t.Fatalf("the caller that won the lease: %v", err)
	}
	if grants.rotations != 1 {
		t.Fatalf("the token endpoint was reached %d times; a single-use token is spent once", grants.rotations)
	}
	if len(grants.spent) != 1 || grants.spent[0] != "refresh-1" {
		t.Fatalf("the tokens spent were %v, want exactly the one that was on deposit", grants.spent)
	}
	// And the second caller finds a token it does not have to renew at all, which
	// is the ordinary outcome of a race: one renewal, and everybody spends the
	// result of it.
	after, _, err := usableToken(t.Context(), rt, grants, connectedConn(), at(0))
	if err != nil {
		t.Fatalf("the caller arriving after the renewal: %v", err)
	}
	if after.AccessToken != "access-1" || grants.rotations != 1 {
		t.Fatalf("the second caller renewed again (%d rotations) instead of spending what the first one kept", grants.rotations)
	}
}

// A caller that does NOT win the lease does no work and reaches nothing. The
// alternative is two callers presenting the same single-use refresh token, where
// the second is told the credential is dead and would park a connection that is
// about to be perfectly fine.
func TestACallerThatLosesTheLeaseNeverReachesTheTokenEndpoint(t *testing.T) {
	rt := newRuntime()
	seal(t, rt, livePair(at(-time.Hour)))
	rt.secrets.stored["workspace//"+appSecretKey] = []byte("secret")
	grants := &fakeGrants{rotated: livePair(at(25 * time.Hour))}
	// What a conditional UPDATE answers when its predicate does not match, which
	// is what another caller holding the lease looks like from here.
	rt.tx.noRows = map[int]bool{1: true}

	_, _, err := usableToken(t.Context(), rt, grants, connectedConn(), at(0))
	if !errors.Is(err, errRefreshInFlight) {
		t.Fatalf("error = %v, want the in-flight class", err)
	}
	if grants.rotations != 0 {
		t.Fatalf("the losing caller reached the token endpoint %d times", grants.rotations)
	}
}

// The replacement is SEALED before anything is mirrored, used or reported. From
// the moment the provider answers, the old refresh token is dead whatever happens
// next.
func TestTheReplacementIsKeptBeforeAnythingElseHappens(t *testing.T) {
	rt := newRuntime()
	seal(t, rt, livePair(at(-time.Hour)))
	rt.secrets.stored["workspace//"+appSecretKey] = []byte("secret")
	renewed := tokenPair{AccessToken: "access-2", RefreshToken: "refresh-2", ExpiresAt: at(25 * time.Hour)}
	grants := &fakeGrants{rotated: renewed}
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, nil, cursor{}),
		connectionRow(statusConnected, nil, cursor{}),
	}

	got, _, err := usableToken(t.Context(), rt, grants, connectedConn(), at(0))
	if err != nil {
		t.Fatalf("usableToken: %v", err)
	}
	if got.AccessToken != "access-2" {
		t.Fatalf("the caller got %q, want the renewed token", got.AccessToken)
	}
	onDeposit, err := unsealTokens(t.Context(), rt, adminUserID)
	if err != nil {
		t.Fatalf("reading back what was kept: %v", err)
	}
	if onDeposit.RefreshToken != "refresh-2" {
		t.Fatalf("what is on deposit is %q; the old one is dead, so anything but the replacement is unrecoverable", onDeposit.RefreshToken)
	}
	// And a rotation that was kept is recorded, so "when did this connection last
	// renew" is a question with an answer.
	if !published(rt, eventCredentialRotated) {
		t.Fatalf("no rotation was recorded; the events were %v", verbs(rt))
	}
}

// A rotation the provider PERFORMED and this side could not keep is
// unrecoverable, and it parks rather than retrying: presenting the old token
// again cannot succeed, so a second attempt is a slower way to end at the same
// place with the evidence gone.
func TestARotationThatCouldNotBeKeptParksInsteadOfRetrying(t *testing.T) {
	rt := newRuntime()
	seal(t, rt, livePair(at(-time.Hour)))
	rt.secrets.stored["workspace//"+appSecretKey] = []byte("secret")
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, nil, cursor{}),
		connectionRow(statusReauth, nil, cursor{}),
	}
	grants := &fakeGrants{rotated: livePair(at(25 * time.Hour))}
	// The custodian refuses the write AFTER the provider has rotated, which is
	// the exact ordering this whole file is arranged around.
	rt.secrets.putUserErr = errors.New("the custodian is unavailable")

	_, _, err := usableToken(t.Context(), rt, grants, connectedConn(), at(0))
	if !errors.Is(err, errRotationLost) {
		t.Fatalf("error = %v, want the lost-rotation class", err)
	}
	if grants.rotations != 1 {
		t.Fatalf("the token endpoint was reached %d times, want exactly once — a lost rotation must not be tried again", grants.rotations)
	}
	sql, args := rt.tx.statementMentioning(t, "last_error_class = $3")
	if !strings.Contains(sql, "status = $2") {
		t.Fatalf("the park did not set a status: %s", sql)
	}
	if args[1] != statusReauth || args[2] != "refresh_rotation_lost" {
		t.Fatalf("the connection was parked as %v; it must name the state and the reason a human can act on", args)
	}
}

// A rotation whose ANSWER never came back is the worst case and gets its own
// class: the old token may be dead and may not, and nothing this side holds can
// tell. Retrying would present a token that is dead half the time and would
// consume the live half.
func TestARotationWithNoAnswerParksWithTheUncertaintyNamed(t *testing.T) {
	rt := newRuntime()
	seal(t, rt, livePair(at(-time.Hour)))
	rt.secrets.stored["workspace//"+appSecretKey] = []byte("secret")
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, nil, cursor{}),
		connectionRow(statusReauth, nil, cursor{}),
	}
	grants := &fakeGrants{rotateErr: errUnanswered}

	_, _, err := usableToken(t.Context(), rt, grants, connectedConn(), at(0))
	if !errors.Is(err, errRotationLost) {
		t.Fatalf("error = %v, want the lost-rotation class", err)
	}
	_, args := rt.tx.statementMentioning(t, "last_error_class = $3")
	if args[2] != "refresh_outcome_unknown" {
		t.Fatalf("the class recorded was %q, want it to name that nobody knows which way the rotation went", args[2])
	}
}

// A refusal of the refresh token itself parks too, and names the credential
// rather than the account's package — the two send an operator to different
// places and one of them costs money.
func TestARefusedRefreshTokenParksAsACredentialProblem(t *testing.T) {
	rt := newRuntime()
	seal(t, rt, livePair(at(-time.Hour)))
	rt.secrets.stored["workspace//"+appSecretKey] = []byte("secret")
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, nil, cursor{}),
		connectionRow(statusReauth, nil, cursor{}),
	}
	grants := &fakeGrants{rotateErr: errUnauthorized}

	_, _, err := usableToken(t.Context(), rt, grants, connectedConn(), at(0))
	if !errors.Is(err, errUnauthorized) {
		t.Fatalf("error = %v, want the credential refusal", err)
	}
	_, args := rt.tx.statementMentioning(t, "last_error_class = $3")
	if args[1] != statusReauth || args[2] != "refresh_token_rejected" {
		t.Fatalf("parked as %v, want reauth_required naming the rejected token", args)
	}
}

// A provider that was simply unreachable spent NOTHING, so the lease is released
// and the next tick may try again. Parking here would end a connection over a
// network blip.
func TestAnUnreachableProviderReleasesTheLeaseInsteadOfParking(t *testing.T) {
	rt := newRuntime()
	seal(t, rt, livePair(at(-time.Hour)))
	rt.secrets.stored["workspace//"+appSecretKey] = []byte("secret")
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, nil, cursor{}),
		connectionRow(statusConnected, nil, cursor{}),
	}
	grants := &fakeGrants{rotateErr: errTransient}

	_, _, err := usableToken(t.Context(), rt, grants, connectedConn(), at(0))
	if !errors.Is(err, errTransient) {
		t.Fatalf("error = %v, want the transient class", err)
	}
	sql, _ := rt.tx.statementMentioning(t, "refresh_claimed_at = NULL")
	if strings.Contains(sql, "status = $2") {
		t.Fatalf("the connection was parked over an unreachable provider: %s", sql)
	}
}

// The app secret is read BEFORE the lease is claimed: discovering it is missing
// while holding the lease would shut the renewal for the lease's whole length
// over a fault that has nothing to do with the provider.
func TestAMissingAppSecretIsFoundBeforeTheLeaseIsClaimed(t *testing.T) {
	rt := newRuntime()
	seal(t, rt, livePair(at(-time.Hour)))
	grants := &fakeGrants{}

	_, _, err := usableToken(t.Context(), rt, grants, connectedConn(), at(0))
	if !errors.Is(err, errCredentialGone) {
		t.Fatalf("error = %v, want the credential-gone class", err)
	}
	if len(rt.tx.statements) != 0 {
		t.Fatalf("the lease was touched before the app secret was checked: %v", rt.tx.statements)
	}
	if grants.rotations != 0 {
		t.Fatalf("the token endpoint was reached %d times with no app secret to present", grants.rotations)
	}
}

// Withdrawing a credential takes BOTH the token pair and any PKCE material, and a
// key that already held nothing is not an error — this is the withdrawal path,
// and "it was already gone" is the outcome asked for.
func TestWithdrawingACredentialTakesEverythingSealedForThatAdmin(t *testing.T) {
	rt := newRuntime()
	seal(t, rt, livePair(at(20*time.Hour)))
	if err := rt.secrets.PutUser(t.Context(), adminUserID, verifierKey, []byte("v")); err != nil {
		t.Fatalf("depositing a verifier: %v", err)
	}

	if err := forgetCredential(t.Context(), rt, adminUserID); err != nil {
		t.Fatalf("forgetCredential: %v", err)
	}
	for _, key := range []string{tokenKey, verifierKey} {
		if _, still := rt.secrets.stored["user/"+adminUserID+"/"+key]; still {
			t.Fatalf("%q is still on deposit after the credential was withdrawn", key)
		}
	}
	// And again, over nothing at all.
	if err := forgetCredential(t.Context(), rt, adminUserID); err != nil {
		t.Fatalf("withdrawing a credential twice reported an error: %v", err)
	}
}

// published reports whether the unit announced a verb.
func published(rt *fakeRuntime, verb string) bool {
	for _, event := range rt.tx.published {
		if event.Verb == verb {
			return true
		}
	}
	return false
}

// verbs is what the unit announced, for a failure message that says what DID
// happen rather than only what did not.
func verbs(rt *fakeRuntime) []string {
	var found []string
	for _, event := range rt.tx.published {
		found = append(found, event.Verb)
	}
	return found
}

// The park is a SEPARATE transaction from whatever failed, because the write that
// must record a failure cannot be the write that failed.
func TestAParkNamesTheStateAndTheReasonAndClearsTheLease(t *testing.T) {
	rt := newRuntime()
	rt.tx.singleRows = [][]any{connectionRow(statusTierLapse, nil, cursor{})}

	parked, err := park(t.Context(), rt, connectedConn(), "package_too_low", statusTierLapse)
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	if parked.Status != statusTierLapse {
		t.Fatalf("status = %q, want %q", parked.Status, statusTierLapse)
	}
	sql, _ := rt.tx.statementMentioning(t, "last_error_class = $3")
	if !strings.Contains(sql, "refresh_claimed_at = NULL") {
		t.Fatalf("a park left the renewal lease held, so the row would also look busy: %s", sql)
	}
	if !published(rt, eventTierLapsed) {
		t.Fatalf("the park announced %v, want the tier-lapsed verb", verbs(rt))
	}
}

// A park against a connection that has since been removed writes nothing and
// reports no error: there is no row to repair and recreating one would resurrect
// a connection somebody deleted.
func TestParkingARemovedConnectionIsNotAnError(t *testing.T) {
	rt := newRuntime()
	rt.tx.noRows = map[int]bool{1: true}

	if _, err := park(t.Context(), rt, connectedConn(), "credential_missing", statusReauth); err != nil {
		t.Fatalf("park: %v", err)
	}
	if len(rt.tx.published) != 0 {
		t.Fatalf("a removed connection was announced as parked: %v", verbs(rt))
	}
}

// The ledger image carries no credential, and that is true by construction rather
// than by filtering: the row has no token column, so an audit trail of
// connections cannot become a place credentials are kept in the clear.
func TestNoLedgerImageCanCarryACredential(t *testing.T) {
	conn := connectedConn()
	image, err := connectionImage(&conn)
	if err != nil {
		t.Fatalf("connectionImage: %v", err)
	}
	var rendered map[string]any
	if err := json.Unmarshal(image, &rendered); err != nil {
		t.Fatalf("the image is not JSON: %v", err)
	}
	for field := range rendered {
		if strings.Contains(field, "token") && field != "access_token_expires_at" {
			t.Fatalf("the ledger image carries %q", field)
		}
		if strings.Contains(field, "secret") {
			t.Fatalf("the ledger image carries %q", field)
		}
	}
	if absent, err := connectionImage(nil); err != nil || absent != nil {
		t.Fatalf("connectionImage(nil) = %v, %v; a create has no before and an erase has no after", absent, err)
	}
}

// A recorded change with neither image is a programming error rather than a row
// with no id, and it says so instead of writing one.
func TestRecordingAChangeWithNoImageAtAllIsRefused(t *testing.T) {
	rt := newRuntime()
	err := rt.Tx(t.Context(), func(ctx context.Context, tx extension.Tx) error {
		return recordConnection(ctx, tx, extension.AuditUpdate, eventPolled, nil, nil)
	})
	if err == nil {
		t.Fatal("a change with no image at all was recorded")
	}
}
