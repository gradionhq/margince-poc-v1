// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The drain, driven over an in-memory socket against captured frames.
//
// No sleeps and no real clock: the drain decides the queue has gone quiet by
// waiting, and [scriptedSocket] makes that wait elapse exactly when its scripted
// frames run out — which is the same moment the real socket goes silent. A test
// that spent real milliseconds on it would be a test that flakes on a loaded
// machine and says nothing more.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// testQuiet is the quiet period these tests pass. Its value is arbitrary — no
// test waits it out — but it has to be distinguishable from the ping interval,
// because that is how [scriptedSocket.after] tells the two waits apart.
const testQuiet = 3 * time.Second

// scriptedSocket is an in-memory [zaloSocket] that hands over a scripted list of
// frames and records everything written to it.
type scriptedSocket struct {
	mu      sync.Mutex
	pending [][]byte
	written [][]byte

	// quiet closes on the first read that finds nothing left, which is strictly
	// after the last frame reached the drain — so the quiet wait can only become
	// ready once there is genuinely nothing else to deliver.
	quiet     chan struct{}
	quietOnce sync.Once

	// pingElapsed, when non-nil, is what a wait on the ping interval returns, so
	// a test can make the keep-alive fire on demand.
	pingElapsed chan time.Time
	// quietNever holds the quiet wait open, so a test that is about some OTHER
	// way for the drain to end cannot have quiet win the race instead.
	quietNever bool
	// failWriteCmd refuses writes of one command, which is how a keep-alive can
	// fail on a socket whose backlog requests succeeded.
	failWriteCmd uint16

	done      chan struct{}
	closeOnce sync.Once
}

func newScriptedSocket(frames ...[]byte) *scriptedSocket {
	return &scriptedSocket{pending: frames, quiet: make(chan struct{}), done: make(chan struct{})}
}

