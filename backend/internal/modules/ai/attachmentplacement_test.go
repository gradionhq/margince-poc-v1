// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Every adapter that carries an attachment answers the same question — WHERE in
// the conversation does the document sit — and answers it in its own
// hand-written loop: attachToLastUserTurn (openai.go), geminiAttachToLastUserTurn
// (gemini.go), and the inline walks in openAICompatMessages, anthropicMessages
// and ollamaMessages. Each carries a comment promising the adapters must not
// disagree. This is what checks that they don't.
//
// A disagreement here is not a crash. It is a document silently attached to a
// system turn several endpoints ignore, or to an assistant turn — which produces
// a model answer about a document it never really saw. That is the failure the
// map-or-reject invariant exists to prevent, arriving by a different door, and
// the five copies mean the sixth adapter's author reads whichever one they
// happen to open.
//
// The answer is compared as the TURN, never as an index into the request body:
// anthropic and gemini carry the system prompt outside the message array and the
// other three carry it as a leading turn, so the same placement is a different
// index on different wires. Comparing the role and the text of the turn the
// attachment landed on is the same question asked in a way all five can answer.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// placedAttachment is where one adapter put the attachment, in the vocabulary
// all five wires share. turns is carried so "landed on the last turn" cannot be
// confused with "landed on the only turn".
type placedAttachment struct {
	role  string
	text  string
	index int
	turns int
}

func (p placedAttachment) String() string {
	return fmt.Sprintf("turn %d of %d (role %q, text %q)", p.index, p.turns, p.role, p.text)
}

// wireTurn is one conversation turn read back off a request body: its role, the
// text a reader would see on it, and whether the attachment landed there.
type wireTurn struct {
	role          string
	text          string
	hasAttachment bool
}

// placementIn folds a wire's turns into the one answer under test, and fails the
// read rather than guessing when a body carries no attachment or more than one
// turn holding it — either would make a disagreement look like agreement.
func placementIn(turns []wireTurn) (placedAttachment, error) {
	var found []placedAttachment
	conversation := 0
	for _, t := range turns {
		// The system prompt is not a conversation turn on any of these wires,
		// and two of them do not put it in the array at all.
		if t.role == roleSystem {
			continue
		}
		if t.hasAttachment {
			found = append(found, placedAttachment{role: t.role, text: t.text, index: conversation})
		}
		conversation++
	}
	if len(found) != 1 {
		return placedAttachment{}, fmt.Errorf("want exactly one turn carrying the attachment, got %d", len(found))
	}
	found[0].turns = conversation
	return found[0], nil
}

// jsonContent is the two shapes three of these wires give a turn's body: a bare
// string, or an ordered array of typed parts once the turn carries an image.
// Read as a raw message and resolved here, because a turn that changed shape
// under an attachment is exactly what these tests are reading.
type jsonContent struct {
	text  string
	parts []jsonPart
}

// jsonPart covers the anthropic and OpenAI-compatible part shapes at once: both
// carry a discriminating `type`, a `text` for the text part, and their own
// spelling of the image payload. A part is an attachment when it is not text.
type jsonPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	ImageURL json.RawMessage `json:"image_url"`
	Source   json.RawMessage `json:"source"`
}

func (c *jsonContent) UnmarshalJSON(raw []byte) error {
	if err := json.Unmarshal(raw, &c.text); err == nil {
		return nil
	}
	return json.Unmarshal(raw, &c.parts)
}

// turn folds one polymorphic-content message into the shared shape.
func (c jsonContent) turn(role string) wireTurn {
	if len(c.parts) == 0 {
		return wireTurn{role: role, text: c.text}
	}
	out := wireTurn{role: role}
	for _, p := range c.parts {
		if p.Type == "text" {
			out.text += p.Text
			continue
		}
		out.hasAttachment = true
	}
	return out
}

