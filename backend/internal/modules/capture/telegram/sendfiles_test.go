// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// The upload path's obligations, against the local stand-in in api_test.go —
// never the real host.
//
// What is under test is what LANDS ON THE WIRE, parsed back out of the multipart
// body rather than substring-matched, because the encoding IS the behaviour
// here: a file part under the wrong field name, a media item pointing at a part
// that is not attached, or a caption on every item of an album are each a
// message that reads as sent and arrives wrong.
//
// Two of the rules asserted here are OURS rather than Telegram's, and the
// distinction matters for what these tests can claim. Telegram validates the
// chat before it validates the media array — a probe against a bogus chat gets
// `chat not found` for a one-item group, an eleven-item group and a mixed-type
// group alike — so the 2-to-10 bound and the uniform-document rule are not
// observable against a stand-in at all. They are asserted here as decisions this
// package makes, and the provider's own enforcement is left to a live send.

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// recordedFile is one file part as it arrived: the field name a media item
// points at, the name and type the header declared, and the bytes.
type recordedFile struct {
	field       string
	filename    string
	contentType string
	body        string
}

// formOf parses the most recent recorded request back into its fields and file
// parts.
//
// The boundary is read out of the recorded content type rather than assumed,
// because multipart mints a fresh random one per request: a test carrying its
// own boundary would be asserting against its own fixture instead of against
// what the client actually sent.
func formOf(t *testing.T, rec *recorder) (map[string]string, []recordedFile) {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(rec.lastContentType(t))
	if err != nil {
		t.Fatalf("parsing the request content type: %v", err)
	}
	if mediaType != "multipart/form-data" {
		t.Fatalf("the upload went out as %q, want multipart/form-data", mediaType)
	}
	reader := multipart.NewReader(strings.NewReader(rec.lastBody(t)), params["boundary"])
	fields := map[string]string{}
	var files []recordedFile
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("reading the next form part: %v", err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("reading the %q part: %v", part.FormName(), err)
		}
		if part.FileName() == "" {
			fields[part.FormName()] = string(body)
			continue
		}
		files = append(files, recordedFile{
			field:       part.FormName(),
			filename:    part.FileName(),
			contentType: part.Header.Get("Content-Type"),
			body:        string(body),
		})
	}
	return fields, files
}

// staged is one file as the send path receives it: already snapshotted out of the
// record library, so the bytes travel with the identity rather than being fetched
// again at transmission.
func staged(name, contentType, body string) connector.OutboundFile {
	return connector.OutboundFile{
		AttachmentID: "01920000-0000-7000-8000-000000000001",
		Filename:     name,
		ContentType:  contentType,
		ByteSize:     int64(len(body)),
		Body:         []byte(body),
	}
}

// carrying is the message every case here uploads: a real chat, a caption, and
// an anchor on the message it answers.
func carrying(files ...connector.OutboundFile) OutboundChannelMessage {
	return OutboundChannelMessage{
		ChatID:           778899,
		Text:             "On its way today.",
		ReplyToMessageID: 4231,
		Files:            files,
	}
}

func TestSendFilesUploadsASingleFileAsADocumentWithTheBodyAsItsCaption(t *testing.T) {
	api, rec := serve(t, http.StatusOK, `{"ok":true,"result":{"message_id":9911}}`)

	id, err := api.SendFiles(context.Background(), "1:secret", carrying(staged("offer.pdf", "application/pdf", "%PDF-1.7 offer")))
	if err != nil {
		t.Fatalf("SendFiles: %v", err)
	}
	if id != 9911 {
		t.Errorf("message id = %d, want 9911 — a later reply threads under it", id)
	}
	// One file is sendDocument, not a one-item album: the Bot API documents
	// sendMediaGroup as taking two to ten.
	if got := rec.lastPath(t); !strings.HasSuffix(got, "/"+methodSendDocument) {
		t.Errorf("the upload went to %q, want the %s method", got, methodSendDocument)
	}

	fields, files := formOf(t, rec)
	for name, want := range map[string]string{
		"chat_id":  "778899",
		"document": "attach://file0",
		"caption":  "On its way today.",
		// The current spelling, carried as a JSON document inside a form field.
		// The deprecated reply_to_message_id is deliberately absent.
		"reply_parameters": `{"message_id":4231}`,
	} {
		if fields[name] != want {
			t.Errorf("form field %s = %q, want %q", name, fields[name], want)
		}
	}
	if len(files) != 1 {
		t.Fatalf("%d file parts attached, want 1", len(files))
	}
	want := recordedFile{field: "file0", filename: "offer.pdf", contentType: "application/pdf", body: "%PDF-1.7 offer"}
	if files[0] != want {
		t.Errorf("file part %+v, want %+v — the bytes, the name and the type all reach the recipient", files[0], want)
	}
}

