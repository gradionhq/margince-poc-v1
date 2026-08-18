// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// Ingress: one drain of one member's inbox, and the seam the unit layer sees.
//
// THE FACT THIS FILE EXISTS TO ENCODE: the offline queue must be REQUESTED, not
// waited for. `cmd 510 subCmd 1` is an OUTBOUND control frame. A client that
// connects, takes the cipher key and waits receives keepalives forever on a
// perfectly healthy socket while messages sit in the queue — and it looks
// exactly like an empty inbox. That mistake cost the PoC a day of capturing
// nothing, so the request is part of [inboxDrain.takeCipherKey] and a test
// asserts the bytes leave.
//
// Reading does NOT clear the queue (measured): a second drain minutes later
// returns the identical messages. Delivery is therefore at-least-once, the
// caller dedupes on MsgID, and a cursor advanced after ingest costs at most a
// deduplicated replay rather than a lost record. It is also what makes this
// file's error handling safe — see [zaloSession.drainInbox].
//
// What this file deliberately does NOT do is filter. Self-echo, the allowlist
// and the cursor all need database state, so they belong to the unit layer;
// everything decoded is returned, including the echo of our own sends
// (uidFrom == "0").

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// The shape this file answers in — [zaloInbound] — is declared in zaloframe.go,
// which is the seam the unit layer reads.

// maxInboundPerDrain bounds one drain. The queue has no end-of-queue marker, so
// without a ceiling a busy — or hostile — provider decides how long one
// scheduled tick lasts and how much of this process's memory it holds. The
// bound is safe precisely because reading does not consume the queue: what a
// capped drain leaves behind is still there on the next tick.
const maxInboundPerDrain = 5000

// drainInbox opens a socket on this session, requests the user backlog, collects
// what arrives until the queue goes quiet, closes, and returns what it decoded.
//
// One call, because open/request/collect/close is a unit: a caller that could
// interleave it would get the 510 order wrong, which is the one mistake that
// looks like a working connector with an empty inbox.
//
// EVERY failure discards the whole drain rather than returning what it managed
// to read, and that is sound here for one measured reason: reading does not
// consume the queue. A drain that refuses a frame it cannot decode loses
// nothing — the next tick reads the same messages — whereas a drain that
// returned the good ones and dropped the bad one would lose a message
// permanently and silently, since the server marks a frame delivered the moment
// it pushes it.
func (s *zaloSession) drainInbox(ctx context.Context, quiet time.Duration) ([]zaloInbound, error) {
	if len(s.wsURLs) == 0 {
		return nil, fmt.Errorf("this session was issued no message-socket endpoint, so there is no inbox to drain — the member has to scan a QR again")
	}
	if quiet <= 0 {
		return nil, fmt.Errorf("a drain decides the queue has gone silent by waiting, so it needs a positive quiet period, not %s", quiet)
	}

	settings, err := getServerInfo(ctx, s.c, s.imei)
	if err != nil {
		return nil, err
	}

	conn, err := s.openInbox(ctx)
	if err != nil {
		return nil, err
	}

	// The reader and ping goroutines are unblocked by the close below and told
	// to stop by this cancel, so both are gone before drainInbox returns.
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	messages, drainErr := s.collectInbound(ctx, conn, quiet, settings.pingInterval())
	if closeErr := conn.close(); closeErr != nil {
		if drainErr != nil {
			return nil, fmt.Errorf("%w (and closing the socket: %w)", drainErr, closeErr)
		}
		return nil, fmt.Errorf("the drain read %d message(s), but closing the socket failed, so whether the queue was read to the end is unknown: %w",
			len(messages), closeErr)
	}
	return messages, drainErr
}

// openInbox resolves the endpoint and opens the socket. The `t` parameter is the
// current time in milliseconds, exactly as the web client sends it; the endpoint
// comes from the session's service map rather than a constant, because Zalo
// issues it per session and rebalances its socket fleet.
func (s *zaloSession) openInbox(ctx context.Context) (zaloSocket, error) {
	endpoint := s.wsURLs[0]
	wsURL, err := makeURL(endpoint, map[string]string{"t": fmt.Sprint(s.c.now().UnixMilli())}, true)
	if err != nil {
		return nil, err
	}

	// The handshake is a hand-built request rather than one of this client's,
	// so the cookie header is rendered here instead of by the jar's transport.
	cookies, err := s.c.jar.cookieString(endpoint)
	if err != nil {
		return nil, err
	}

	conn, err := s.dial(ctx, wsURL, s.c.userAgent, cookies)
	if err != nil {
		return nil, fmt.Errorf("open the message socket: %w", err)
	}
	return conn, nil
}

// inboundFrame is one result of the reader goroutine: a frame, or the failure
// that ended the stream.
type inboundFrame struct {
	frame zaloFrame
	err   error
}