func (s *scriptedSocket) read(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		s.quietOnce.Do(func() { close(s.quiet) })
		// A real socket blocks here until somebody closes it, which is exactly
		// how the drain unblocks its reader on the way out.
		select {
		case <-s.done:
			return nil, io.EOF
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	next := s.pending[0]
	s.pending = s.pending[1:]
	s.mu.Unlock()
	return next, nil
}

func (s *scriptedSocket) write(_ context.Context, payload []byte) error {
	frame, err := decodeZaloFrame(payload)
	if err != nil {
		return fmt.Errorf("the drain wrote something that is not a frame: %w", err)
	}
	if s.failWriteCmd != 0 && frame.cmd == s.failWriteCmd {
		return io.ErrClosedPipe
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.written = append(s.written, payload)
	return nil
}

func (s *scriptedSocket) close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

// after stands in for time.After. The quiet wait elapses when the script runs
// out; the ping interval elapses only if a test asked for it.
func (s *scriptedSocket) after(d time.Duration) <-chan time.Time {
	if d != testQuiet {
		if s.pingElapsed != nil {
			return s.pingElapsed
		}
		return make(chan time.Time)
	}
	if s.quietNever {
		return make(chan time.Time)
	}
	elapsed := make(chan time.Time, 1)
	go func() {
		<-s.quiet
		elapsed <- time.Unix(0, 0)
	}()
	return elapsed
}

func (s *scriptedSocket) outbound() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written
}

// drainOver wires a resumed session to one scripted socket and drains it.
func drainOver(t *testing.T, sock *scriptedSocket) ([]zaloInbound, error) {
	t.Helper()
	session := resumeAgainst(t, newChatServer())
	session.after = sock.after
	session.dial = func(context.Context, string, string, string) (zaloSocket, error) { return sock, nil }
	return session.drainInbox(t.Context(), testQuiet)
}

// cipherKeyFrame is the socket's first frame, carrying the payload key.
func cipherKeyFrame() []byte {
	return capturedFrame(zaloCmdCipherKey, 1, []byte(capturedQueueManifest))
}

// messageFrame is one 1:1 message event at the encoding the live socket uses.
func messageFrame(t *testing.T, cmd uint16, subCmd byte, envelope string) []byte {
	t.Helper()
	return capturedFrame(cmd, subCmd, sealCapturedEvent(t, envelope, 2))
}

// THE TEST THIS FILE EXISTS FOR. The offline queue is REQUESTED, not pushed: a
// drain that only reads sits on a healthy socket collecting keepalives while the
// member's messages stay in the queue, and reports an empty inbox. That mistake
// cost the PoC a day, so it gets a test that fails the moment somebody makes the
// listener passive again.
func TestTheDrainAsksForTheBacklogAndOnlyAfterTheCipherKeyHasArrived(t *testing.T) {
	sock := newScriptedSocket(cipherKeyFrame())

	if _, err := drainOver(t, sock); err != nil {
		t.Fatalf("drain: %v", err)
	}

	sent := sock.outbound()
	if len(sent) != 2 {
		t.Fatalf("the drain wrote %d frame(s), want the 1:1 and group backlog requests", len(sent))
	}
	for i, want := range []uint16{zaloCmdUserBacklog, zaloCmdGroupBacklog} {
		frame, err := decodeZaloFrame(sent[i])
		if err != nil {
			t.Fatalf("the drain wrote a frame it could not have sent: %v", err)
		}
		if frame.cmd != want || frame.subCmd != zaloSubCmdRequestBacklog {
			t.Errorf("outbound frame %d is cmd %d sub %d, want %d:%d",
				i, frame.cmd, frame.subCmd, want, zaloSubCmdRequestBacklog)
		}
	}
}

func TestNothingIsAskedForBeforeTheCipherKeyBecauseNothingCouldBeReadYet(t *testing.T) {
	// A frame that is not the cipher key, arriving first. Until the key lands
	// there is no point asking: every reply would be unreadable.
	sock := newScriptedSocket(capturedFrame(zaloCmdPing, zaloSubCmdKeepConnection, []byte(`{"eventId":1}`)))

	if _, err := drainOver(t, sock); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if sent := sock.outbound(); len(sent) != 0 {
		t.Errorf("the drain wrote %d frame(s) before the cipher key arrived, want none", len(sent))
	}
}

// The captured pair is one message the member received and one they sent, which
// comes back as an ordinary inbound frame carrying `uidFrom: "0"`. BOTH are
// returned: filtering is the unit layer's job, and it needs database state.
func TestTheDrainReturnsEverythingItDecodedIncludingTheEchoOfOurOwnSend(t *testing.T) {
	sock := newScriptedSocket(
		cipherKeyFrame(),
		messageFrame(t, zaloCmdUserBacklog, zaloSubCmdRequestBacklog, capturedSelfEcho),
		messageFrame(t, zaloCmdUserMessage, 0, capturedInbound),
	)

	messages, err := drainOver(t, sock)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("the drain returned %d message(s), want both the echo and the inbound one", len(messages))
	}

	echo := messages[0]
	if echo.UIDFrom != "0" {
		t.Errorf("the echo's uidFrom is %q, want the \"0\" that means the member sent it", echo.UIDFrom)
	}
	if echo.MsgID != "8161097159889" || echo.Content != "test A" {
		t.Errorf("the echo read back as %q/%q, want the captured values", echo.MsgID, echo.Content)
	}

	inbound := messages[1]
	if inbound.UIDFrom != "1900000000000000001" || inbound.IDTo != "0" {
		t.Errorf("the inbound message reads %q → %q, want the counterparty → the member", inbound.UIDFrom, inbound.IDTo)
	}
	if inbound.DName != "Nguyễn Văn Mẫu" || inbound.MsgType != "webchat" || inbound.Content != "Test B" {
		t.Errorf("the inbound message read back as %q/%q/%q, want the captured values",
			inbound.DName, inbound.MsgType, inbound.Content)
	}
	// 1786940459649 ms, from the frame's own `ts` and nothing else.
	if want := time.UnixMilli(1786940459649).UTC(); !inbound.OccurredAt.Equal(want) {
		t.Errorf("OccurredAt is %s, want the frame's own ts %s", inbound.OccurredAt, want)
	}
	if !json.Valid(inbound.Raw) {
		t.Error("Raw is not the message object the frame carried")
	}
}