// The album is the case the uniform-document rule exists for, and the ordering
// is part of the behaviour: the rep chose it, and a recipient reading a numbered
// set of documents in a different order is reading a different message.
func TestSendFilesUploadsAnAlbumAsAUniformDocumentGroup(t *testing.T) {
	api, rec := serve(t, http.StatusOK, `{"ok":true,"result":[{"message_id":9911},{"message_id":9912},{"message_id":9913}]}`)

	if _, err := api.SendFiles(context.Background(), "1:secret", carrying(
		staged("offer.pdf", "application/pdf", "one"),
		staged("photo.jpg", "image/jpeg", "two"),
		staged("terms.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "three"),
	)); err != nil {
		t.Fatalf("SendFiles: %v", err)
	}
	if got := rec.lastPath(t); !strings.HasSuffix(got, "/"+methodSendMediaGroup) {
		t.Errorf("the upload went to %q, want the %s method", got, methodSendMediaGroup)
	}

	fields, files := formOf(t, rec)
	// An image travels as a document like everything else. Telegram refuses a
	// group mixing the two types outright, and a `photo` would be recompressed.
	wantMedia := `[{"type":"document","media":"attach://file0","caption":"On its way today."},` +
		`{"type":"document","media":"attach://file1"},` +
		`{"type":"document","media":"attach://file2"}]`
	if fields["media"] != wantMedia {
		t.Errorf("media field:\n got %s\nwant %s", fields["media"], wantMedia)
	}
	if len(files) != 3 {
		t.Fatalf("%d file parts attached, want 3", len(files))
	}
	for i, want := range []recordedFile{
		{field: "file0", filename: "offer.pdf", contentType: "application/pdf", body: "one"},
		{field: "file1", filename: "photo.jpg", contentType: "image/jpeg", body: "two"},
		{
			field: "file2", filename: "terms.docx",
			contentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", body: "three",
		},
	} {
		if files[i] != want {
			t.Errorf("file part %d is %+v, want %+v", i, files[i], want)
		}
	}
}

// Telegram numbers every item of an album separately but anchors a reply to one
// message, and the first item is the one a client shows the reply against.
func TestSendFilesReportsTheFirstMessageIdOfAnAlbum(t *testing.T) {
	api, _ := serve(t, http.StatusOK, `{"ok":true,"result":[{"message_id":9911},{"message_id":9912}]}`)

	id, err := api.SendFiles(context.Background(), "1:secret", carrying(
		staged("offer.pdf", "application/pdf", "one"),
		staged("terms.pdf", "application/pdf", "two"),
	))
	if err != nil {
		t.Fatalf("SendFiles: %v", err)
	}
	if id != 9911 {
		t.Errorf("album anchor = %d, want the first message id 9911", id)
	}
}

// An absent caption and a stated empty one differ on the wire: Telegram renders
// an empty caption as a blank line under the file. The same holds for the anchor,
// which it reads as a real reference and refuses at zero.
func TestSendFilesOmitsTheCaptionAndTheAnchorWhenThereAreNone(t *testing.T) {
	api, rec := serve(t, http.StatusOK, `{"ok":true,"result":{"message_id":9911}}`)
	msg := carrying(staged("offer.pdf", "application/pdf", "one"))
	msg.Text = ""
	msg.ReplyToMessageID = 0

	if _, err := api.SendFiles(context.Background(), "1:secret", msg); err != nil {
		t.Fatalf("SendFiles: %v", err)
	}
	fields, _ := formOf(t, rec)
	for _, absent := range []string{"caption", "reply_parameters"} {
		if value, present := fields[absent]; present {
			t.Errorf("the form carries %s = %q for a message that named none", absent, value)
		}
	}
}

// An album over the bound is refused HERE rather than by Telegram, and the
// distinction is the whole point: the group is atomic on validation, so a
// provider refusal costs the whole upload, and this one costs nothing. Nothing
// must reach the provider — an error return is only half the property.
func TestSendFilesRefusesAnAlbumLargerThanOneMessageCarries(t *testing.T) {
	api, rec := serve(t, http.StatusOK, `{"ok":true,"result":[{"message_id":9911}]}`)
	files := make([]connector.OutboundFile, 0, maxSendableFiles+1)
	for i := range maxSendableFiles + 1 {
		files = append(files, staged("offer.pdf", "application/pdf", strings.Repeat("x", i+1)))
	}

	_, err := api.SendFiles(context.Background(), "1:secret", carrying(files...))
	if !errors.Is(err, ErrRequestRejected) {
		t.Fatalf("SendFiles with %d files → %v, want ErrRequestRejected", len(files), err)
	}
	if rec.calls() != 0 {
		t.Errorf("%d requests reached the provider for an album this connector refuses", rec.calls())
	}
}

// The caption bound is exact and measured at this boundary: 1024 accepted, 1025
// refused. Counted in RUNES, so a multibyte caption is not refused for being
// wide.
func TestSendFilesRefusesACaptionLongerThanTheProviderTakes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		text    string
		refused bool
	}{
		{"at the bound", strings.Repeat("ä", maxCaptionRunes), false},
		{"one rune over", strings.Repeat("ä", maxCaptionRunes+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, rec := serve(t, http.StatusOK, `{"ok":true,"result":{"message_id":9911}}`)
			msg := carrying(staged("offer.pdf", "application/pdf", "one"))
			msg.Text = tc.text

			_, err := api.SendFiles(context.Background(), "1:secret", msg)
			switch {
			case tc.refused && !errors.Is(err, ErrRequestRejected):
				t.Fatalf("a %d-rune caption → %v, want ErrRequestRejected", len([]rune(tc.text)), err)
			case tc.refused && rec.calls() != 0:
				t.Errorf("%d requests reached the provider for a caption this connector refuses", rec.calls())
			case !tc.refused && err != nil:
				t.Fatalf("a caption exactly at the %d-rune bound was refused: %v", maxCaptionRunes, err)
			}
		})
	}
}

