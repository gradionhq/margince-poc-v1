// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// The upload path: how a message's files reach Telegram (ADR-0086/A131). Every
// number below is measured against a live bot rather than read off the
// documentation, and the const block says what each measurement was.
//
// Apart from api.go because it is a different request ENCODING —
// multipart/form-data, with a JSON document living inside one of its form fields
// and one part per file — sharing only the response verdict, which it reaches
// through httpAPI.verdict rather than through a second copy. api.go is also at
// its length ceiling, and the upload path is not a variation on `call`.
//
// Two rules shape everything here, and both are safety properties rather than
// preferences:
//
// ONE CALL PER MESSAGE. A message with files goes out as sendDocument (exactly
// one) or sendMediaGroup (two to ten), never as a text call followed by a file
// call. A split send has a window in which the customer holds the words without
// the documents, and no observer can tell that window from a message still in
// flight.
//
// A UNIFORM DOCUMENT ALBUM. Every file travels as type `document`, including an
// image. Telegram refuses a group mixing documents and photos outright, so
// grouping by type would mean deciding per message whether one message becomes
// two provider calls — reintroducing the split send above. It also preserves the
// file: Telegram recompresses a `photo`, and a re-encoded contract scan is a
// worse record than the bytes the rep attached. The cost is real and accepted —
// an image arrives as a downloadable file rather than an inline picture.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The three bounds this connector declares it can carry, each measured against
// a live bot rather than read off the documentation.
//
// maxSendableFiles is what sendMediaGroup accepts, and it is 10 rather than 1
// because the album proved ATOMIC ON VALIDATION: a group holding one bad item
// came back ok=false with the chat's message_id advanced by exactly one across
// the attempt, so the valid item was never delivered. It is also the contract's
// own `attachment_ids` cap, and TestTheSendAttachmentCapMatchesTheContract binds
// the two — a bound below the contract's refuses a request the contract calls
// legal, and a bound above it is the only thing standing between one request and
// an unbounded upload, because nothing in this stack validates a body against
// its schema.
//
// maxSendableBytesPerFile is deliberately the INBOUND getFile cap rather than
// the higher 50 MB send limit (55 MB measured a 413). Two reasons: a file this
// installation could not receive is a strange thing to promise to send, and a
// full album at this size uploads in about two minutes against the send job's
// five-minute timeout — a margin that a larger per-file cap would spend.
//
// maxCaptionRunes is exact and measured at the boundary: 1024 accepted, 1025
// refused with `message caption is too long`. RUNES, not bytes, because it is a
// provider's character count.
const (
	maxSendableFiles        = 10
	maxSendableBytesPerFile = 20 << 20
	maxCaptionRunes         = 1024
)

// uploadBudget is the whole-request deadline for one upload. It is sized rather
// than picked, and the sizing has TWO terms, because the job it runs inside
// spends time before the upload starts.
//
// The upload itself: a message's files are capped at 20 MiB in aggregate by the
// read seam that loads them, and 20 MiB measured about twelve seconds on an
// ordinary uplink. The rest of the job: that same 20 MiB comes out of the
// blobstore first, and the seat, consent and pacing gates each cost a database
// round trip. Three minutes is an order of magnitude over the measured upload
// and still leaves two of the send job's five minutes for everything upstream of
// it — a budget sized against the upload ALONE would let the job be killed
// mid-send, which reports an outcome Telegram never gave for a message that may
// well have arrived.
//
// It replaces the 30-second bound every other Bot API call rides. That bound is
// right for a JSON round trip and fatal here: an upload cut off mid-send answers
// ErrUnreachable, the send path classifies that as an outcome nobody knows, and
// the delivery parks without a retry — every time, for a message this connector
// declares it carries.
//
// The cost of holding a worker this long is real and is recorded rather than
// waved past: the send job runs on the SHARED default queue, so concurrent
// attachment sends can occupy it for minutes while Telegram polls and capture
// syncs wait behind them. That is a queue-topology fix, not a budget one
// (#2063) — shortening this budget instead would just move the failure back to
// cutting uploads off mid-send.
const uploadBudget = 3 * time.Minute

// The two provider methods the upload path uses. Named because the method a
// message took is reported in every error from here, and an operator reading one
// needs the same spelling the Bot API documentation uses.
const (
	methodSendDocument   = "sendDocument"
	methodSendMediaGroup = "sendMediaGroup"
)

