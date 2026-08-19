// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The websocket client and its fragmentation guard, against a hand-written RFC
// 6455 server.
//
// The server is hand-written rather than golang.org/x/net/websocket's own
// Handler for one reason: the Handler will not fragment a message, and
// fragmentation is the whole question. So these tests write the 101 response and
// the frame headers themselves — which also means the guard is measured against
// bytes rather than against the library's opinion of them.
//
// No real network: the listener is on 127.0.0.1 and nothing is dialled beyond
// it.

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"net"
	"strings"
	"testing"

	"golang.org/x/net/websocket"
)

// wsAcceptMagic is RFC 6455's fixed GUID; the 101 response is not accepted
// without the digest built over it.
const wsAcceptMagic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// rawWSServer serves ONE upgrade and then writes whatever frames a test asks
// for.
type rawWSServer struct {
	listener net.Listener
	frames   [][]byte
}

// binaryFrame is one whole binary message in one frame.
func binaryFrame(payload []byte) []byte {
	return wsFrame(0x2, true, payload)
}

// fragmentsOf splits a payload into a binary frame with FIN clear and a
// continuation frame with FIN set — a message x/net/websocket hands back as a
// whole message with only the first fragment in it.
func fragmentsOf(payload []byte) [][]byte {
	half := len(payload) / 2
	return [][]byte{wsFrame(0x2, false, payload[:half]), wsFrame(0x0, true, payload[half:])}
}

// wsFrame lays out one unmasked server-to-client frame.
func wsFrame(opcode byte, fin bool, payload []byte) []byte {
	first := opcode
	if fin {
		first |= 0x80
	}
	switch {
	case len(payload) < 126:
		return append([]byte{first, byte(len(payload))}, payload...)
	case len(payload) < 1<<16:
		header := []byte{first, 126, 0, 0}
		binary.BigEndian.PutUint16(header[2:4], uint16(len(payload)))
		return append(header, payload...)
	default:
		header := make([]byte, 10)
		header[0], header[1] = first, 127
		binary.BigEndian.PutUint64(header[2:10], uint64(len(payload)))
		return append(header, payload...)
	}
}

func startRawWS(t *testing.T, frames ...[]byte) *rawWSServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &rawWSServer{listener: listener, frames: frames}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil && !strings.Contains(err.Error(), "use of closed") {
			t.Errorf("close the listener: %v", err)
		}
	})

	go server.serve(t)
	return server
}

func (s *rawWSServer) serve(t *testing.T) {
	conn, err := s.listener.Accept()
	if err != nil {
		// The listener closed at the end of the test; there is nothing to serve.
		return
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close the served connection: %v", err)
		}
	}()

	reader := bufio.NewReader(conn)
	var key string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Errorf("read the upgrade request: %v", err)
			return
		}
		if name, value, found := strings.Cut(line, ":"); found && strings.EqualFold(name, "Sec-WebSocket-Key") {
			key = strings.TrimSpace(value)
		}
		if line == "\r\n" {
			break
		}
	}

	digest := sha1.Sum([]byte(key + wsAcceptMagic)) //nolint:gosec // G401/G505: RFC 6455 §4.2.2 defines the handshake digest as SHA-1 over the client key and a fixed GUID. It proves the peer read the key, not secrecy, and any other hash fails the handshake.
	response := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(digest[:]) + "\r\n\r\n"
	if _, err := conn.Write([]byte(response)); err != nil {
		t.Errorf("write the upgrade response: %v", err)
		return
	}
	for _, frame := range s.frames {
		if _, err := conn.Write(frame); err != nil {
			t.Errorf("write a frame: %v", err)
			return
		}
	}
	// Hold the connection until the test's client closes it, so a read that
	// should refuse does not instead see an EOF.
	if _, err := reader.ReadByte(); err != nil {
		return
	}
}

// dialRawWS opens the connector's own client against the hand-written server,
// over the same code path production uses from the handshake down.
func dialRawWS(t *testing.T, server *rawWSServer) zaloSocket {
	t.Helper()
	url := "ws://" + server.listener.Addr().String() + "/"
	config, err := websocket.NewConfig(url, zaloWebOrigin)
	if err != nil {
		t.Fatalf("build the websocket configuration: %v", err)
	}

	raw, err := net.Dial("tcp", server.listener.Addr().String())
	if err != nil {
		t.Fatalf("dial the test server: %v", err)
	}
	sock, err := newXNetSocket(config, raw)
	if err != nil {
		t.Fatalf("websocket handshake: %v", err)
	}
	t.Cleanup(func() {
		if err := sock.close(); err != nil && !strings.Contains(err.Error(), "use of closed") {
			t.Errorf("close the socket: %v", err)
		}
	})
	return sock
}