// readAnthropic and its four siblings are the per-wire readers. Each is the
// inverse of one adapter's mapping and nothing more, so a wire whose shape
// changes fails HERE, loudly, rather than reporting a placement it guessed.
func readAnthropic(body []byte) ([]wireTurn, error) {
	var wire struct {
		Messages []struct {
			Role    string      `json:"role"`
			Content jsonContent `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, err
	}
	turns := make([]wireTurn, 0, len(wire.Messages))
	for _, m := range wire.Messages {
		turns = append(turns, m.Content.turn(m.Role))
	}
	return turns, nil
}

func readOpenAICompat(body []byte) ([]wireTurn, error) { return readAnthropic(body) }

func readOllama(body []byte) ([]wireTurn, error) {
	var wire struct {
		Messages []struct {
			Role    string   `json:"role"`
			Content string   `json:"content"`
			Images  []string `json:"images"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, err
	}
	turns := make([]wireTurn, 0, len(wire.Messages))
	for _, m := range wire.Messages {
		turns = append(turns, wireTurn{role: m.Role, text: m.Content, hasAttachment: len(m.Images) > 0})
	}
	return turns, nil
}

func readOpenAIResponses(body []byte) ([]wireTurn, error) {
	var wire struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, err
	}
	turns := make([]wireTurn, 0, len(wire.Input))
	for _, item := range wire.Input {
		turn := wireTurn{role: item.Role}
		for _, part := range item.Content {
			// input_text / output_text are the conversation; input_image and
			// input_file are the attachment kinds this wire spells.
			if strings.HasSuffix(part.Type, "_text") {
				turn.text += part.Text
				continue
			}
			turn.hasAttachment = true
		}
		turns = append(turns, turn)
	}
	return turns, nil
}

func readGemini(body []byte) ([]wireTurn, error) {
	var wire struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text       string          `json:"text"`
				InlineData json.RawMessage `json:"inlineData"` //nolint:tagliatelle // Google's wire format (camelCase), same as gemini.go's own structs
				FileData   json.RawMessage `json:"fileData"`   //nolint:tagliatelle // Google's wire format (camelCase), same as gemini.go's own structs
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, err
	}
	turns := make([]wireTurn, 0, len(wire.Contents))
	for _, content := range wire.Contents {
		turn := wireTurn{role: content.Role}
		for _, part := range content.Parts {
			if part.InlineData != nil || part.FileData != nil {
				turn.hasAttachment = true
				continue
			}
			turn.text += part.Text
		}
		turns = append(turns, turn)
	}
	return turns, nil
}

