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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
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

	// The bound refusals answer ErrFilesNotCarried rather than this package's own
	// rejection sentinel, and that is what makes the delivery PARK: none of them
	// can come out differently on a second attempt.
	_, err := api.SendFiles(context.Background(), "1:secret", carrying(files...))
	if !errors.Is(err, connector.ErrFilesNotCarried) {
		t.Fatalf("SendFiles with %d files → %v, want ErrFilesNotCarried", len(files), err)
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
			case tc.refused && !errors.Is(err, connector.ErrFilesNotCarried):
				t.Fatalf("a %d-rune caption → %v, want ErrFilesNotCarried", len([]rune(tc.text)), err)
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

	if _, err := api.SendFiles(context.Background(), "1:secret", carrying(oversize)); !errors.Is(err, connector.ErrFilesNotCarried) {
		t.Fatalf("SendFiles with an oversize file → %v, want ErrFilesNotCarried", err)
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

// A file with no bytes is refused here rather than by Telegram, which refuses an
// empty document too — but as a 400, which reads as a provider condition and
// climbs the retry ladder. A file with no content has none on the next attempt
// either, so the refusal has to be the deterministic class.
func TestSendFilesRefusesAFileWithNoContent(t *testing.T) {
	api, rec := serve(t, http.StatusOK, `{"ok":true,"result":{"message_id":9911}}`)

	_, err := api.SendFiles(context.Background(), "1:secret", carrying(staged("empty.pdf", "application/pdf", "")))
	if !errors.Is(err, connector.ErrFilesNotCarried) {
		t.Fatalf("SendFiles with an empty file → %v, want ErrFilesNotCarried", err)
	}
	if rec.calls() != 0 {
		t.Errorf("%d requests reached the provider for a file with nothing in it", rec.calls())
	}
}

// The album's message count is the ONLY evidence this side gets that the group
// went whole: Telegram numbers each item separately, so three files answered with
// two messages have not substantiated the third. Reporting success would record a
// timeline row claiming files the provider never confirmed — and the honest class
// is the unknown outcome, because a half-arrived album is precisely what nobody
// can find out about afterwards.
func TestSendFilesRefusesToClaimAnAlbumTheProviderDidNotAnswerFor(t *testing.T) {
	api, _ := serve(t, http.StatusOK, `{"ok":true,"result":[{"message_id":9911},{"message_id":9912}]}`)

	_, err := api.SendFiles(context.Background(), "1:secret", carrying(
		staged("one.pdf", "application/pdf", "one"),
		staged("two.pdf", "application/pdf", "two"),
		staged("three.pdf", "application/pdf", "three"),
	))
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("a 3-file album answered with 2 messages → %v, want ErrUnreachable so no retry sends a second copy", err)
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

// The content type is the OTHER half of the same hand-built header block, and it
// arrives from whatever the upload declared — nothing on the write path that
// stored it validated it. A value that can end its line early can write whatever
// follows, so it is defended exactly as the filename is, and a value that cannot
// be represented becomes the honest fallback rather than a header nobody wrote.
func TestSendFilesNeverLetsAContentTypeWriteItsOwnHeaders(t *testing.T) {
	for _, tc := range []struct{ name, declared, want string }{
		{
			"a type that would inject a second part header",
			"application/pdf\r\nContent-Disposition: form-data; name=\"chat_id\"\r\n\r\n999999\r\nX: ",
			"application/octet-stream",
		},
		{"a type that is not a media type at all", "not a media type", "application/octet-stream"},
		// The case a bare mime.FormatMediaType silently discarded: a parameter is
		// not a reason to throw the type away, and a mail-captured text part
		// routinely carries one.
		{"an honest type with a parameter", "text/plain; charset=utf-8", "text/plain; charset=utf-8"},
		{"no declared type at all", "", "application/octet-stream"},
		{"an honest type, carried through", "image/png", "image/png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, rec := serve(t, http.StatusOK, `{"ok":true,"result":{"message_id":9911}}`)

			if _, err := api.SendFiles(context.Background(), "1:secret",
				carrying(staged("offer.pdf", tc.declared, "one"))); err != nil {
				t.Fatalf("SendFiles: %v", err)
			}
			fields, files := formOf(t, rec)
			if len(files) != 1 {
				t.Fatalf("%d file parts attached, want 1 — a content type that broke the encoding produces more", len(files))
			}
			if files[0].contentType != tc.want {
				t.Errorf("content type = %q, want %q", files[0].contentType, tc.want)
			}
			// The injected line above names chat_id; the real one must be the only
			// one that answered.
			if fields["chat_id"] != "778899" {
				t.Errorf("chat_id = %q, want the recipient's own 778899 — a header rewrote the form", fields["chat_id"])
			}
		})
	}
}

// deadlineProbe reports the whole-request budget a call was actually given.
//
// http.Client.Timeout is implemented as a deadline on the request context, so
// reading that deadline off the outgoing request measures the real budget
// exactly, with no clock to wait on and nothing to sleep through.
type deadlineProbe struct {
	inner http.RoundTripper
	left  time.Duration
}

func (p *deadlineProbe) RoundTrip(req *http.Request) (*http.Response, error) {
	if deadline, ok := req.Context().Deadline(); ok {
		p.left = time.Until(deadline)
	}
	return p.inner.RoundTrip(req)
}

// An upload must NOT ride the bound written for a JSON round trip, and this is
// the case that says so from outside.
//
// The 30-second short bound caps the WHOLE request, body transmission included.
// A 20 MiB document — let alone a full album — cannot cross the wire inside it,
// and being cut off mid-send answers ErrUnreachable, which the send path is
// obliged to treat as an outcome nobody knows and never retry. The result would
// be that every message this connector DECLARES it carries parks, permanently,
// on the first attempt. So the property is not "the upload has some timeout" but
// "the upload's budget is bigger than the one every other call rides", and the
// text path must keep the short one.
func TestTheUploadIsGivenABudgetAnUploadCanMeet(t *testing.T) {
	if uploadBudget <= httpTimeout {
		t.Fatalf("the upload budget is %s and the short call bound is %s; an upload cannot be "+
			"given less room than a JSON round trip", uploadBudget, httpTimeout)
	}
	// It must also stay UNDER the send job's own timeout: a budget at or above the
	// job's would let the job be killed first, reporting an outcome Telegram never
	// gave for a message that may well have arrived.
	//
	// The job's timeout is READ from the job contract, never restated here. A copy
	// of the number would leave this — the only gate on the relationship — green
	// while somebody lowered the real timeout underneath it, which is the exact
	// shape of a test that supplies its own version of production.
	spec, declared := jobs.SpecFor("comms_send_email")
	if !declared {
		t.Fatal("comms_send_email is not a declared job kind; this gate has nothing to hold the upload budget against")
	}
	if uploadBudget >= spec.Timeout.Fixed {
		t.Fatalf("the upload budget is %s and the send job's timeout is %s; the job would be "+
			"killed before the upload could answer", uploadBudget, spec.Timeout.Fixed)
	}

	for _, tc := range []struct {
		name string
		call func(API) error
		want time.Duration
	}{
		{"the upload", func(a API) error {
			_, err := a.SendFiles(context.Background(), "1:secret", carrying(staged("offer.pdf", "application/pdf", "one")))
			return err
		}, uploadBudget},
		{"a text message", func(a API) error {
			_, err := a.SendMessage(context.Background(), "1:secret", OutboundChannelMessage{ChatID: 778899, Text: "hi"})
			return err
		}, httpTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, srv := serveWithProbe(t, `{"ok":true,"result":{"message_id":9911}}`)
			if err := tc.call(api); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			// Compared as "more than most of the budget" rather than to the exact
			// value: the deadline is set before the request leaves, so a few
			// microseconds of it are always already spent.
			if floor := tc.want - tc.want/10; srv.left < floor {
				t.Errorf("%s was given %s of budget, want about %s", tc.name, srv.left, tc.want)
			}
			if ceiling := tc.want; srv.left > ceiling {
				t.Errorf("%s was given %s of budget, more than the %s it should have", tc.name, srv.left, ceiling)
			}
		})
	}
}

// serveWithProbe is serve with the outgoing request's budget measured. The
// stand-in answers one canned body; what is under test is what reached it, not
// what came back.
func serveWithProbe(t *testing.T, body string) (API, *deadlineProbe) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("writing the fixture response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	probe := &deadlineProbe{inner: srv.Client().Transport}
	return NewAPI(&http.Client{Timeout: httpTimeout, Transport: probe}, srv.URL), probe
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