// collectInbound runs the drain until the queue goes quiet, the budget runs out,
// or the ceiling is reached.
//
// "Quiet" is the only end-of-queue signal there is: the server sends no marker
// and closes nothing, so the drain reads until nothing has arrived for `quiet`
// and then stops. The timer restarts on every frame, so the wait measures
// silence rather than total duration — which is why the ctx deadline and
// [maxInboundPerDrain] are both still needed to bound the whole thing.
func (s *zaloSession) collectInbound(ctx context.Context, conn zaloSocket, quiet, ping time.Duration) ([]zaloInbound, error) {
	frames := make(chan inboundFrame)
	go readInboundFrames(ctx, conn, frames)

	drain := &inboxDrain{session: s, conn: conn, pingFailed: make(chan error, 1)}

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("the drain ran out of time with %d message(s) read: %w", len(drain.messages), ctx.Err())

		case err := <-drain.pingFailed:
			return nil, err

		case <-s.after(quiet):
			return drain.messages, nil

		case in := <-frames:
			if in.err != nil {
				return nil, fmt.Errorf("the message socket stopped after %d message(s): %w", len(drain.messages), in.err)
			}
			if err := drain.handle(ctx, in.frame, ping); err != nil {
				return nil, err
			}
			if len(drain.messages) >= maxInboundPerDrain {
				return drain.messages, nil
			}
		}
	}
}

// readInboundFrames turns the blocking socket into something selectable. It ends
// when the socket fails, which is also how cancellation reaches it: the drain
// closes the socket on its way out.
func readInboundFrames(ctx context.Context, conn zaloSocket, out chan<- inboundFrame) {
	for {
		data, err := conn.read(ctx)
		result := inboundFrame{}
		if err != nil {
			result.err = err
		} else if result.frame, err = decodeZaloFrame(data); err != nil {
			result.err = err
		}

		select {
		case out <- result:
		case <-ctx.Done():
			return
		}
		if result.err != nil {
			return
		}
	}
}

// inboxDrain is the state one drain accumulates.
type inboxDrain struct {
	session   *zaloSession
	conn      zaloSocket
	cipherKey string
	messages  []zaloInbound

	// pingFailed carries a keep-alive failure back to the drain loop. A ping
	// that cannot be sent means the socket is almost certainly gone, and a
	// drain that kept waiting for quiet would report an empty queue instead of
	// a broken connection.
	pingFailed chan error
}

// handle routes one frame.
func (d *inboxDrain) handle(ctx context.Context, f zaloFrame, ping time.Duration) error {
	switch {
	case f.cmd == zaloCmdCipherKey:
		return d.takeCipherKey(ctx, f, ping)
	case f.cmd == zaloCmdUserMessage,
		f.cmd == zaloCmdUserBacklog && f.subCmd == zaloSubCmdRequestBacklog:
		return d.collectMessages(f)
	}
	// Everything else is passed over on purpose. Group frames (511/521) are a
	// later phase — their backlog is requested here so the queue drains
	// consistently, but nothing consumes them yet — and the rest of the
	// socket's traffic (ping acks, typing, read receipts, presence) is either
	// answered elsewhere or is telemetry this CRM has no business storing.
	return nil
}

// takeCipherKey learns the payload key and then ASKS FOR THE BACKLOG. The order
// is the whole point: nothing encrypted can be read before the key arrives, so
// the request has to wait for this frame — and it has to actually be sent.
func (d *inboxDrain) takeCipherKey(ctx context.Context, f zaloFrame, ping time.Duration) error {
	var parsed struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(f.body, &parsed); err != nil {
		return fmt.Errorf("the socket's first frame is not JSON (%s): %w", truncate(string(f.body), 120), err)
	}
	if parsed.Key == "" {
		return fmt.Errorf("the socket's first frame carried no payload cipher key, so nothing that arrives on it can be read")
	}
	d.cipherKey = parsed.Key

	go d.pingLoop(ctx, ping)
	return d.requestBacklog(ctx)
}

// requestBacklog asks for what arrived while nothing was listening.
//
// THIS IS THE CALL A PASSIVE LISTENER MISSES. The server replays nothing on its
// own: 510:1 (1:1) and 511:1 (groups) are ordinary outbound control frames, and
// a client that only reads gets keepalives on a healthy socket while the queue
// stays full. 511:1 is sent even though group frames land in a later phase, so
// the queue is drained consistently rather than accumulating one half of itself.
func (d *inboxDrain) requestBacklog(ctx context.Context) error {
	for _, cmd := range []uint16{zaloCmdUserBacklog, zaloCmdGroupBacklog} {
		// An empty OBJECT, not an empty body: a frame body is always JSON.
		frame := encodeZaloFrame(cmd, zaloSubCmdRequestBacklog, []byte("{}"))
		if err := d.conn.write(ctx, frame); err != nil {
			return fmt.Errorf("ask for the offline backlog (cmd %d): %w", cmd, err)
		}
	}
	return nil
}