func TestAGroupFrameIsPassedOverBecauseNothingConsumesItYet(t *testing.T) {
	sock := newScriptedSocket(
		cipherKeyFrame(),
		messageFrame(t, zaloCmdGroupBacklog, zaloSubCmdRequestBacklog, capturedInbound),
	)

	messages, err := drainOver(t, sock)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(messages) != 0 {
		t.Errorf("the drain returned %d group message(s), want none until the group phase lands", len(messages))
	}
}

func TestTheDrainStopsWhenTheQueueHasBeenQuietForTheGivenPeriod(t *testing.T) {
	sock := newScriptedSocket(cipherKeyFrame(), messageFrame(t, zaloCmdUserMessage, 0, capturedInbound))

	messages, err := drainOver(t, sock)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("the drain returned %d message(s), want the one that arrived", len(messages))
	}
	// It stopped by itself: the socket was never closed from the far end, and no
	// end-of-queue marker exists to have read.
	select {
	case <-sock.done:
	default:
		t.Error("the drain returned without closing the socket")
	}
}

// The queue has no end marker, so without a ceiling the provider decides how
// long a scheduled tick lasts. The cap is safe because reading does not consume
// the queue: what it leaves behind is still there next tick.
func TestADrainStopsAtItsCeilingRatherThanLettingTheProviderDecideItsLength(t *testing.T) {
	message := onlyMessage(t, capturedInbound)
	frames := [][]byte{cipherKeyFrame()}
	for delivered := 0; delivered <= maxInboundPerDrain; delivered += 100 {
		batch := make([]json.RawMessage, 100)
		for i := range batch {
			batch[i] = withField(t, message, "msgId", fmt.Sprint(8161098001435+delivered+i))
		}
		frames = append(frames, messageFrame(t, zaloCmdUserMessage, 0, envelopeAround(t, batch...)))
	}

	messages, err := drainOver(t, newScriptedSocket(frames...))
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(messages) != maxInboundPerDrain {
		t.Errorf("the drain returned %d messages, want it to stop at the %d ceiling", len(messages), maxInboundPerDrain)
	}
}

func TestADrainWhoseBudgetExpiresSaysSoRatherThanReportingAnEmptyQueue(t *testing.T) {
	sock := newScriptedSocket(cipherKeyFrame())
	sock.quietNever = true

	session := resumeAgainst(t, newChatServer())
	session.after = sock.after
	ctx, cancel := context.WithCancel(t.Context())
	// The member's turn runs out the moment the socket is open, which is the
	// live shape: the per-member budget is what bounds a queue that never quiets.
	session.dial = func(context.Context, string, string, string) (zaloSocket, error) {
		cancel()
		return sock, nil
	}

	_, err := session.drainInbox(ctx, testQuiet)
	if err == nil {
		t.Fatal("a drain whose budget was gone returned successfully")
	}
	// Either route is legitimate — the loop notices the budget, or the reader
	// does — and both have to carry the cause rather than look like an empty
	// queue.
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the failure reads %q, and it has to carry the expired budget as its cause", err)
	}
}

// A malformed frame is REFUSED rather than skipped, and the whole drain fails
// with it. That is sound because reading does not consume the queue — the next
// tick reads the same messages — whereas skipping would lose a record silently,
// since the server marks a frame delivered the moment it pushes it.
func TestASocketMessageTooShortToBeAFrameFailsTheDrainRatherThanPanicking(t *testing.T) {
	sock := newScriptedSocket(cipherKeyFrame(), []byte{1, 0xF5})

	_, err := drainOver(t, sock)
	if err == nil {
		t.Fatal("a two-byte socket message was accepted, want a refusal")
	}
	if !strings.Contains(err.Error(), "frame header") {
		t.Errorf("the failure reads %q, and it has to name the header it could not read", err)
	}
}

func TestAMessageWhoseTimestampWillNotParseIsRefusedRatherThanStampedWithTheClock(t *testing.T) {
	for name, ts := range map[string]any{
		"a value that is not a number": "not-a-timestamp",
		"an empty string":              "",
		"the epoch":                    "0",
	} {
		t.Run(name, func(t *testing.T) {
			broken := withField(t, onlyMessage(t, capturedInbound), "ts", ts)
			sock := newScriptedSocket(cipherKeyFrame(),
				messageFrame(t, zaloCmdUserMessage, 0, envelopeAround(t, broken)))

			_, err := drainOver(t, sock)
			if err == nil {
				t.Fatal("a message with an unreadable ts was accepted, want a refusal")
			}
			if !strings.Contains(err.Error(), "ts is") {
				t.Errorf("the failure reads %q, and it has to name the timestamp it refused", err)
			}
		})
	}
}

