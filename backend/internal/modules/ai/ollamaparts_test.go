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

// The no-regression pin. A request carrying no image must marshal to exactly the
// bytes this adapter sent before the images array existed — the key absent
// rather than present and empty, since a runner that reads `images: []` as a
// vision request would reload the model for nothing. Written as literal expected
// JSON rather than as a round-trip, because a round-trip through the same type
// would agree with itself whatever it emits.
func TestOllamaTextOnlyTurnsCarryNoImagesKey(t *testing.T) {
	got, err := json.Marshal(ollamaMessages("be brief", []model.Message{
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

// Ollama's images are BARE base64 on the message, not typed parts and not a
// data: URL — the two spellings every other wire here uses. A runner handed a
// data: URL decodes the prefix as image bytes and fails on the image, not on the
// request, so this is asserted as literal bytes.
func TestOllamaImageRidesTheLastUserTurnAsBareBase64(t *testing.T) {
	msgs := ollamaMessages("sys", []model.Message{
		{Role: roleUser, Content: "first"},
		{Role: roleAssistant, Content: "answered"},
		{Role: roleUser, Content: "read this"},
	}, []model.Attachment{{MIME: "image/png", Bytes: []byte("PNG")}})

	if len(msgs) != 4 {
		t.Fatalf("want the system turn plus 3, got %d", len(msgs))
	}
	for _, i := range []int{0, 1, 2} {
		if len(msgs[i].Images) != 0 {
			t.Fatalf("message %d must carry no image, got %v", i, msgs[i].Images)
		}
	}
	last := msgs[3]
	if last.Content != "read this" {
		t.Errorf("the turn's text stays its body on this wire, got %q", last.Content)
	}
	if len(last.Images) != 1 || last.Images[0] != "UE5H" {
		t.Fatalf("want one bare-base64 image, got %v", last.Images)
	}
	if strings.Contains(last.Images[0], "data:") {
		t.Errorf("an ollama image must carry no data: prefix, got %q", last.Images[0])
	}
}

// Attachments belong to a user turn. A request whose only message is the system
// prompt has none, so one is created rather than the image being hung off the
// system turn, which the chat template renders as instructions.
func TestOllamaAttachmentWithNoUserTurnGetsOne(t *testing.T) {
	msgs := ollamaMessages("sys", nil, []model.Attachment{{MIME: "image/png", Bytes: []byte("PNG")}})
	if len(msgs) != 2 {
		t.Fatalf("want the system turn plus a created user turn, got %d", len(msgs))
	}
	if msgs[1].Role != roleUser || len(msgs[1].Images) != 1 || msgs[1].Content != "" {
		t.Fatalf("want one image on a new, empty user turn, got %+v", msgs[1])
	}
}

// The images array is bytes and nothing else: this runner neither fetches a URL
// nor keeps a file registry a handle could name, so a URI attachment is refused
// rather than skipped.
func TestOllamaRefusesAnAttachmentGivenByURI(t *testing.T) {
	err := ollamaRefuseAttachments([]model.Attachment{{MIME: "image/png", URI: "https://files.example/a.png"}})
	if !errors.Is(err, model.ErrAttachmentUnsupported) {
		t.Fatalf("a uri attachment must be refused as unsupported carriage, got %v", err)
	}
	if strings.Contains(err.Error(), "files.example") {
		t.Errorf("the refusal must not echo the uri, got %q", err)
	}
	if err := ollamaRefuseAttachments([]model.Attachment{
		{MIME: "image/png", Bytes: []byte("PNG")},
	}); err != nil {
		t.Errorf("inline image bytes are exactly what this wire takes, got %v", err)
	}
}

// A carried image occupies context the byte heuristic cannot see: its base64
// length is a property of the encoder, not of what the model spends on it. A
// window sized as if the turn were text-only truncates the prompt around the
// image it was asked to read.
func TestOllamaSizesTheWindowForCarriedImages(t *testing.T) {
	turn := []model.Message{{Role: roleUser, Content: "read this"}}
	wireWith := func(n int) ollamaWire {
		atts := make([]model.Attachment, n)
		for i := range atts {
			atts[i] = model.Attachment{MIME: "image/png", Bytes: []byte("PNG")}
		}
		return ollamaWire{Messages: ollamaMessages("", turn, atts)}
	}
	// The budget puts the text-only case on a bucket boundary, so the images are
	// the only thing that can move the answer. Three of them, because the bucket
	// deliberately absorbs small differences — it exists to keep a whole workload
	// on one loaded runner — so a single image proves nothing about the sizing.
	const budget = ollamaContextFloor
	text := wireWith(0).contextWindow(budget)
	if got := wireWith(1).contextWindow(budget); got < text {
		t.Errorf("one image must never narrow the window: %d < %d", got, text)
	}
	if got := wireWith(3).contextWindow(budget); got <= text {
		t.Fatalf("three carried images must widen the window past %d, got %d", text, got)
	}
}
