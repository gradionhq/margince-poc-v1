// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The QR login, which is TWO operations rather than one.
//
// DESIGN §3.1 describes connecting as "serve the QR, poll the handshake, seal
// the result", and that cannot be one HTTP call: the handshake's own waits are
// a 100s scan poll and a 120s confirm poll, each longer than any sensible
// server timeout and each holding a connection open per connecting member. So
// the screen drives it — start once, then ask how it is going on a timer — and
// the state between the calls is a SEALED SECRET rather than process memory,
// because the in-flight jar is credential material and because in-process state
// would bind the member to whichever api replica served the start.
//
// Both operations bind to rt.Caller().UserID; see connection.go for why that
// rule is the sharpest one in this unit.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// pollBudget is how long ONE connect/status call spends waiting on the scan.
//
// The handshake's own steps are a 100s and a 120s long-poll, and neither fits
// in an HTTP request: a call that outlives every sensible server timeout also
// holds a connection open per connecting member. So the screen polls, and each
// call asks for a few seconds of the provider's wait. Zalo's own client retries
// the same way, so a short poll that answers "still waiting" is the protocol's
// idiom rather than a workaround.
const pollBudget = 5 * time.Second

// handshake is the QR login handshake, injectable for the same reason
// resumeFunc is: the production one dials Zalo, and a login flow proven only
// against a live account is a login flow proven nowhere.
type handshake struct {
	start func(ctx context.Context, opts zaloOptions) (zaloPending, zaloQRCode, error)
	poll  func(ctx context.Context, p zaloPending, opts zaloOptions, budget time.Duration) (zaloPollResult, zaloPending, error)
}

// liveHandshake is the production one.
func liveHandshake() handshake {
	return handshake{start: zaloStartQR, poll: zaloPollQR}
}

// connectStart opens a QR login for the calling member and hands back the code
// to scan.
//
// The in-flight handshake is sealed as a SECRET rather than kept in memory, for
// two reasons that are both about correctness rather than tidiness: the jar it
// holds is credential material, and in-process state would bind the member to
// whichever api replica served this call — a bug that appears only once there
// is more than one.
func connectStart(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	return connectStartVia(ctx, rt, in, liveHandshake())
}

func connectStartVia(ctx context.Context, rt extension.Runtime, in json.RawMessage, hs handshake) (json.RawMessage, error) {
	if _, err := extension.DecodeArgs[struct{}](in); err != nil {
		return nil, err
	}
	member, err := connectingMember(rt)
	if err != nil {
		return nil, err
	}
	pending, code, err := hs.start(ctx, loginOptions())
	if err != nil {
		return nil, err
	}
	sealed, err := json.Marshal(pending)
	if err != nil {
		return nil, fmt.Errorf("zalo-personal: sealing the in-flight login: %w", err)
	}
	if err := rt.Secrets().PutUser(ctx, member, pendingKey, sealed); err != nil {
		return nil, err
	}
	// The image is the PROVIDER's own data URL, passed through untouched. This
	// unit encodes no QR: re-encoding the payload would be a second spelling of
	// a code the provider is the authority on.
	return json.Marshal(struct {
		QRImage   string `json:"qr_image"`
		ExpiresAt string `json:"expires_at"`
	}{QRImage: code.ImageDataURL, ExpiresAt: code.ExpiresAt.UTC().Format(time.RFC3339)})
}

// connectStatus advances the calling member's QR login by one bounded poll and
// says where it got to.
func connectStatus(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	return connectStatusVia(ctx, rt, in, liveHandshake(), resumeSession)
}

func connectStatusVia(ctx context.Context, rt extension.Runtime, in json.RawMessage,
	hs handshake, resume resumeFunc,
) (json.RawMessage, error) {
	if _, err := extension.DecodeArgs[struct{}](in); err != nil {
		return nil, err
	}
	member, err := connectingMember(rt)
	if err != nil {
		return nil, err
	}
	pending, err := pendingLogin(ctx, rt, member)
	if err != nil {
		return nil, err
	}
	result, next, err := hs.poll(ctx, pending, loginOptions(), pollBudget)
	if err != nil {
		// The sealed handshake is left alone: a poll that failed to reach the
		// provider has not spent the code, and deleting it would make a network
		// blip cost the member a re-scan.
		return nil, err
	}
	switch result.State {
	case zaloScanConfirmed:
		return confirmLogin(ctx, rt, member, result, resume)
	case zaloScanDeclined, zaloScanExpired:
		return abandonLogin(ctx, rt, member, result.State)
	case zaloScanWaiting, zaloScanScanned:
		// The ADVANCED handshake is re-sealed: a scan the member has already
		// performed is progress, and re-sealing the stale one would ask them to
		// scan again for nothing.
		return resealLogin(ctx, rt, member, next, result)
	}
	return nil, fmt.Errorf("zalo-personal: the provider reported the scan state %q, which this unit does not recognise — the login has not advanced", result.State)
}