// mediaKindDocument is the ONE media kind this connector sends, and one constant
// covers both places it is spelled because the Bot API means the same thing in
// both: sendDocument's `document` form field and an album item's `type` name the
// same kind, which is why a document cannot be uploaded as a photo or grouped
// with one.
const mediaKindDocument = "document"

// inputMediaDocument is one item of a sendMediaGroup album. Only the document
// type is ever built (see this file's opening comment), and only the first item
// of a group carries the caption — Telegram renders a group's caption from the
// first item that has one, so a caption on every item repeats the text under
// every file.
type inputMediaDocument struct {
	Type    string `json:"type"`
	Media   string `json:"media"`
	Caption string `json:"caption,omitempty"`
}

// SendFiles transmits one message and everything staged with it in a single
// provider call, returning the id a later reply threads under.
//
// For an album that is the FIRST message id of the group: Telegram numbers each
// item of an album separately but anchors a reply to a single message, and the
// first item is the one a client shows the reply against.
func (a *httpAPI) SendFiles(ctx context.Context, token string, m OutboundChannelMessage) (int64, error) {
	method, err := uploadMethod(m)
	if err != nil {
		return 0, err
	}
	form, contentType, err := uploadForm(m, method)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(token, method), form)
	if err != nil {
		// The cause is deliberately NOT wrapped. Its one failure mode here is a
		// *url.Error, whose message carries the whole URL — and the bot token
		// rides that URL's path, so wrapping would put a live credential into
		// the delivery's failure note and every log line quoting it.
		return 0, fmt.Errorf("telegram: the %s request could not be built: %w", method, ErrRequestRejected)
	}
	req.Header.Set("Content-Type", contentType)
	var result json.RawMessage
	if err := a.verdict(a.clientWithBudget(uploadBudget), req, method, &result); err != nil {
		return 0, err
	}
	return firstMessageID(result, method, len(m.Files))
}

// uploadMethod refuses a file set this connector has declared it cannot carry,
// then picks the provider method for what is left.
//
// The refusals are deliberately a SECOND check: the shared carriage gate already
// measured this message against Carriage(), but it measured it against what the
// connector CLAIMS, so this is the last place a claim the send path cannot
// honour can be caught — and it is caught before any provider I/O, so nothing
// half-arrives.
//
// One file takes sendDocument rather than a one-item album because the Bot API
// documents sendMediaGroup as taking two to ten items; a single-item group is
// undocumented ground, and the one-call-per-message rule holds either way.
func uploadMethod(m OutboundChannelMessage) (string, error) {
	if err := carriable(m); err != nil {
		return "", err
	}
	if len(m.Files) == 1 {
		return methodSendDocument, nil
	}
	return methodSendMediaGroup, nil
}

// carriable states the three bounds as refusals. Each names the limit and the
// measured value, because these reach a parked delivery's reason where a human
// has to decide what to do about it — "too large" without the number tells them
// to guess.
//
// Every CARRIAGE refusal answers connector.ErrFilesNotCarried, which is the sentinel
// written for exactly this case and is what makes the delivery PARK instead of
// climbing the retry ladder: none of these can come out differently on a second
// attempt, and a ladder spent on a deterministic refusal re-reads every file
// from the blobstore per rung and then parks under a reason naming no cause.
func carriable(m OutboundChannelMessage) error {
	switch {
	case len(m.Files) == 0:
		// The one case that is NOT a carriage refusal: the caller chose this
		// path, so an empty set is a programming error rather than a message
		// nobody can carry, and sending an empty album in its place would put a
		// bare caption where a document was staged.
		return fmt.Errorf("telegram: the upload path was handed a message carrying no files: %w", ErrRequestRejected)
	case len(m.Files) > maxSendableFiles:
		return fmt.Errorf("telegram: %d files is more than the %d one message carries: %w",
			len(m.Files), maxSendableFiles, connector.ErrFilesNotCarried)
	case utf8.RuneCountInString(m.Text) > maxCaptionRunes:
		// A message with files carries its text as a caption, which is bounded far
		// below a text-only message. It cannot be truncated (the rep wrote it) and
		// it cannot be split off into its own call (the rule above), so it refuses.
		return fmt.Errorf("telegram: a message carrying files holds its text in a caption, and %d characters is over the %d a caption takes: %w",
			utf8.RuneCountInString(m.Text), maxCaptionRunes, connector.ErrFilesNotCarried)
	}
	for i, file := range m.Files {
		if len(file.Body) == 0 {
			// Telegram refuses an empty document, and refusing it HERE is the
			// difference between a park and a 400 that reads as a provider
			// condition: a file with no bytes has none on the next attempt either,
			// so the ladder would spend itself and then park naming no cause.
			//
			// This is the one refusal here that is NOT a declared bound, and that
			// is a gap rather than a design: Carriage has no field for it, so the
			// gate above cannot refuse it and the composer cannot warn about it.
			// The real fix is to refuse an empty attachment at the door (#2062) —
			// no transport can send one — after which this becomes the
			// belt-and-braces it should be rather than the only line of defence.
			return fmt.Errorf("telegram: %q has no content, and a file with no bytes cannot be sent: %w",
				partName(i, file), connector.ErrFilesNotCarried)
		}
		if int64(len(file.Body)) > maxSendableBytesPerFile {
			// Named exactly as the wire would have named it — partName is the one
			// spelling — so a human reading the parked reason and a human reading
			// the chat are looking at the same file.
			return fmt.Errorf("telegram: %q is %d bytes, over the %d one file carries: %w",
				partName(i, file), len(file.Body), maxSendableBytesPerFile, connector.ErrFilesNotCarried)
		}
	}
	return nil
}

