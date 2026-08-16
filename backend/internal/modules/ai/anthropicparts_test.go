// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The no-regression pin. A request carrying no attachment must marshal to
// exactly the bytes this adapter sent before content blocks existed: `"content"`
// a bare string. Written as literal expected JSON rather than as a round-trip,
// because a round-trip through the same type would agree with itself whatever it
// emits.
func TestAnthropicTextOnlyTurnsMarshalToBareStrings(t *testing.T) {
	got, err := json.Marshal(anthropicMessages([]model.Message{
		{Role: roleUser, Content: "hello"},
		{Role: roleAssistant, Content: "hi"},
		// An empty body is the case a shape change is most likely to alter: the
		// old string field carried no omitempty, so "" went on the wire as "".
		{Role: roleUser, Content: ""},
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	const want = `[{"role":"user","content":"hello"},` +
		`{"role":"assistant","content":"hi"},` +
		`{"role":"user","content":""}]`
	if string(got) != want {
		t.Fatalf("text-only wire changed shape\n got: %s\nwant: %s", got, want)
	}
}

// The system prompt is Anthropic's own top-level field, so it must NOT appear as
// a leading turn the way it does on the Ollama and OpenAI-compatible shapes —
// sending it twice would put the instructions in the conversation as well.
func TestAnthropicMessagesCarryNoSystemTurn(t *testing.T) {
	msgs := anthropicMessages([]model.Message{{Role: roleUser, Content: "hello"}}, nil)
	if len(msgs) != 1 || msgs[0].Role != roleUser {
		t.Fatalf("want the conversation alone, got %+v", msgs)
	}
}

func TestAnthropicImageBecomesABase64BlockOnTheLastUserTurn(t *testing.T) {
	msgs := anthropicMessages([]model.Message{
		{Role: roleUser, Content: "first"},
		{Role: roleAssistant, Content: "answered"},
		{Role: roleUser, Content: "read this"},
	}, []model.Attachment{{MIME: "image/png", Bytes: []byte("PNG")}})

	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}
	for _, i := range []int{0, 1} {
		if len(msgs[i].Content.Blocks) != 0 {
			t.Fatalf("message %d must keep its string body, got blocks %v", i, msgs[i].Content.Blocks)
		}
	}
	blocks := msgs[2].Content.Blocks
	if len(blocks) != 2 {
		t.Fatalf("want the turn's text plus the image, got %d blocks", len(blocks))
	}
	// The turn's own text is promoted to a leading block rather than dropped:
	// once Blocks is non-empty the string body no longer reaches the wire.
	if blocks[0].Type != "text" || blocks[0].Text != "read this" {
		t.Errorf("the turn's text must survive as a block, got %+v", blocks[0])
	}
	src := blocks[1].Source
	if blocks[1].Type != "image" || src == nil {
		t.Fatalf("want an image block, got %+v", blocks[1])
	}
	if src.Type != "base64" || src.MediaType != "image/png" || src.Data != "UE5H" {
		t.Errorf("inline bytes must become a base64 source, got %+v", src)
	}
	// The base64 rides the `data` field, never a data: URL — that spelling is the
	// OpenAI-compatible wire's, and this API rejects it.
	if strings.Contains(src.Data, "data:") {
		t.Errorf("the base64 source must not carry a data: prefix, got %q", src.Data)
	}
}

// An https URI maps to the API's url source, which the vendor fetches itself.
func TestAnthropicImageURIBecomesAURLSource(t *testing.T) {
	msgs := anthropicMessages([]model.Message{{Role: roleUser, Content: "x"}},
		[]model.Attachment{{MIME: "image/png", URI: "https://files.example/a.png"}})
	src := msgs[0].Content.Blocks[1].Source
	if src == nil || src.Type != "url" || src.URL != "https://files.example/a.png" {
		t.Fatalf("a URI attachment must ride as a url source, got %+v", src)
	}
	if src.Data != "" || src.MediaType != "" {
		t.Errorf("a url source carries neither data nor a media type, got %+v", src)
	}
}

// Attachments belong to a user turn. A request whose messages hold none — the
// system prompt travels elsewhere on this wire — gets one created rather than
// the image being hung off an assistant turn.
func TestAnthropicAttachmentWithNoUserTurnGetsOne(t *testing.T) {
	msgs := anthropicMessages(nil, []model.Attachment{{MIME: "image/png", Bytes: []byte("PNG")}})
	if len(msgs) != 1 || msgs[0].Role != roleUser {
		t.Fatalf("want one created user turn, got %+v", msgs)
	}
	// Nothing to promote: an empty body must not become an empty text block.
	if len(msgs[0].Content.Blocks) != 1 || msgs[0].Content.Blocks[0].Type != "image" {
		t.Errorf("want only the image block, got %+v", msgs[0].Content.Blocks)
	}
}

// A URI this wire cannot resolve is refused as a carriage failure, not mapped to
// a url source the vendor would reject for a reason naming the wrong thing.
func TestAnthropicRefusesAnAttachmentURIItCannotResolve(t *testing.T) {
	err := anthropicRefuseAttachments([]model.Attachment{{MIME: "image/png", URI: "file-abc123"}})
	if !errors.Is(err, model.ErrAttachmentUnsupported) {
		t.Fatalf("a file handle must be refused as unsupported carriage, got %v", err)
	}
	// The handle itself stays out of the message: a URI can be a signed URL, and
	// an error is the wrong place for one.
	if strings.Contains(err.Error(), "file-abc123") {
		t.Errorf("the refusal must not echo the uri, got %q", err)
	}
	if err := anthropicRefuseAttachments([]model.Attachment{
		{MIME: "image/png", URI: "https://files.example/a.png"},
	}); err != nil {
		t.Errorf("an https url is fetchable and must be carried, got %v", err)
	}
}