func TestAMessageWithNoIDIsRefusedBecauseNothingCouldDeduplicateIt(t *testing.T) {
	broken := withField(t, onlyMessage(t, capturedInbound), "msgId", "")
	sock := newScriptedSocket(cipherKeyFrame(),
		messageFrame(t, zaloCmdUserMessage, 0, envelopeAround(t, broken)))

	_, err := drainOver(t, sock)
	if err == nil {
		t.Fatal("a message with no msgId was accepted, want a refusal")
	}
	if !strings.Contains(err.Error(), "msgId") {
		t.Errorf("the failure reads %q, and it has to name the missing id", err)
	}
}

// `content` is `any` on the wire: a plain string for `webchat`, a structured
// object for every richer kind. A non-string becomes a named placeholder — never
// a Go-syntax dump of a map, which reads to a rep as corruption, and never an
// empty body, which reads as a message nobody sent.
func TestARicherMessageKindBecomesANamedPlaceholderRatherThanADumpOrNothing(t *testing.T) {
	rich := withField(t, onlyMessage(t, capturedInbound), "content",
		map[string]any{"href": "https://cdn.zalo.example/photo.jpg", "thumb": "https://cdn.zalo.example/t.jpg"})
	rich = withField(t, rich, "msgType", "photo")

	messages, err := drainOver(t, newScriptedSocket(cipherKeyFrame(),
		messageFrame(t, zaloCmdUserMessage, 0, envelopeAround(t, rich))))
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("the drain returned %d message(s), want the one that arrived", len(messages))
	}

	if got := messages[0].Content; got != "[zalo photo]" {
		t.Errorf("a photo's content read back as %q, want a named placeholder for its kind", got)
	}
	if strings.Contains(messages[0].Content, "map[") || strings.Contains(messages[0].Content, "href") {
		t.Errorf("the placeholder %q leaks the wire object instead of naming the kind", messages[0].Content)
	}
}

func TestAFirstFrameWithNoCipherKeyIsRefusedBecauseNothingAfterItCouldBeRead(t *testing.T) {
	sock := newScriptedSocket(capturedFrame(zaloCmdCipherKey, 1, []byte(`{"key":"","encrypt":2}`)))

	_, err := drainOver(t, sock)
	if err == nil {
		t.Fatal("a socket that handed over no cipher key was drained anyway")
	}
	if !strings.Contains(err.Error(), "cipher key") {
		t.Errorf("the failure reads %q, and it has to name the missing key", err)
	}
}

// A ping that cannot be sent means the socket is almost certainly gone. Waiting
// out the quiet period after that would report an empty queue where there is a
// broken connection.
func TestAKeepAliveThatCannotBeSentEndsTheDrainRatherThanReportingQuiet(t *testing.T) {
	sock := newScriptedSocket(cipherKeyFrame())
	sock.quietNever = true
	sock.failWriteCmd = zaloCmdPing
	sock.pingElapsed = make(chan time.Time, 1)
	sock.pingElapsed <- time.Unix(0, 0)

	_, err := drainOver(t, sock)
	if err == nil {
		t.Fatal("a drain whose keep-alive could not be sent reported a quiet queue")
	}
	if !strings.Contains(err.Error(), "keep the message socket alive") {
		t.Errorf("the failure reads %q, and a keep-alive failure has to say so", err)
	}
}

func TestASessionIssuedNoSocketEndpointSaysSoRatherThanDiallingNothing(t *testing.T) {
	fake := newChatServer()
	fake.wsEndpoints = nil

	_, err := resumeAgainst(t, fake).drainInbox(t.Context(), testQuiet)
	if err == nil {
		t.Fatal("a session with no socket endpoint was drained anyway")
	}
	if !strings.Contains(err.Error(), "scan a QR again") {
		t.Errorf("the failure reads %q, and it has to say what the member does about it", err)
	}
}