// placementFixtureServer answers each wire's happy path and hands back the body
// the adapter actually sent. One server for all five, keyed on path, because a
// per-adapter server would let one adapter's route silently answer another's.
func placementFixtureServer(t *testing.T) (*httptest.Server, func() []byte) {
	t.Helper()
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			// Kept as a failure rather than dropped: a transport read error here
			// would otherwise surface below as "this wire has no messages", which
			// reads as a mapping bug in the code under test.
			t.Errorf("reading the request body for %s: %v", r.URL.Path, err)
			return
		}
		sent = body
		switch {
		case strings.Contains(r.URL.Path, ":generateContent"):
			writeFixture(t, w, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
		case strings.HasPrefix(r.URL.Path, "/v1/messages"):
			writeFixture(t, w, `{"model":"m","content":[{"type":"text","text":"ok"}]}`)
		case strings.HasPrefix(r.URL.Path, "/v1/responses"):
			writeFixture(t, w, `{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
		case strings.HasPrefix(r.URL.Path, "/api/chat"):
			writeFixture(t, w, `{"model":"m","message":{"content":"ok"},"done":true}`)
		default:
			writeFixture(t, w, `{"choices":[{"message":{"content":"ok"}}]}`)
		}
	}))
	return srv, func() []byte { return sent }
}

// placementReaders is the inverse of each adapter's mapping, keyed by provider.
// Enrolment is NOT this map: the test below derives it from knownProviders and
// fails when a provider that carries an image has no reader here, so the sixth
// adapter joins this invariant by existing rather than by being remembered.
var placementReaders = map[string]func([]byte) ([]wireTurn, error){
	providerOpenAI:           readOpenAIResponses,
	providerGemini:           readGemini,
	providerAnthropic:        readAnthropic,
	providerOllama:           readOllama,
	providerOpenAICompatible: readOpenAICompat,
	// vllm rides the openai_compatible transport, so it is the same wire and
	// the same walk; it is listed because it is a separate knownProviders entry
	// that carries images, not because it is a separate mapping.
	providerVLLM: readOpenAICompat,
}

// placementBinding is how a provider is bound for this test: at the fixture
// server, declaring the image lane, since the two OpenAI-compatible wires carry
// an image only when the binding says so (#1324) and declaring it is a no-op on
// the wires whose carriage is a property of the wire.
func placementBinding(provider, baseURL string) ProviderConfig {
	return ProviderConfig{Provider: provider, BaseURL: baseURL, Model: "m", Input: []string{modalityText, modalityImage}}
}

// TestEveryAttachmentCarryingProviderIsUnderThePlacementInvariant is the
// enrolment half. A provider whose Caps() admit an image is a provider that
// places one somewhere, and a placement nothing reads is a placement nothing
// checks.
//
// ProviderFake is the one exemption and it is structural rather than granted:
// it has no conversation wire at all — fakeWire echoes the request rather than
// mapping it to turns — so there is no turn for a document to sit on and
// nothing for a reader to invert.
func TestEveryAttachmentCarryingProviderIsUnderThePlacementInvariant(t *testing.T) {
	for _, env := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "OPENAI_COMPATIBLE_API_KEY"} {
		t.Setenv(env, "k")
	}
	for _, provider := range knownProviders {
		if provider == ProviderFake {
			continue
		}
		if !model.CarriesMIME(capsFor(t, provider, []string{modalityText, modalityImage}), "image/png") {
			continue
		}
		if _, read := placementReaders[provider]; !read {
			t.Errorf("%s carries images but has no wire reader in placementReaders, so nothing checks where it puts one — "+
				"add the inverse of its message mapping and it joins the cross-adapter placement test below", provider)
		}
	}
}

// TestEveryAdapterPlacesAnAttachmentOnTheSameTurn is the cross-adapter half of
// the placement invariant. Each adapter's own test pins the answer for that
// wire; this one asserts they AGREE, and it does not replace them.
func TestEveryAdapterPlacesAnAttachmentOnTheSameTurn(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("GEMINI_API_KEY", "k")
	t.Setenv("ANTHROPIC_API_KEY", "k")
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "k")

	png := model.Attachment{MIME: "image/png", Bytes: []byte("PNG")}
	for _, conversation := range placementConversations() {
		t.Run(conversation.name, func(t *testing.T) {
			srv, lastRequest := placementFixtureServer(t)
			defer srv.Close()

			for name, read := range placementReaders {
				client, err := SelectBrain(placementBinding(name, srv.URL))
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if _, err := client.Complete(context.Background(), model.Request{
					System:      "you are a CRM assistant",
					Messages:    conversation.messages,
					Attachments: []model.Attachment{png},
				}); err != nil {
					t.Fatalf("%s must carry the image, got %v", name, err)
				}
				turns, err := read(lastRequest())
				if err != nil {
					t.Fatalf("%s: reading its own wire back: %v (%s)", name, err, lastRequest())
				}
				got, err := placementIn(turns)
				if err != nil {
					t.Fatalf("%s: %v (%s)", name, err, lastRequest())
				}
				if got.role != roleUser {
					t.Errorf("%s put the attachment on a %q turn; a document belongs to a user turn", name, got.role)
				}
				if got.text != conversation.wantText || got.index != conversation.wantIndex {
					t.Errorf("%s disagrees about where the document sits: got %s, want turn %d carrying %q",
						name, got, conversation.wantIndex, conversation.wantText)
				}
			}
		})
	}
}

// placementConversation is one shape of conversation and the single turn every
// adapter must choose for it.
type placementConversation struct {
	name      string
	messages  []model.Message
	wantIndex int
	wantText  string
}

func placementConversations() []placementConversation {
	return []placementConversation{{
		// The branch every adapter writes by hand and no other test reaches:
		// there is no user turn, so one has to be created rather than the
		// document landing on the assistant turn that is there.
		name:      "no user turn at all",
		messages:  []model.Message{{Role: roleAssistant, Content: "A1"}},
		wantIndex: 1,
		wantText:  "",
	}, {
		// The one that matters most: the LAST turn is the assistant's, so an
		// adapter that simply appends to the end attaches to the wrong speaker.
		name:      "user then assistant",
		messages:  []model.Message{{Role: roleUser, Content: "U1"}, {Role: roleAssistant, Content: "A1"}},
		wantIndex: 0,
		wantText:  "U1",
	}, {
		name: "several user turns",
		messages: []model.Message{
			{Role: roleUser, Content: "U1"},
			{Role: roleAssistant, Content: "A1"},
			{Role: roleUser, Content: "U2"},
		},
		wantIndex: 2,
		wantText:  "U2",
	}}
}
