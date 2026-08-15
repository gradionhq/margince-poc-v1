// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What one reading of one document does, from claiming the run record to
// closing it.
//
// The division of labour with documentextract.go is: that file owns the
// question put to the model and what may come back, this one owns the run — the
// claim, the lane, and the three outcomes a rep can be shown. Keeping the
// outcomes here is deliberate, because the difference between them is the
// product: "still reading", "read it and it states none of them", and "could
// not read it" must never collapse into one another (RD-AC-N-2).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// maxDocumentBytes bounds what may be sent as an input part.
//
// It is derived from the wire, not chosen for roundness: inline bytes are
// base64 on every carrying adapter, which costs four bytes for every three, so
// this becomes ~10.7 MB of request body. Both adapters that carry documents
// today accept a request an order of magnitude under their vendors' ~20 MB
// ceiling at that size, leaving the prompt and the schema room that does not
// have to be counted per call.
//
// DOC-PARAM-4 lets a 25 MB file be ATTACHED, which is a different question from
// what one model call carries. A file above this is refused as a reading rather
// than truncated: half a scanned contract read confidently is worse than no
// reading at all, because nothing on the panel would say which half it saw.
const maxDocumentBytes = 8 << 20

// maxDocumentTextChars bounds the text lane, matching what a transcript reading
// addresses (activities.MaxReadableTranscriptChars) — the same question of how
// much prose one call can hold steadily, asked of the same models.
const maxDocumentTextChars = activities.MaxReadableTranscriptChars

// documentTextMIMEs are the media types whose bytes ARE their text, so no
// parser stands between the file and the model.
//
// This is the lane the survey of other BYOK systems calls the
// provider-independent one, reached without any of their machinery: a file that
// is already text needs no OCR engine, no document-intelligence service and no
// vision-capable binding, and its quotes can be checked against the document
// verbatim (RD-AC-N-4) — which the bytes lane can never do.
var documentTextMIMEs = []string{
	"text/plain", "text/csv", "text/markdown", "text/html", "application/json", "message/rfc822",
}

// DocumentExtractor reads an attached document for the deal facts it states.
type DocumentExtractor struct {
	pool  *pgxpool.Pool
	brain documentCompleter
	log   *slog.Logger
}

// documentCompleter is the completion seam for a lane whose input may be a
// document: it answers what it can CARRY as well as how to call it.
//
// The ordinary completer seam is not enough here, and the reason is the one the
// LiteLLM-style capability registry exists for. A lane that learns its binding
// is text-only by sending the bytes and being refused has already written a
// failed attempt into the operator's own call trace — for a configuration that
// is not failing, merely text-only. Asking first is what lets "this binding
// cannot read images" be reported as the plain fact it is.
type documentCompleter interface {
	completer
	// AttachmentMIMEs is what a caller may hand this lane, in
	// model.CarriesMIME's spelling. Empty means documents cannot go to it.
	AttachmentMIMEs() []string
}

// NewDocumentExtractor builds the engine over the pool and one model lane.
func NewDocumentExtractor(pool *pgxpool.Pool, brain documentCompleter, log *slog.Logger) *DocumentExtractor {
	return &DocumentExtractor{pool: pool, brain: brain, log: log}
}

// documentReadStore is the slice of the activities store one reading drives.
// Named so the run can be tested against the real store or a double without
// either standing in for the whole module.
type documentReadStore interface {
	BeginExtractionRead(ctx context.Context, readID ids.UUID, reclaimAfter time.Duration) (activities.ExtractionRead, error)
	FinishExtractionRead(ctx context.Context, readID ids.UUID, outcome activities.ExtractionReadOutcome) error
	OpenAttachment(ctx context.Context, id ids.UUID) (crmcontracts.Attachment, io.ReadCloser, error)
}

// Read performs one reading: claim the run, put the document to the model on
// whichever lane its type and the binding allow, and close the run with what
// happened.
//
// It returns an error only for a fault the JOB should retry — the model lane
// being down, the database being unreachable. A reading that legitimately could
// not be used closes the run and returns nil, because retrying would ask the
// same question of the same document and get the same answer.
func (d *DocumentExtractor) Read(ctx context.Context, store documentReadStore, readID, attachmentID ids.UUID) error {
	if _, err := store.BeginExtractionRead(ctx, readID, activities.ExtractionReadLease); err != nil {
		return err
	}
	src, detail, err := d.sourceFor(ctx, store, attachmentID)
	if err != nil {
		if terminal, msg := unreadableDocument(err); terminal {
			return d.finishEmpty(ctx, store, readID, msg)
		}
		// Anything else — the database unreachable, the object store down — is
		// the JOB's to retry. Closing the reading here would turn a blip into a
		// permanent verdict the rep has to notice and undo.
		return err
	}
	if detail != "" {
		// The document is fine and this installation cannot read it. A completed
		// reading saying so, not a failure: nothing is broken and nothing a rep
		// does will change it (RD-AC-N-7). No model call was made.
		return d.finishEmpty(ctx, store, readID, detail)
	}
	fields, err := d.ask(ctx, src)
	if err != nil {
		if errors.Is(err, errRefusedDocument) {
			d.log.WarnContext(ctx, "document reading refused",
				"attachment_extraction_id", readID, "attachment_id", attachmentID, "reason", err)
			return d.fail(ctx, store, readID,
				"the model's reading of this document could not be used; the document is unchanged and can be read again")
		}
		return err
	}
	return store.FinishExtractionRead(ctx, readID, activities.ExtractionReadOutcome{
		Status: activities.ExtractionReadDone,
		Detail: emptyReadingDetail(fields),
		Fields: fields,
	})
}

