// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The no-regression pin. Endpoints on this wire are not uniformly tolerant of
// the content-parts array, so a request carrying no attachment must marshal to
// exactly the bytes this adapter sent before parts existed: `"content"` a bare
// string. Written as literal expected JSON rather than as a round-trip, because
// a round-trip through the same type would agree with itself whatever it emits.
func TestATextOnlyTurnMarshalsToABareStringNotAPartsArray(t *testing.T) {
	got, err := json.Marshal(openAICompatMessages("be brief", []model.Message{
		{Role: roleUser, Content: "hello"},
		{Role: roleAssistant, Content: "hi"},
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	const want = `[{"role":"system","content":"be brief"},` +
		`{"role":"user","content":"hello"},` +
		`{"role":"assistant","content":"hi"}]`
	if string(got) != want {
		t.Fatalf("text-only wire changed shape\n got: %s\nwant: %s", got, want)
	}
}

func TestAnImageBecomesAnImageURLPartOnTheLastUserTurn(t *testing.T) {
	msgs := openAICompatMessages("sys", []model.Message{
		{Role: roleUser, Content: "first"},
		{Role: roleAssistant, Content: "answered"},
		{Role: roleUser, Content: "read this"},
	}, []model.Attachment{{MIME: "image/png", Bytes: []byte("PNG")}})

	if len(msgs) != 4 {
		t.Fatalf("want 4 messages, got %d", len(msgs))
	}
	for _, i := range []int{0, 1, 2} {
		if len(msgs[i].Content.Parts) != 0 {
			t.Fatalf("message %d must keep its string body, got parts %v", i, msgs[i].Content.Parts)
		}
	}
	parts := msgs[3].Content.Parts
	if len(parts) != 2 {
		t.Fatalf("want the turn's text plus the image, got %d parts", len(parts))
	}
	// The turn's own text is promoted to a leading part rather than dropped:
	// once Parts is non-empty the string body no longer reaches the wire.
	if parts[0].Type != "text" || parts[0].Text != "read this" {
		t.Errorf("the turn's text must survive as a part, got %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
		t.Fatalf("want an image_url part, got %+v", parts[1])
	}
	if want := "data:image/png;base64,"; !strings.HasPrefix(parts[1].ImageURL.URL, want) {
		t.Errorf("inline bytes must become a data URL, got %q", parts[1].ImageURL.URL)
	}
}

func TestAnAttachmentURIIsPassedThroughUnchanged(t *testing.T) {
	msgs := openAICompatMessages("", []model.Message{{Role: roleUser, Content: "x"}},
		[]model.Attachment{{MIME: "image/png", URI: "https://files.example/a.png"}})
	part := msgs[0].Content.Parts[1]
	if part.ImageURL == nil || part.ImageURL.URL != "https://files.example/a.png" {
		t.Fatalf("a URI attachment must ride as itself, got %+v", part)
	}
}

// Attachments belong to a user turn. A request whose only message is a system
// prompt has none, so one is created rather than the image being hung off the
// system turn, where several endpoints ignore it.
func TestAnAttachmentWithNoUserTurnGetsOne(t *testing.T) {
	msgs := openAICompatMessages("sys", nil, []model.Attachment{{MIME: "image/png", Bytes: []byte("PNG")}})
	if len(msgs) != 2 {
		t.Fatalf("want the system turn plus a created user turn, got %d", len(msgs))
	}
	if msgs[1].Role != roleUser || len(msgs[1].Content.Parts) != 1 {
		t.Fatalf("want one image part on a new user turn, got %+v", msgs[1])
	}
	// Nothing to promote: an empty body must not become an empty text part.
	if msgs[1].Content.Parts[0].Type != "image_url" {
		t.Errorf("want only the image part, got %+v", msgs[1].Content.Parts[0])
	}
}

func TestDescribeCarriageNamesTheEmptyCase(t *testing.T) {
	if got := describeCarriage(nil); got != "no attachments" {
		t.Errorf("an undeclared binding must read as a sentence, got %q", got)
	}
	if got := describeCarriage([]string{"image/*"}); got != "image/*" {
		t.Errorf("describeCarriage = %q", got)
	}
}
