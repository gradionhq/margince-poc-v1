// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The websocket client, and the guard that ships with it.
//
// The client is golang.org/x/net/websocket, chosen because the product module
// already requires it, so the whole of ingress adds no new third-party code to
// this tree (DESIGN §9.6). It sits behind [zaloSocket] so it can be replaced
// without touching a line of protocol code.
//
// THE GUARD IS NOT OPTIONAL, and it is the reason this file dials TLS itself
// instead of handing a URL to the library. x/net/websocket does not reassemble
// a fragmented message AND CANNOT REPORT THAT IT HASN'T: Codec.Receive reads a
// single frame, and hybiFrameHandler.HandleFrame rewrites a continuation
// frame's opcode to the original payload type, so a fragment is
// indistinguishable from a whole message through the public API. Our own frame
// body carries no length, so a truncated payload fails to decode with nothing
// to distinguish it from corruption — and the payloads most likely to fragment
// are exactly the deep backlog drains this feature rests on.
//
// So the byte stream is tapped BENEATH the library — websocket.NewClient
// accepts any ReadWriteCloser, which is what makes this possible at all — RFC
// 6455 frame headers are counted on their way past, and a read REFUSES to
// return a message once a fragment has been seen. This DELIBERATELY converts a
// silent corruption into a loud outage: the failure then arrives once, with an
// error naming the fix, instead of as a mangled backfill nobody can trace. If
// it ever fires, the repair is confined to this file.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"net"
	"sync"

	"golang.org/x/net/websocket"
)

// zaloSocket is everything this protocol needs of a websocket client: whole
// binary messages in and out.
type zaloSocket interface {
	// read returns the next whole binary message.
	read(ctx context.Context) ([]byte, error)
	write(ctx context.Context, payload []byte) error
	close() error
}

// zaloDialer opens one socket. It is a field on [zaloSession] so a test drives
// the protocol over an in-memory socket, and so DESIGN §9.6's swap stays a
// one-line change.
type zaloDialer func(ctx context.Context, wsURL, userAgent, cookies string) (zaloSocket, error)

// zaloWebOrigin is the origin the handshake must present; Zalo refuses the
// upgrade without it.
const zaloWebOrigin = "https://chat.zalo.me"

// maxSocketPayloadBytes bounds one message. The library's own default is far
// too small for a backlog drain, which carries whole conversations in one
// frame, and no bound at all would let the provider decide this process's
// memory.
const maxSocketPayloadBytes = 32 << 20

// socketPort is the only port this connector opens a message socket on.
const socketPort = "443"

// dialZaloSocket opens the message socket for one session.
func dialZaloSocket(ctx context.Context, wsURL, userAgent, cookies string) (zaloSocket, error) {
	config, err := websocket.NewConfig(wsURL, zaloWebOrigin)
	if err != nil {
		return nil, fmt.Errorf("build the websocket configuration: %w", err)
	}

	// The endpoint comes from the session's own service map, so it is
	// provider-chosen data. Checked here for the same reason serviceURL checks
	// a REST host: this handshake carries the member's live session cookies,
	// and an off-allowlist host would be handed the whole session.
	host := config.Location.Hostname()
	if !isZaloHost(host) {
		return nil, fmt.Errorf("refusing to open a message socket to %s: it is not a Zalo host, and the handshake would carry the member's session", host)
	}
	// THE PORT IS PART OF THE ENDPOINT, and it is provider-chosen too. The
	// allowlist above answers for the name only, so a service map that named an
	// allowed host on an arbitrary port would still hand the member's session to
	// whatever answers there. Zalo's message socket is TLS on the default port;
	// anything else is not the endpoint this connector speaks to.
	if port := config.Location.Port(); port != "" && port != socketPort {
		return nil, fmt.Errorf("refusing to open a message socket to %s on port %s: the message socket is TLS on %s, and the handshake would carry the member's session", host, port, socketPort)
	}
	config.Header.Set("Cookie", cookies)
	config.Header.Set("User-Agent", userAgent)

	addr := net.JoinHostPort(host, socketPort)
	raw, err := (&tls.Dialer{Config: &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	}}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", host, err)
	}
	return newXNetSocket(config, raw)
}

// newXNetSocket completes the websocket handshake over an already-connected
// transport, with the fragmentation tap underneath it.
func newXNetSocket(config *websocket.Config, raw net.Conn) (zaloSocket, error) {
	guard := &wireGuard{inHandshake: true}
	conn, err := websocket.NewClient(config, &guardedConn{Conn: raw, guard: guard})
	if err != nil {
		if closeErr := raw.Close(); closeErr != nil {
			return nil, fmt.Errorf("websocket handshake: %w (and closing the socket: %w)", err, closeErr)
		}
		return nil, fmt.Errorf("websocket handshake: %w", err)
	}
	conn.MaxPayloadBytes = maxSocketPayloadBytes
	return &xnetSocket{conn: conn, guard: guard}, nil
}

type xnetSocket struct {
	conn  *websocket.Conn
	guard *wireGuard

	// A drain closes the socket on its way out, and a blocked read is unblocked
	// by that same close, so both paths legitimately call close. It happens
	// once and both callers get the same answer, rather than one of them
	// reporting a socket the other already closed.
	closeOnce sync.Once
	closeErr  error
}