func TestADrainWithNoQuietPeriodIsRefusedRatherThanNeverEnding(t *testing.T) {
	if _, err := resumeAgainst(t, newChatServer()).drainInbox(t.Context(), 0); err == nil {
		t.Fatal("a drain with no quiet period was started, and nothing would have ended it")
	}
}

// The endpoint comes from the session's own service map, which is
// provider-chosen data inside an encrypted response — and the handshake carries
// the member's live cookies.
func TestAMessageSocketIsNeverOpenedToAHostZaloDoesNotOwn(t *testing.T) {
	fake := newChatServer()
	fake.wsEndpoints = []string{"wss://collect.attacker.example"}

	_, err := resumeAgainst(t, fake).drainInbox(t.Context(), testQuiet)
	if err == nil {
		t.Fatal("a socket was opened to a host Zalo does not own")
	}
	if !strings.Contains(err.Error(), "not a Zalo host") {
		t.Errorf("the failure reads %q, and it has to say why the host was refused", err)
	}
}

func TestTheServerSPingIntervalIsWhatTheDrainPingsAt(t *testing.T) {
	fake := newChatServer()
	fake.pingIntervalMS = 45_000

	settings, err := getServerInfo(t.Context(), resumeAgainst(t, fake).c, vectorIMEI)
	if err != nil {
		t.Fatalf("getServerInfo: %v", err)
	}
	if got := settings.pingInterval(); got != 45*time.Second {
		t.Errorf("the ping interval read back as %s, want the 45s the server named", got)
	}
}

func TestAnUnreadablePingIntervalFallsBackToPingingMoreOftenNotLess(t *testing.T) {
	fake := newChatServer()
	fake.pingIntervalMS = 0

	settings, err := getServerInfo(t.Context(), resumeAgainst(t, fake).c, vectorIMEI)
	if err != nil {
		t.Fatalf("getServerInfo: %v", err)
	}
	// Pinging more often than asked keeps a socket alive; less often loses it.
	if got := settings.pingInterval(); got != time.Minute {
		t.Errorf("the fallback interval is %s, want a floor short enough to keep a socket", got)
	}
}

// Zalo answers this one under either spelling, and the correct one has to work
// as well as the typo — a reader that knows only `setttings` would silently fall
// back to a made-up interval on the day Zalo fixes it.
func TestEitherSpellingOfTheSettingsKeyIsRead(t *testing.T) {
	for _, key := range []string{"settings", "setttings"} {
		answer := fmt.Sprintf(`{"error_code":0,"data":{%q:{"features":{"socket":{"ping_interval":30000}}}}}`, key)

		settings, err := parseServerInfo([]byte(answer))
		if err != nil {
			t.Fatalf("parse an answer keyed %q: %v", key, err)
		}
		if got := settings.pingInterval(); got != 30*time.Second {
			t.Errorf("an answer keyed %q read back as %s, want the 30s it named", key, got)
		}
	}
}

func TestServerInfoWithNeitherSpellingFallsBackRatherThanFailing(t *testing.T) {
	settings, err := parseServerInfo([]byte(`{"error_code":0,"data":{}}`))
	if err != nil {
		t.Fatalf("parse an answer carrying no settings: %v", err)
	}
	if got := settings.pingInterval(); got != time.Minute {
		t.Errorf("the interval read back as %s, want the floor", got)
	}
}

func TestARefusedServerInfoIsARefusalRatherThanASilentDefault(t *testing.T) {
	_, err := parseServerInfo([]byte(`{"error_code":102,"error_message":"session expired"}`))
	if err == nil {
		t.Fatal("a refused getServerInfo was read as an answer")
	}
	if !strings.Contains(err.Error(), "session expired") {
		t.Errorf("the refusal reads %q, and it has to carry what Zalo said", err)
	}
}

func TestAChallengePageInPlaceOfServerInfoIsReportedAsOne(t *testing.T) {
	_, err := parseServerInfo([]byte("<html>are you a robot</html>"))
	if err == nil {
		t.Fatal("an HTML challenge page was read as socket settings")
	}
	if !strings.Contains(err.Error(), "html") {
		t.Errorf("the failure reads %q, and it has to quote what arrived instead", err)
	}
}