// A file over the per-file bound is refused before the upload rather than after
// a 413, because the upload is what costs the time: a full album spends minutes
// on the wire before Telegram answers.
func TestSendFilesRefusesAFileLargerThanOneCarries(t *testing.T) {
	api, rec := serve(t, http.StatusOK, `{"ok":true,"result":{"message_id":9911}}`)
	oversize := staged("scan.tiff", "image/tiff", strings.Repeat("x", maxSendableBytesPerFile+1))

	if _, err := api.SendFiles(context.Background(), "1:secret", carrying(oversize)); !errors.Is(err, ErrRequestRejected) {
		t.Fatalf("SendFiles with an oversize file → %v, want ErrRequestRejected", err)
	}
	if rec.calls() != 0 {
		t.Errorf("%d requests reached the provider for a file this connector refuses", rec.calls())
	}
}

// Reaching the upload path with nothing to upload is a programming error, not a
// text message: sending an empty album in its place would put a bare caption
// where a document was staged, which is the silent strip this whole seam exists
// to prevent.
func TestSendFilesRefusesAMessageCarryingNoFiles(t *testing.T) {
	api, rec := serve(t, http.StatusOK, `{"ok":true,"result":{"message_id":9911}}`)

	if _, err := api.SendFiles(context.Background(), "1:secret", carrying()); !errors.Is(err, ErrRequestRejected) {
		t.Fatalf("SendFiles with no files → %v, want ErrRequestRejected", err)
	}
	if rec.calls() != 0 {
		t.Errorf("%d requests reached the provider for a message with nothing to upload", rec.calls())
	}
}

// A nameless or hostile filename must not be able to write its own headers. The
// upload builds the Content-Disposition line by hand, so this is the case that
// keeps extension.SafeFilename in that path.
func TestSendFilesNeverLetsAFilenameWriteItsOwnHeaders(t *testing.T) {
	api, rec := serve(t, http.StatusOK, `{"ok":true,"result":{"message_id":9911}}`)

	if _, err := api.SendFiles(context.Background(), "1:secret", carrying(
		staged("in\r\nContent-Type: text/html\r\n\r\nvoice.pdf", "application/pdf", "one"),
	)); err != nil {
		t.Fatalf("SendFiles: %v", err)
	}
	_, files := formOf(t, rec)
	if len(files) != 1 {
		t.Fatalf("%d file parts attached, want 1 — a filename that broke the encoding produces more", len(files))
	}
	if strings.ContainsAny(files[0].filename, "\r\n") {
		t.Errorf("the attached filename %q still carries a line break", files[0].filename)
	}
	if files[0].contentType != "application/pdf" {
		t.Errorf("content type = %q, want the declared application/pdf — a filename rewrote the header", files[0].contentType)
	}
}

// The upload path shares api.go's ONE status verdict rather than growing a second
// opinion. 413 is the oversize refusal the per-file bound is meant to catch
// first; when it does reach here it must classify as the definite refusal it is.
func TestSendFilesReachesTheSameStatusVerdictAsEveryOtherCall(t *testing.T) {
	api, _ := serve(t, http.StatusRequestEntityTooLarge, `{"ok":false,"description":"Request Entity Too Large"}`)

	_, err := api.SendFiles(context.Background(), "1:secret", carrying(staged("offer.pdf", "application/pdf", "one")))
	if !errors.Is(err, ErrRequestRejected) {
		t.Fatalf("a 413 upload → %v, want ErrRequestRejected", err)
	}
}

// ok=true with no message id is Telegram ACCEPTING the album, so it may be on
// its way and a retry would deliver it twice. It takes the reachability sentinel
// for exactly the reason SendMessage states for the same case.
func TestSendFilesTreatsAnIdlessResultAsAnUnknownOutcome(t *testing.T) {
	for _, tc := range []struct{ name, result string }{
		{"an empty album", `[]`},
		{"an album whose first message has no id", `[{"date":1}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, _ := serve(t, http.StatusOK, `{"ok":true,"result":`+tc.result+`}`)

			_, err := api.SendFiles(context.Background(), "1:secret", carrying(
				staged("offer.pdf", "application/pdf", "one"),
				staged("terms.pdf", "application/pdf", "two"),
			))
			if !errors.Is(err, ErrUnreachable) {
				t.Fatalf("SendFiles → %v, want ErrUnreachable so no retry sends a second copy", err)
			}
		})
	}
}