func TestTheClientReadsAWholeMessageAndWritesOneBack(t *testing.T) {
	payload := sealCapturedEvent(t, capturedInbound, 2)
	frame := capturedFrame(zaloCmdUserMessage, 0, payload)
	sock := dialRawWS(t, startRawWS(t, binaryFrame(frame)))

	got, err := sock.read(context.Background())
	if err != nil {
		t.Fatalf("read a whole message: %v", err)
	}
	if string(got) != string(frame) {
		t.Errorf("the client returned %d bytes, want the %d that were sent", len(got), len(frame))
	}
	if err := sock.write(context.Background(), frame); err != nil {
		t.Fatalf("write a message: %v", err)
	}
}

// THE GUARD. golang.org/x/net/websocket returns one FRAME per Receive and
// rewrites a continuation frame's opcode to the original payload type, so a
// fragment is indistinguishable from a whole message through its public API —
// and our frame body carries no length, so the truncation would surface as a
// decode failure with nothing to tell it apart from corruption. Converting that
// into a loud, named refusal is deliberate.
func TestAFragmentedMessageIsRefusedRatherThanReturnedAsIfItWereWhole(t *testing.T) {
	payload := sealCapturedEvent(t, capturedInbound, 2)
	frame := capturedFrame(zaloCmdUserMessage, 0, payload)
	sock := dialRawWS(t, startRawWS(t, fragmentsOf(frame)...))

	_, err := sock.read(context.Background())
	if err == nil {
		t.Fatal("a fragmented message was returned as if it were whole, which is the silent corruption this guard exists to prevent")
	}
	if !strings.Contains(err.Error(), "fragmented") || !strings.Contains(err.Error(), "DESIGN §9.6") {
		t.Errorf("the refusal reads %q, and it has to name the fragmentation and where the fix is written down", err)
	}
}

// The guard counts frame headers on the raw stream, so it has to be right about
// what a frame boundary is before its verdict means anything — including when a
// header arrives split across two reads and when one read delivers several
// frames at once, both of which are ordinary on a socket.
func TestTheGuardCountsFragmentsHoweverTheReadsFallAcrossTheFrames(t *testing.T) {
	body := make([]byte, 300)
	stream := []byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n\r\n")
	stream = append(stream, binaryFrame(body[:10])...)
	stream = append(stream, wsFrame(0x2, false, body)...)
	stream = append(stream, wsFrame(0x9, true, nil)...) // a ping between two fragments
	stream = append(stream, wsFrame(0x0, true, body)...)

	for _, chunk := range []int{1, 3, 7, 64, len(stream)} {
		guard := &wireGuard{inHandshake: true}
		for offset := 0; offset < len(stream); offset += chunk {
			guard.observe(stream[offset:min(offset+chunk, len(stream))])
		}
		// Two frames say the message continues: the FIN-clear binary frame that
		// opened it and the continuation frame that closed it.
		if got := guard.fragmented(); got != 2 {
			t.Errorf("reading the stream %d bytes at a time counted %d fragment frame(s), want 2", chunk, got)
		}
	}
}

func TestTheGuardSaysNothingIsFragmentedWhenNothingIs(t *testing.T) {
	stream := []byte("HTTP/1.1 101 Switching Protocols\r\n\r\n")
	// One frame of each length encoding, since the header width is what the
	// parser has to get right to keep its place at all.
	for _, size := range []int{0, 125, 126, 4096, 1 << 16} {
		stream = append(stream, binaryFrame(make([]byte, size))...)
	}

	guard := &wireGuard{inHandshake: true}
	guard.observe(stream)
	if got := guard.fragmented(); got != 0 {
		t.Errorf("the guard counted %d fragment frame(s) among whole messages, want 0 — a false positive here is a refused drain", got)
	}
}

func TestAHandshakeToAHostZaloDoesNotOwnIsNeverAttempted(t *testing.T) {
	_, err := dialZaloSocket(context.Background(), "wss://collect.attacker.example/", defaultUserAgent, "zpw_sek=x")
	if err == nil {
		t.Fatal("a socket was opened to a host Zalo does not own")
	}
	if !strings.Contains(err.Error(), "not a Zalo host") {
		t.Errorf("the refusal reads %q, and it has to say why the host was refused", err)
	}
}

// The allowlist answers for the NAME. A service map that named a real Zalo host
// on an arbitrary port would still send the member's session cookies to whatever
// listens there, so the port is refused before the handshake, not after it.
func TestAHandshakeToAZaloHostOnAnUnexpectedPortIsNeverAttempted(t *testing.T) {
	_, err := dialZaloSocket(context.Background(), "wss://chat.zalo.me:8443/", defaultUserAgent, "zpw_sek=x")
	if err == nil {
		t.Fatal("a socket was opened to a Zalo host on a port this connector does not speak")
	}
	if !strings.Contains(err.Error(), "8443") {
		t.Errorf("the refusal reads %q, and it has to name the port it refused", err)
	}
}

func TestAnEndpointThatIsNotAWebsocketURLIsReportedRatherThanDialled(t *testing.T) {
	if _, err := dialZaloSocket(context.Background(), "https://chat.zalo.me/", defaultUserAgent, ""); err == nil {
		t.Fatal("an https endpoint was accepted as a websocket one")
	}
}