// emptyReadingDetail says why a completed reading offered nothing, and says
// nothing when it offered something.
//
// A reading that grounded no field is a correct and common answer — plenty of
// documents state none of the four — but an empty panel that does not explain
// itself reads as a broken feature, which is the one thing this whole shape
// exists to prevent.
func emptyReadingDetail(fields []extraction.ExtractedField) string {
	for _, f := range fields {
		if !f.Omitted {
			return ""
		}
	}
	return "this document states none of the deal fields clearly enough to offer one"
}

// sourceFor decides which lane this document takes, and reads it.
//
// It answers one of three ways, and the middle one is the point: a source to
// read, or a detail saying this installation cannot read this document, or an
// error. The detail is not a failure — the document is intact and the binding
// is working, they simply do not meet.
func (d *DocumentExtractor) sourceFor(
	ctx context.Context, store documentReadStore, attachmentID ids.UUID,
) (documentSource, string, error) {
	meta, body, err := store.OpenAttachment(ctx, attachmentID)
	if err != nil {
		return documentSource{}, "", err
	}
	defer func() {
		if err := body.Close(); err != nil {
			d.log.WarnContext(ctx, "closing document body", "attachment_id", attachmentID, "error", err)
		}
	}()
	// ContentType is optional on the row; a document that never declared one
	// takes neither lane, which is the honest answer — the alternative is
	// sniffing bytes here, a second content-type authority beside DOC-PARAM-9's.
	mime := ""
	if meta.ContentType != nil {
		mime = strings.ToLower(strings.TrimSpace(*meta.ContentType))
	}
	// One byte past the bound is what distinguishes "exactly at the limit" from
	// "truncated to the limit"; without it a document of exactly maxDocumentBytes
	// and one of a gigabyte both arrive as a full buffer and look identical.
	bytes, err := io.ReadAll(io.LimitReader(body, maxDocumentBytes+1))
	if err != nil {
		return documentSource{}, "", fmt.Errorf("read document bytes: %w", err)
	}
	if len(bytes) > maxDocumentBytes {
		return documentSource{}, fmt.Sprintf(
			"this document is larger than the %d MB one reading carries; a reading of part of it could not say which part it saw",
			maxDocumentBytes>>20), nil
	}
	if model.CarriesMIME(documentTextMIMEs, mime) {
		src, detail := d.textSource(meta, bytes)
		return src, detail, nil
	}
	if model.CarriesMIME(d.brain.AttachmentMIMEs(), mime) {
		return documentSource{
			Part:     model.Attachment{MIME: mime, Bytes: bytes, Name: meta.Filename},
			Filename: meta.Filename,
		}, "", nil
	}
	if mime == "" {
		return documentSource{}, "this document declares no content type, so nothing can say how to read it", nil
	}
	return documentSource{}, fmt.Sprintf(
		"this installation's model cannot read a %s document; a file whose text can be read directly, or a model bound to carry documents, would be read",
		mime), nil
}

// textSource takes the text lane, where the document's bytes are its text.
func (d *DocumentExtractor) textSource(meta crmcontracts.Attachment, raw []byte) (documentSource, string) {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return documentSource{}, "this document carries no text to read"
	}
	if len(text) > maxDocumentTextChars {
		return documentSource{}, fmt.Sprintf(
			"this document is %d characters, and one reading addresses at most %d",
			len(text), maxDocumentTextChars)
	}
	return documentSource{Text: text, Filename: meta.Filename}, ""
}

// ask puts one document to the model and returns what it may act on.
func (d *DocumentExtractor) ask(ctx context.Context, src documentSource) ([]extraction.ExtractedField, error) {
	resp, err := d.brain.Complete(ctx, documentExtractRequest(src))
	if err != nil {
		if errors.Is(err, model.ErrAttachmentUnsupported) {
			// The binding declared it carries this type and then refused it on
			// the wire. That is the two halves of one declaration disagreeing,
			// which is a configuration fault rather than a fault of this
			// document — so it is refused, not retried.
			return nil, fmt.Errorf("%w: the model refused a document type its binding declares it carries: %w",
				errRefusedDocument, err)
		}
		return nil, err
	}
	return readDocumentFields(resp.Text)
}

// unreadableDocument separates a refusal a rep can act on from a fault the job
// should retry, and answers with the message the rep is shown.
//
// Only the typed refusals reach status_detail. A raw err.Error() there would
// put a driver string ("failed to connect to host=…") in front of a rep on the
// one field this feature exists to make readable, and would settle a transient
// blip as a permanent verdict.
func unreadableDocument(err error) (terminal bool, detail string) {
	if detail, ok := activities.ScanGateHTTPError(err); ok {
		return true, detail.Detail
	}
	if errors.Is(err, apperrors.ErrNotFound) {
		return true, "this document is no longer available to read"
	}
	if errors.Is(err, activities.ErrBlobstoreUnconfigured) {
		return true, "this installation stores no document bytes, so there is nothing to read"
	}
	return false, ""
}

// finishEmpty closes a reading that completed without offering a field: the
// document was read, or could honestly not be, and nothing is broken.
func (d *DocumentExtractor) finishEmpty(ctx context.Context, store documentReadStore, readID ids.UUID, detail string) error {
	return store.FinishExtractionRead(ctx, readID, activities.ExtractionReadOutcome{
		Status: activities.ExtractionReadDone,
		Detail: detail,
	})
}

// fail closes the run with a reason a rep can act on. A failure to record the
// failure is returned, so a run cannot be left claimed and silent.
func (d *DocumentExtractor) fail(ctx context.Context, store documentReadStore, readID ids.UUID, detail string) error {
	return store.FinishExtractionRead(ctx, readID, activities.ExtractionReadOutcome{
		Status: activities.ExtractionReadFailed,
		Detail: detail,
	})
}