// uploadForm encodes the message as multipart/form-data, returning the body and
// the content type that describes it.
//
// The whole album is buffered in memory, so a send peaks at roughly twice the
// album's size. What bounds that is the pair of declared limits above — ten
// files at 20 MiB — and stating the peak beside the bound that caps it is the
// point: the alternative, streaming the parts, buys headroom nothing here asks
// for at the cost of a request whose length is unknown before it is sent.
func uploadForm(m OutboundChannelMessage, method string) (*bytes.Buffer, string, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	fields, err := uploadFields(m, method)
	if err != nil {
		return nil, "", err
	}
	for _, field := range fields {
		if err := form.WriteField(field.name, field.value); err != nil {
			return nil, "", fmt.Errorf("telegram: encoding the %s request: %w", method, err)
		}
	}
	for i, file := range m.Files {
		if err := writeFilePart(form, i, file); err != nil {
			return nil, "", fmt.Errorf("telegram: encoding the %s request: %w", method, err)
		}
	}
	if err := form.Close(); err != nil {
		return nil, "", fmt.Errorf("telegram: closing the %s request: %w", method, err)
	}
	return &body, form.FormDataContentType(), nil
}

// formField is one name/value pair of the form. A slice rather than a map so the
// encoded request is byte-identical run to run: a body that reordered itself is
// a body nobody can diff against a captured one.
type formField struct{ name, value string }

// uploadFields is the non-file half of the form: who it goes to, what it says,
// and what it answers. The file parts are attached separately and referenced
// from here by the attach:// scheme, which is how the Bot API joins a JSON field
// to a multipart part.
func uploadFields(m OutboundChannelMessage, method string) ([]formField, error) {
	fields := []formField{{"chat_id", strconv.FormatInt(m.ChatID, 10)}}
	if method == methodSendDocument {
		fields = append(fields, formField{mediaKindDocument, attachRef(0)})
		if m.Text != "" {
			// An absent caption and an empty one are different on the wire:
			// Telegram renders a stated empty caption as a blank line under the
			// file.
			fields = append(fields, formField{"caption", m.Text})
		}
	} else {
		album, err := mediaGroup(m)
		if err != nil {
			return nil, err
		}
		fields = append(fields, formField{"media", album})
	}
	if m.ReplyToMessageID != 0 {
		anchor, err := json.Marshal(map[string]int64{"message_id": m.ReplyToMessageID})
		if err != nil {
			return nil, fmt.Errorf("telegram: encoding the %s reply anchor: %w", method, err)
		}
		// reply_parameters is the current spelling and it is a JSON document
		// inside a form field, exactly as the JSON path sends it as a nested
		// object — the deprecated reply_to_message_id is not sent, so a Telegram
		// deprecation cannot silently drop the threading this system relies on.
		fields = append(fields, formField{"reply_parameters", string(anchor)})
	}
	return fields, nil
}

// mediaGroup renders the album descriptor sendMediaGroup takes.
func mediaGroup(m OutboundChannelMessage) (string, error) {
	items := make([]inputMediaDocument, 0, len(m.Files))
	for i := range m.Files {
		item := inputMediaDocument{Type: mediaKindDocument, Media: attachRef(i)}
		if i == 0 {
			item.Caption = m.Text
		}
		items = append(items, item)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("telegram: encoding the album: %w", err)
	}
	return string(encoded), nil
}