// confirmLogin keeps a scan the member has confirmed.
//
// The order is CREDENTIAL FIRST, ROW SECOND: a row with no session behind it is
// a connection that looks live and is refused at first use, which is the state
// nobody can see is wrong.
//
// The order alone is not enough, though, and the rollback below is the other
// half of it: a sealed session with NO row is invisible to every screen and
// therefore cannot be withdrawn, so a confirm that fails part-way takes its own
// deposit back rather than leaving one behind.
//
// The resume between them is not ceremony. It is the only place the account's
// own uid can be learned — the handshake reports a display name, and the
// session reports whose it is — and it doubles as proof that what was just
// sealed actually works, so a member is told "connected" only once this
// installation has used the credential end to end.
func confirmLogin(ctx context.Context, rt extension.Runtime, member extension.UserID,
	result zaloPollResult, resume resumeFunc,
) (json.RawMessage, error) {
	if result.Sealed == nil {
		return nil, errors.New("zalo-personal: the provider confirmed the scan and returned no session — nothing was deposited, so the login must be started again")
	}
	// ONE Put of ONE document: cookies, device id, user agent and language are
	// halves of a single credential, and a rotation that landed some of them
	// would leave a session that authenticates as a device it is not.
	sealed, err := json.Marshal(*result.Sealed)
	if err != nil {
		return nil, fmt.Errorf("zalo-personal: sealing the session: %w", err)
	}
	if err := rt.Secrets().PutUser(ctx, member, sessionKey, sealed); err != nil {
		return nil, err
	}
	out, err := finishLogin(ctx, rt, member, result, resume)
	if err != nil {
		// THE DEPOSIT IS ROLLED BACK, and this is the one place in the unit
		// where a credential is withdrawn on a failure path rather than on a
		// member's instruction.
		//
		// Everything after the Put above can fail on ordinary flakiness — the
		// resume handshake times out, the database is briefly unreachable — and
		// a session left on deposit after one of those is the worst state this
		// unit can be in: a live credential that reads the member's whole chat
		// history, with no row, so every screen says "not connected" and the
		// disconnect they would reach for has nothing to show them. Authority
		// with no visible holder is exactly what the disconnect ordering exists
		// to prevent, and it must not be reachable by a timeout either.
		//
		// The withdrawal's own failure is JOINED rather than replacing the
		// cause: which of the two happened decides whether a credential is
		// still out there, so neither may hide the other.
		return nil, errors.Join(err, forget(ctx, rt, member, sessionKey))
	}
	return out, nil
}

// finishLogin is everything after the credential is on deposit: learn which
// account it belongs to, drop the spent handshake, and record the connection.
// It is a separate function so its caller can state, in one place, what happens
// to the deposit when any of it fails.
func finishLogin(ctx context.Context, rt extension.Runtime, member extension.UserID,
	result zaloPollResult, resume resumeFunc,
) (json.RawMessage, error) {
	resumed, err := resume(ctx, *result.Sealed)
	if err != nil {
		return nil, err
	}
	if err := forget(ctx, rt, member, pendingKey); err != nil {
		return nil, err
	}
	if err := upsertConnection(ctx, rt, member, resumed.UID(), result.DisplayName); err != nil {
		return nil, err
	}
	return loginProgress(result)
}

// abandonLogin drops a handshake the member declined or let expire. The sealed
// jar goes with it: it can no longer be advanced, so keeping it would leave
// credential material behind for a login that will never complete.
func abandonLogin(ctx context.Context, rt extension.Runtime, member extension.UserID,
	state zaloScanState,
) (json.RawMessage, error) {
	if err := forget(ctx, rt, member, pendingKey); err != nil {
		return nil, err
	}
	return loginProgress(zaloPollResult{State: state})
}

// resealLogin stores the handshake as the poll left it, for the next call.
func resealLogin(ctx context.Context, rt extension.Runtime, member extension.UserID,
	next zaloPending, result zaloPollResult,
) (json.RawMessage, error) {
	sealed, err := json.Marshal(next)
	if err != nil {
		return nil, fmt.Errorf("zalo-personal: sealing the advanced login: %w", err)
	}
	if err := rt.Secrets().PutUser(ctx, member, pendingKey, sealed); err != nil {
		return nil, err
	}
	return loginProgress(result)
}

// loginProgress renders what the screen shows while a member scans. It carries
// no session material and no jar — only the state, and the name and picture the
// provider reports for the account being scanned, so the member can see WHICH
// account they are about to connect before they confirm it.
func loginProgress(result zaloPollResult) (json.RawMessage, error) {
	return json.Marshal(struct {
		State       string `json:"state"`
		DisplayName string `json:"display_name,omitempty"`
		Avatar      string `json:"avatar,omitempty"`
	}{State: string(result.State), DisplayName: result.DisplayName, Avatar: result.Avatar})
}

// pendingLogin reads back the handshake connectStart sealed.
func pendingLogin(ctx context.Context, rt extension.Runtime, member extension.UserID) (zaloPending, error) {
	var pending zaloPending
	sealed, err := rt.Secrets().GetUser(ctx, member, pendingKey)
	if err != nil {
		if errors.Is(err, extension.ErrSecretNotFound) {
			return pending, fmt.Errorf("%w: no QR login is in progress for you — start one before asking how it is going", extension.ErrNotFound)
		}
		return pending, err
	}
	if err := json.Unmarshal(sealed, &pending); err != nil {
		// Nothing later will decode it either, so this says what to do rather
		// than reporting a fault to retry.
		return pending, fmt.Errorf("%w: the in-flight login is not the shape this unit sealed — start the QR login again", extension.ErrInvalid)
	}
	return pending, nil
}

// loginOptions is how this unit presents itself to the QR handshake, and it is
// EMPTY deliberately.
//
// The user agent is not a header preference here: Zalo derives the device id it
// binds a session to from it, so the string that logs a member in is credential
// material and must be the same one every later call presents. The protocol
// layer pins it and seals it into the credential; a copy in this file would be
// a second spelling of it, and the day the two drift a live session starts
// looking like a different device.
func loginOptions() zaloOptions {
	return zaloOptions{}
}
