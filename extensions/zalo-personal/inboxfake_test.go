// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The provider seam the capture half of this unit is driven through: one member's
// resumed session, faked, so the fairness order, the three filters, the record
// mapping and the cursor are all exercised without a socket.
//
// THE FRAMES ARE THE REAL ONES. They are decoded out of the captured envelopes in
// zalocapture_test.go by the unit's own production mapper, so a fixture here
// cannot drift from what Zalo actually sends — and, more to the point, the
// self-echo test is only worth anything if the echo is a real echo. A hand-built
// frame with `uidFrom: "0"` would prove that this suite agrees with itself.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// memberZaloUID is the connected account's own id, and counterpartyZaloUID is the
// person on the other end — the same placeholder the captured envelopes carry, so
// a frame decoded from one of them matches a verdict scripted here.
const (
	memberZaloUID       = "1800000000000000002"
	counterpartyZaloUID = "1900000000000000001"
)

// The two message ids the capture carries, spelled once. The inbound one is the
// HIGHER of the two, which is what makes an ordering assertion mean something.
const (
	echoMsgID    = "8161097159889"
	inboundMsgID = "8161098001435"
)

// memberIMEI is the device identity inside the scripted sealed credential. The
// fake provider keys on it, which is how a fleet test gives two members two
// different inboxes through one openFunc.
const memberIMEI = "11111111-2222-3333-4444-555555555555"

// capturedFrames decodes the two real captured messages: this member's own echo,
// and a message somebody sent them.
func capturedFrames(t *testing.T) (echo, inbound zaloInbound) {
	t.Helper()
	echo, err := zaloInboundFrom(onlyMessage(t, capturedSelfEcho))
	if err != nil {
		t.Fatalf("decoding the captured self-echo: %v", err)
	}
	inbound, err = zaloInboundFrom(onlyMessage(t, capturedInbound))
	if err != nil {
		t.Fatalf("decoding the captured inbound message: %v", err)
	}
	return echo, inbound
}

// fakeInbox is one member's resumed session.
type fakeInbox struct {
	uid       string
	frames    []zaloInbound
	drainErr  error
	drains    int
	quiet     time.Duration
	roster    []zaloFriend
	rosterErr error
}

func (f *fakeInbox) UID() string { return f.uid }

func (f *fakeInbox) drainInbox(_ context.Context, quiet time.Duration) ([]zaloInbound, error) {
	f.drains++
	f.quiet = quiet
	if f.drainErr != nil {
		return nil, f.drainErr
	}
	return f.frames, nil
}

func (f *fakeInbox) friends(context.Context) ([]zaloFriend, error) {
	if f.rosterErr != nil {
		return nil, f.rosterErr
	}
	return f.roster, nil
}

// fakeProvider hands out one inbox per sealed credential, keyed by the device
// identity inside it. A credential it does not recognise fails to resume, which
// is the case a test uses for a session Zalo no longer accepts.
type fakeProvider struct {
	byIMEI map[string]*fakeInbox
	opens  int
	// openErr is why the session could not be opened, for the case that is NOT a
	// credential Zalo has stopped accepting: a request that never reached Zalo at
	// all. The two have opposite recoveries, so a test has to be able to say which
	// one happened.
	openErr error
}

func newProvider(inboxes map[string]*fakeInbox) *fakeProvider {
	return &fakeProvider{byIMEI: inboxes}
}

func (p *fakeProvider) open() openFunc {
	return func(_ context.Context, sealed zaloSealed) (inbox, error) {
		p.opens++
		if p.openErr != nil {
			return nil, p.openErr
		}
		opened, ok := p.byIMEI[sealed.IMEI]
		if !ok {
			return nil, errors.New("this session is no longer accepted")
		}
		return opened, nil
	}
}

// depositSession puts a usable sealed credential in one member's namespace. It
// goes through the real secret port rather than the map behind it, so a handler
// that reads it the way production does finds it.
func depositSession(t *testing.T, rt *fakeRuntime, member extension.UserID, imei string) {
	t.Helper()
	sealed := []byte(`{"cookies":[{"name":"zpsid","value":"v"}],"imei":"` + imei +
		`","user_agent":"ua","language":"vi"}`)
	if err := rt.secrets.PutUser(context.Background(), member, sessionKey, sealed); err != nil {
		t.Fatalf("depositing a session: %v", err)
	}
}

// allowRow scripts one stored verdict in allowlistColumns order, so a column
// added to that projection is one edit in the fixtures. It carries NO cursor,
// which is the state of a conversation the member has just allowed — and the state
// that has to let everything Zalo is still holding through.
func allowRow(id, counterparty string, mode verdict, name string) []any {
	return []any{id, counterparty, string(mode), name, 1}
}

// cursorRow scripts one conversation's reading position, in the order cursorsOf
// projects. A conversation with NO row here has no bookmark, which is the state that
// lets everything Zalo is still holding for it through — so most fixtures script none.
//
// It is written UNDER THE MODE the member is in now, which is the ordinary state and
// the one every fixture that is not about the floor wants: theModeChosenAt is the floor
// this suite stamps, and an hour past it is after every floor any fixture here moves to.
func cursorRow(counterparty, at string) []any {
	return cursorRowWrittenAt(counterparty, at, theModeChosenAt().Add(time.Hour))
}

// cursorRowWrittenAt scripts a reading position together with WHEN it was written,
// which is what a test about a bookmark older than the member's current mode needs: a
// bookmark minted under a previous answer says where THAT answer got to and nothing
// about the mode in force now.
func cursorRowWrittenAt(counterparty, at string, written time.Time) []any {
	return []any{counterparty, at, written}
}

// floorRow scripts one conversation's consent floor, in the order floorsOf projects:
// the counterparty, and the instant their last explicit exclusion was lifted.
//
// A conversation with NO row here was never explicitly excluded and carries no floor —
// which is the state that lets a newly named conversation collect its whole backlog,
// so most fixtures script none.
func floorRow(counterparty string, notBefore time.Time) []any {
	return []any{counterparty, notBefore}
}

// storedFloor scripts one floor as the SINGLE-row reads see it — floorColumns, which
// carries the row id the ledger records against and the version that tells a first
// exclusion from a second.
func storedFloor(id, counterparty string, notBefore time.Time, version int) []any {
	return []any{id, counterparty, notBefore, version}
}

// floorEntryID is a floor row id, canonical because the ledger's own grammar refuses
// anything that is not a lower-case UUID.
const floorEntryID = "9c5d3e4f-6071-4c8d-afbf-2a3b4c5d6e7f"

// entryID and secondEntryID are verdict row ids, canonical because the ledger's
// own grammar refuses anything that is not a lower-case UUID.
const (
	entryID       = "7a3b1c2d-4e5f-4a6b-8c9d-0e1f2a3b4c5d"
	secondEntryID = "8b4c2d3e-5f60-4b7c-9dae-1f2a3b4c5d6e"
)

// secondConnectionID and secondUserID are the other member of a fleet test.
const (
	secondConnectionID = "6d3b7f02-1e54-4cc9-ab88-9f2e4c6d5b31"
	secondUserID       = "2c7e3a49-88d5-4b2a-9e01-6f4b8c3d2a15"
)