// attachName and attachRef are the two halves of one join: the multipart part's
// field name, and how a JSON field points at it. Spelled together so a rename
// cannot leave a media item referring to a part that is not there — which
// Telegram answers as a refusal naming neither side.
func attachName(index int) string { return "file" + strconv.Itoa(index) }
func attachRef(index int) string  { return "attach://" + attachName(index) }

// quotedFieldEscape escapes what a quoted multipart header parameter cannot hold
// literally, matching mime/multipart's own escaping of the same two characters.
//
// mime.FormatMediaType is deliberately NOT used for the filename, though it is
// used for the content type below. It would RFC 2231-encode a non-ASCII name
// into `filename*=utf-8”…`, and neither browsers nor the Bot API speak that
// dialect in a form part — the stdlib's own CreateFormFile escapes exactly these
// two characters and leaves UTF-8 literal, which is the encoding every multipart
// reader agrees on.
var quotedFieldEscape = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// partName is the filename one file travels under, and the name every message
// about that file uses. Sanitized through extension.SafeFilename because it
// lands in a HEADER here: a name carrying a line break would end the
// Content-Disposition line early and let the rest of it be read as headers of
// its own. SafeFilename is the one spelling of that removal in this tree, and it
// also answers the empty case — a nameless file gets a positional name rather
// than an unnamed part the Bot API refuses.
func partName(index int, file connector.OutboundFile) string {
	return extension.SafeFilename(file.Filename, index+1)
}

// declaredType is the content type a part announces.
//
// It goes through extension.SendableContentType for the reason the filename goes
// through SafeFilename: this value is written verbatim into a header, and it
// arrives from whatever the upload declared — nothing on the write path that
// stored it validated it. That helper is the ONE spelling of the question, which
// the mail renderer asks too; a second spelling here is a second answer about
// which content types survive a send.
func declaredType(file connector.OutboundFile) string {
	return mime.FormatMediaType(extension.SendableContentType(file.ContentType))
}

// writeFilePart attaches one file's bytes under the name a media item points at.
//
// The part is built by hand rather than through CreateFormFile because that
// helper declares every file application/octet-stream, and the content type is
// what decides whether the recipient's client offers to preview the document or
// only to download it. Building it by hand is why BOTH header values are
// sanitized rather than one: they sit in the same block, and a value that can
// end its line early can write whatever follows.
func writeFilePart(form *multipart.Writer, index int, file connector.OutboundFile) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="`+attachName(index)+
		`"; filename="`+quotedFieldEscape.Replace(partName(index, file))+`"`)
	header.Set("Content-Type", declaredType(file))
	part, err := form.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := part.Write(file.Body); err != nil {
		return err
	}
	return nil
}

// firstMessageID reads the reply anchor out of either result shape: one message
// for sendDocument, an array of them for sendMediaGroup — and it insists the
// array holds ONE MESSAGE PER FILE.
//
// That count is the only evidence this side ever gets that the album went whole.
// Telegram numbers each item separately, so a group of three that answers with
// two messages has not substantiated the third, and reporting success would
// record a timeline row claiming files the provider never confirmed. The
// atomic-on-validation behaviour says that should not happen; this is what
// notices if it does.
//
// Every unusable answer takes ErrUnreachable rather than a refusal, for the
// reason SendMessage states for the same case: ok=true is Telegram ACCEPTING the
// message, so it may well be on its way, and reading that as a refusal invites a
// retry that delivers a second copy. An album that half-arrived is exactly the
// outcome nobody can know.
func firstMessageID(raw json.RawMessage, method string, files int) (int64, error) {
	type sent struct {
		MessageID int64 `json:"message_id"`
	}
	var messages []sent
	if method == methodSendDocument {
		var one sent
		if err := json.Unmarshal(raw, &one); err != nil {
			return 0, fmt.Errorf("telegram: decoding the %s result: %w", method, ErrUnreachable)
		}
		messages = []sent{one}
	} else if err := json.Unmarshal(raw, &messages); err != nil {
		return 0, fmt.Errorf("telegram: decoding the %s result: %w", method, ErrUnreachable)
	}
	if len(messages) != files {
		return 0, fmt.Errorf("telegram: %s carried %d file(s) and answered for %d: %w",
			method, files, len(messages), ErrUnreachable)
	}
	if messages[0].MessageID == 0 {
		return 0, fmt.Errorf("telegram: %s answered without a message id: %w", method, ErrUnreachable)
	}
	return messages[0].MessageID, nil
}