// read returns the next whole message, or refuses because it cannot be sure
// this one IS whole. There is no context plumbing to do: x/net's Receive has no
// deadline of its own, and closing the connection is what unblocks it — which
// is exactly what the drain does on its way out.
func (s *xnetSocket) read(_ context.Context) ([]byte, error) {
	var data []byte
	if err := websocket.Message.Receive(s.conn, &data); err != nil {
		return nil, err
	}
	if n := s.guard.fragmented(); n > 0 {
		return nil, fmt.Errorf(
			"the server fragmented %d message(s) and golang.org/x/net/websocket cannot reassemble them, "+
				"so what it returned is one frame of a larger message rather than the whole of it: "+
				"the connector needs a client that reassembles (DESIGN §9.6)", n)
	}
	return data, nil
}

func (s *xnetSocket) write(_ context.Context, payload []byte) error {
	return websocket.Message.Send(s.conn, payload)
}

func (s *xnetSocket) close() error {
	s.closeOnce.Do(func() { s.closeErr = s.conn.Close() })
	return s.closeErr
}

// wireGuard counts RFC 6455 frame headers on an inbound byte stream. It is fed
// by [guardedConn.Read], so it sees exactly the bytes the library sees, and it
// only ever counts: nothing here can alter what a read returns.
type wireGuard struct {
	mu sync.Mutex

	// buf holds the bytes of a header that arrived split across reads.
	buf []byte
	// skip counts payload bytes still to be passed over before the next header.
	skip int64
	// inHandshake is true until the HTTP 101 response has gone by; the upgrade
	// response shares this stream and is not a frame.
	inHandshake bool

	// fragmentFrames counts frames that PROVE the stream is fragmented, which is
	// deliberately not the same as counting completed fragmented messages.
	//
	// A message can only be counted once its last frame has arrived, and by then
	// the library may already have handed the first fragment up as if it were
	// whole — the tap sits under a buffered reader, so what has passed the tap
	// when Receive returns is only what Receive needed. A frame that says "this
	// message continues", by contrast, is known the moment its 2-byte header goes
	// past, and the library cannot have returned that frame's payload without
	// reading the header first. So the earliest observable fact is the one the
	// guard is built on.
	fragmentFrames int
}

// fragmented reports how many frames have said the message continues past them.
// Anything above zero means at least one message this socket returned was a
// fragment of a larger one.
func (g *wireGuard) fragmented() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fragmentFrames
}

// observe consumes inbound bytes and advances the header parser.
func (g *wireGuard) observe(p []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.buf = append(g.buf, p...)
	for g.step() {
	}
}

// step consumes one parse unit and reports whether another may follow.
//
// "Another may follow" is simply "there are bytes left", and it has to be: one
// Read commonly delivers several frames at once, and a parser that stopped after
// the first header would leave the rest of the buffer unexamined until the next
// read happened to be small. It would then report no fragmentation on a busy
// socket by never having looked — which is the one wrong answer this guard must
// not give. Each branch consumes at least one byte or returns false, so the
// caller's loop terminates.
func (g *wireGuard) step() bool {
	if g.inHandshake {
		return g.skipHandshake()
	}
	if g.skip > 0 {
		n := min(int64(len(g.buf)), g.skip)
		g.buf = g.buf[n:]
		g.skip -= n
		return len(g.buf) > 0
	}
	return g.readHeader()
}

// skipHandshake drops the HTTP 101 response that precedes the first frame.
func (g *wireGuard) skipHandshake() bool {
	terminator := []byte("\r\n\r\n")
	i := bytes.Index(g.buf, terminator)
	if i < 0 {
		// Keep just enough context to match a terminator split across reads.
		if len(g.buf) > len(terminator) {
			g.buf = g.buf[len(g.buf)-len(terminator):]
		}
		return false
	}
	g.buf = g.buf[i+len(terminator):]
	g.inHandshake = false
	return len(g.buf) > 0
}

// readHeader parses one frame header, or reports that more bytes are needed.
func (g *wireGuard) readHeader() bool {
	if len(g.buf) < 2 {
		return false
	}
	fin := g.buf[0]&0x80 != 0
	opcode := g.buf[0] & 0x0f
	masked := g.buf[1]&0x80 != 0

	length := int64(g.buf[1] & 0x7f)
	header := 2
	switch length {
	case 126:
		if len(g.buf) < 4 {
			return false
		}
		length = int64(binary.BigEndian.Uint16(g.buf[2:4]))
		header = 4
	case 127:
		if len(g.buf) < 10 {
			return false
		}
		//nolint:gosec // G115: an RFC 6455 64-bit length has its high bit reserved zero, and a payload that overflowed int64 could not be buffered anyway — MaxPayloadBytes refuses it long before this.
		length = int64(binary.BigEndian.Uint64(g.buf[2:10]))
		header = 10
	}
	if masked {
		header += 4
	}
	if len(g.buf) < header {
		return false
	}
	g.buf = g.buf[header:]
	g.skip = length

	g.record(fin, opcode)
	return len(g.buf) > 0
}

// record accounts one frame. A control frame is skipped: RFC 6455 lets one sit
// between two fragments and forbids fragmenting it, so it says nothing either
// way.
func (g *wireGuard) record(fin bool, opcode byte) {
	const (
		opContinuation = 0
		opText         = 1
		opBinary       = 2
	)
	switch opcode {
	case opContinuation:
		// A continuation frame exists only inside a fragmented message, whether
		// or not it is the last one.
		g.fragmentFrames++
	case opText, opBinary:
		if !fin {
			g.fragmentFrames++
		}
	}
}

// guardedConn tees everything read from the transport into the parser.
type guardedConn struct {
	net.Conn
	guard *wireGuard
}

func (c *guardedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.guard.observe(b[:n])
	}
	return n, err
}