// pingLoop keeps the socket alive at the interval the server itself named. A
// deep drain can outlast that interval, and an unpinged socket is closed
// server-side mid-queue.
func (d *inboxDrain) pingLoop(ctx context.Context, interval time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.session.after(interval):
			if err := d.ping(ctx); err != nil {
				select {
				case d.pingFailed <- err:
				case <-ctx.Done():
				}
				return
			}
		}
	}
}

func (d *inboxDrain) ping(ctx context.Context) error {
	// The web client sends the current time as the event id, and the server
	// echoes it; nothing correlates it, so this is the whole body.
	body := fmt.Appendf(nil, `{"eventId":%d}`, d.session.c.now().UnixMilli())
	if err := d.conn.write(ctx, encodeZaloFrame(zaloCmdPing, zaloSubCmdKeepConnection, body)); err != nil {
		return fmt.Errorf("keep the message socket alive: %w", err)
	}
	return nil
}

// collectMessages decodes one event frame and maps every 1:1 message in it.
func (d *inboxDrain) collectMessages(f zaloFrame) error {
	decoded, err := decodeZaloEvent(f.body, d.cipherKey)
	if err != nil {
		return fmt.Errorf("read the payload of socket frame cmd %d: %w", f.cmd, err)
	}

	var envelope struct {
		Data struct {
			Msgs []json.RawMessage `json:"msgs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(decoded, &envelope); err != nil {
		return fmt.Errorf("parse the message event on socket frame cmd %d: %w", f.cmd, err)
	}

	for _, raw := range envelope.Data.Msgs {
		message, err := zaloInboundFrom(raw)
		if err != nil {
			return err
		}
		d.messages = append(d.messages, message)
	}
	return nil
}

// zaloInboundFrom maps one wire message object.
func zaloInboundFrom(raw json.RawMessage) (zaloInbound, error) {
	// msgId and ts are quoted STRINGS on the socket, unlike the send receipt
	// where msgId arrives as a JSON number. A shape change refuses loudly here
	// rather than being coerced, because both fields are load-bearing: msgId is
	// the dedupe key and ts is the only provider time there is.
	var wire struct {
		MsgID   string `json:"msgId"`
		UIDFrom string `json:"uidFrom"`
		IDTo    string `json:"idTo"`
		DName   string `json:"dName"`
		MsgType string `json:"msgType"`
		TS      string `json:"ts"`

		// Content stays UNDECODED here because it is not one type on the wire —
		// see zaloInboundContent.
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return zaloInbound{}, fmt.Errorf("parse an inbound message (%s): %w", truncate(string(raw), 120), err)
	}
	if wire.MsgID == "" {
		return zaloInbound{}, fmt.Errorf("an inbound message carried no msgId, and without one nothing can tell a redelivery from a new message")
	}

	occurredAt, err := zaloInboundTime(wire.TS)
	if err != nil {
		return zaloInbound{}, err
	}

	return zaloInbound{
		MsgID:      wire.MsgID,
		UIDFrom:    wire.UIDFrom,
		IDTo:       wire.IDTo,
		DName:      wire.DName,
		MsgType:    wire.MsgType,
		Content:    zaloInboundContent(wire.MsgType, wire.Content),
		OccurredAt: occurredAt,
		Raw:        raw,
	}, nil
}

// zaloInboundTime reads the frame's own `ts`, which is unix MILLIseconds carried
// as a string. A value that will not parse is refused rather than defaulted:
// the core refuses a record with no provider time, and substituting now() would
// silently date every message in a recovered backlog to the drain that found it.
func zaloInboundTime(ts string) (time.Time, error) {
	millis, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("an inbound message's ts is %q, which is not the unix-millisecond timestamp the frame is meant to carry: %w", ts, err)
	}
	if millis <= 0 {
		return time.Time{}, fmt.Errorf("an inbound message's ts is %q, which is not a time this message could have happened at", ts)
	}
	return time.UnixMilli(millis).UTC(), nil
}

// zaloInboundContent renders the frame's `content`, which is not one type on the
// wire: a plain string for msgType "webchat", and a structured object for every
// richer kind (photos, files, cards, stickers).
//
// A content that is not a string becomes a NAMED placeholder for its kind. The
// two things it must never become are a Go-syntax dump of the object — which is
// what fmt would produce, and which reads to a rep as corruption — and an empty
// string, which reads as a message nobody sent. Mapping the richer kinds
// properly waits for a captured example of each: no attachment has crossed the
// wire in testing, so the shape would be a guess.
func zaloInboundContent(msgType string, content json.RawMessage) string {
	// A missing or null content is not an empty message either — it is a message
	// whose body this mapping cannot state, so it takes the placeholder too.
	if len(content) > 0 && string(content) != "null" {
		var text string
		if err := json.Unmarshal(content, &text); err == nil {
			return text
		}
	}
	kind := msgType
	if kind == "" {
		kind = "message of an unnamed kind"
	}
	return "[zalo " + kind + "]"
}
